package manager

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 会话 wire transcript JSONL（M1 #5，对齐 codeg acp_transcript.rs，§5.3/§5.7）：
// 每 session 一个 JSONL 文件，schema v1，append-only（写后不重写）——
// 作为「续看/调试/未来跨 agent 会话导入」的原始事实源：daemon 重启后 bus 历史丢失，
// transcript 仍保有每轮 prompt/update 原语（用户输入与 agent 增量/终态）。
//
// 文件：<dir>/<sessionID>.jsonl
// 首行 header，之后每行一个事件原语：
//
//	{"schema":"ark-forge.wire-transcript.v1","version":1,"session_id":"...","agent_id":"...",
//	 "runtime":"...","cwd":"...","created_at":"..."}
//	{"seq":2,"ts":"...","type":"session.user_message","payload":{...}}
//
// 记录在 Service.publish 统一汇口挂载（域事件 = prompt/update 原语），
// 写入失败只告警不阻断会话（transcript 是尽力而为的旁路事实源）。

// wireTranscriptSchema transcript 文件 schema 名。
const wireTranscriptSchema = "ark-forge.wire-transcript.v1"

// wireTranscriptHeader 文件首行（header）。
type wireTranscriptHeader struct {
	Schema    string `json:"schema"`
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	Runtime   string `json:"runtime"`
	Cwd       string `json:"cwd,omitempty"`
	CreatedAt string `json:"created_at"`
}

// wireTranscriptLine 事件原语行。
type wireTranscriptLine struct {
	Seq     int64          `json:"seq"`
	TS      string         `json:"ts"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// transcriptRecorder 按 session 维护打开的 JSONL 文件（单写者：publish 串行路径）。
type transcriptRecorder struct {
	dir string
	log *slog.Logger

	mu     sync.Mutex
	open   map[string]*os.File
	failed map[string]bool // 该 session 文件写入已失败（跳过后续，防刷屏）
}

// newTranscriptRecorder 构造 recorder（dir 为空则禁用）。
func newTranscriptRecorder(dir string) *transcriptRecorder {
	return &transcriptRecorder{
		dir:    dir,
		log:    slog.Default(),
		open:   make(map[string]*os.File),
		failed: make(map[string]bool),
	}
}

// begin 为会话创建/打开 JSONL 并写 header（幂等：已开则忽略）。
func (r *transcriptRecorder) begin(sessionID, agentID, runtimeID, cwd string) {
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
		r.log.Warn("transcript: open failed", "session", sessionID, "err", err)
		r.markFailedLocked(sessionID)
		return
	}
	h := wireTranscriptHeader{
		Schema:    wireTranscriptSchema,
		Version:   1,
		SessionID: sessionID,
		AgentID:   agentID,
		Runtime:   runtimeID,
		Cwd:       cwd,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	// 仅在文件为空（新会话）时写 header：重启后 resume 同一会话时 O_APPEND 打开的是已有文件，
	// 不重写 header，保持「首行=header」不变式（loadTranscripts 依赖它）。
	st, stErr := f.Stat()
	if stErr != nil || st.Size() == 0 {
		if err := writeJSONLine(f, h); err != nil {
			_ = f.Close()
			r.log.Warn("transcript: header write failed", "session", sessionID, "err", err)
			r.markFailedLocked(sessionID)
			return
		}
	}
	r.open[sessionID] = f
}

// appendLine 追加一个事件原语行。
func (r *transcriptRecorder) appendLine(sessionID string, seq int64, typ string, payload map[string]any) {
	if r == nil || r.dir == "" {
		return
	}
	line := wireTranscriptLine{Seq: seq, TS: time.Now().UTC().Format(time.RFC3339Nano), Type: typ, Payload: payload}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.open[sessionID]
	if !ok || r.failed[sessionID] {
		return
	}
	if err := writeJSONLine(f, line); err != nil {
		r.log.Warn("transcript: append failed", "session", sessionID, "err", err)
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
