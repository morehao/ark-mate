package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/morehao/golib/glog"
)

// 会话 wire transcript JSONL（M1 #5，对齐 codeg acp_transcript.rs，§5.3/§5.7）：
// 每 session 一个 JSONL 文件，schema v1，append-only（写后不重写）——
// 作为「续看/调试/未来跨 agent 会话导入」的原始事实源：daemon 重启后 bus 历史丢失，
// transcript 仍保有每轮 prompt/update 原语（用户输入与 agent 增量/终态）。
//
// 文件：<dir>/<sessionID>.jsonl
// 首行 header，之后每行一个事件原语：
//
//	{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"...","agent_id":"...",
//	 "runtime":"...","cwd":"...","created_at":"..."}
//	{"id":"evt_...","seq":2,"ts":"...","type":"session.user_message","payload":{...}} // seq = **会话内**位次（v21 起 0 起）
//
// **v2 = 事件行多一支 `id`（身份），`seq` 保留（位次）**（2026-10-12，设计
// `.dsh/docs/specs/2026-10-12-data-model-v17-design.md`）：v1 的 `seq` 在装载时被丢掉、按行号
// 重铸，于是同一个事件**每次重启换一个位次**（客户端书签里的 since_seq 随之失效）。v2 让身份与
// 位次都随数据落盘：装载用 `PublishAt` 恢复两者，`mate migrate` 按 `id` 判重。
// **v1 文件不再被回读**（旧文件的 seq 只是"文件内位次"，与库里的水位没有可比性）——装载面
// 传 WARN + 计数跳过，`mate transcripts prune --older-than 1s` 是它的处置出口（不自动删）。
//
// 记录在 Service.publish 统一汇口挂载（域事件 = prompt/update 原语），
// 写入失败只告警不阻断会话（transcript 是尽力而为的旁路事实源）。
//
// 粒度（③ 流式增量合并后）：文本增量（session.message/thought）按 25ms / 4KB / 64 条
// 攒批落行，**一行可能覆盖多个 delta**（同一条消息的 text 已拼接）；工具卡/权限卡/提问卡/
// 用户消息/终态事件不受合并影响，仍是一条事件一行。故"每轮 prompt/update 原语"应读作
// "每轮事件原语（文本增量已攒批）"，前端/导入侧按 msg_id 拼接的语义不变。

// wireTranscriptSchema transcript 文件 schema 名（v2：事件行带 `id`，见文件头注释）。
const wireTranscriptSchema = "ark-mate.wire-transcript.v2"

// legacyWireTranscriptSchemaV1 是 v17 之前的 schema 名。
//
// 它必须**被写出来**才能被认出来：装载面见到它就整份跳过 + WARN（v1 的 `seq` 只是文件内位次，
// 拿它当全局水位就是把两套语义混起来）。处置出口是 `mate transcripts prune --older-than 1s`
// ——**不自动删**（ADR-5：transcript 是 memory 模式的事实源，自动删不可逆）。
const legacyWireTranscriptSchemaV1 = "ark-mate.wire-transcript.v1"

// wireTranscriptHeader 文件首行（header）。
// v19 起没有 `agent_id`：会话身份只剩 `runtime`。老文件首行里若还留着 `agent_id`，
// `json.Unmarshal` 会忽略它（读面宽容，不需要迁移）。
type wireTranscriptHeader struct {
	Schema    string `json:"schema"`
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	Runtime   string `json:"runtime"`
	Cwd       string `json:"cwd,omitempty"`
	// WorkspaceTemporary 该工作空间是否由平台代建。**memory 模式以 transcript 为事实源**，
	// 因此这个权威标志必须落 header；旧文件缺该字段 → 零值 false（历史会话视为非临时，语义正确）。
	WorkspaceTemporary bool   `json:"workspace_temporary,omitempty"`
	CreatedAt          string `json:"created_at"`
}

// wireTranscriptLine 事件原语行。
type wireTranscriptLine struct {
	// ID 事件身份（`evt_<26 位 ULID>`，v2 起必填）。它与 `seq` 一起落盘、一起被装载面恢复：
	// 身份回答"是哪一条"（导入判重、诊断引用），seq 回答"在哪"（回放顺序与续传水位）。
	ID      string         `json:"id"`
	Seq     int64          `json:"seq"`
	TS      string         `json:"ts"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// transcriptRecorder 按 session 维护打开的 JSONL 文件（单写者：publish 串行路径）。
type transcriptRecorder struct {
	dir string

	mu     sync.Mutex
	open   map[string]*os.File
	failed map[string]bool // 该 session 文件写入已失败（跳过后续，防刷屏）
}

// newTranscriptRecorder 构造 recorder（dir 为空则禁用）。
func newTranscriptRecorder(dir string) *transcriptRecorder {
	return &transcriptRecorder{
		dir:    dir,
		open:   make(map[string]*os.File),
		failed: make(map[string]bool),
	}
}

// begin 为会话创建/打开 JSONL 并写 header（幂等：已开则忽略）。
func (r *transcriptRecorder) begin(sessionID, runtimeID, cwd string, workspaceTemporary bool) {
	if r == nil || r.dir == "" || sessionID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.open[sessionID]; ok || r.failed[sessionID] {
		return
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		r.markFailedLocked(sessionID)
		return
	}
	path := filepath.Join(r.dir, sessionID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		glog.Warnw(context.Background(), "transcript: open failed", "session", sessionID, "err", err)
		r.markFailedLocked(sessionID)
		return
	}
	h := wireTranscriptHeader{
		Schema:             wireTranscriptSchema,
		Version:            2,
		SessionID:          sessionID,
		Runtime:            runtimeID,
		Cwd:                cwd,
		WorkspaceTemporary: workspaceTemporary,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339Nano),
	}
	// 仅在文件为空（新会话）时写 header：重启后 resume 同一会话时 O_APPEND 打开的是已有文件，
	// 不重写 header，保持「首行=header」不变式（loadTranscripts 依赖它）。
	st, stErr := f.Stat()
	if stErr != nil || st.Size() == 0 {
		if err := writeJSONLine(f, h); err != nil {
			_ = f.Close()
			glog.Warnw(context.Background(), "transcript: header write failed", "session", sessionID, "err", err)
			r.markFailedLocked(sessionID)
			return
		}
	}
	r.open[sessionID] = f
}

// appendLine 追加一个事件原语行（`id` 与 `seq` 都由上游 `publishRaw` 传下来，这里绝不重铸）。
func (r *transcriptRecorder) appendLine(sessionID, eventID string, seq int64, typ string, payload map[string]any) {
	if r == nil || r.dir == "" {
		return
	}
	line := wireTranscriptLine{ID: eventID, Seq: seq, TS: time.Now().UTC().Format(time.RFC3339Nano), Type: typ, Payload: payload}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.open[sessionID]
	if !ok || r.failed[sessionID] {
		return
	}
	if err := writeJSONLine(f, line); err != nil {
		glog.Warnw(context.Background(), "transcript: append failed", "session", sessionID, "err", err)
		_ = f.Close()
		delete(r.open, sessionID)
		r.markFailedLocked(sessionID)
	}
}

// closeAll 关闭全部文件（daemon 退出/demo 收尾）。
func (r *transcriptRecorder) closeAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, f := range r.open {
		_ = f.Close()
		delete(r.open, id)
	}
}

func (r *transcriptRecorder) markFailedLocked(sessionID string) {
	r.failed[sessionID] = true
}

// writeJSONLine 序列化 v 并写一行（编码器自带换行）。
func writeJSONLine(f *os.File, v any) error {
	enc := json.NewEncoder(f)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	return nil
}
