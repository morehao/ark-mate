package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/runtime"
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
//	{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"...","runtime":"...",
//	 "cwd":"...","workspace_temporary":true,"created_at":"..."}
//	{"id":"evt_...","seq":2,"ts":"...","type":"session.user_message","payload":{...}} // seq = **会话内**位次（v21 起 0 起）
//
// **v2 = 事件行多一支 `id`（身份），`seq` 保留（位次）**（2026-10-12，设计
// `.dsh/docs/specs/2026-10-12-data-model-v17-design.md`）：v1 的 `seq` 在装载时被丢掉、按行号
// 重铸，于是同一个事件**每次重启换一个位次**（客户端书签里的 since_seq 随之失效）。v2 让身份与
// 位次都随数据落盘：装载用 `PublishAt` 恢复两者，`mate migrate` 按 `id` 判重。
// **v1 文件不再被回读**（旧文件的 seq 只是"文件内位次"，与库里的水位没有可比性）——装载面
// 传 WARN + 计数跳过，`mate transcripts prune --older-than 1s` 是它的处置出口（不自动删）。
//
// 记录在 Plane.publish 统一汇口挂载（域事件 = prompt/update 原语），
// 写入失败只告警不阻断会话（transcript 是尽力而为的旁路事实源）。
//
// 粒度（③ 流式增量合并后）：文本增量（session.message/thought）按 25ms / 4KB / 64 条
// 攒批落行，**一行可能覆盖多个 delta**（同一条消息的 text 已拼接）；工具卡/权限卡/提问卡/
// 用户消息/终态事件不受合并影响，仍是一条事件一行。故"每轮 prompt/update 原语"应读作
// "每轮事件原语（文本增量已攒批）"，前端/导入侧按 msg_id 拼接的语义不变。
//
// P3b-2b：**整块随会话域搬进来**（recorder 本体 + 装载 + 检索一次性）。此前域里只有一个
// `TranscriptSink` 端口、recorder 留在 manager——端口存在的唯一理由是"装载/检索那半还在
// manager，它要用 recorder 的内部状态"，那半搬完端口自然消失（见计划 §3.1.46）。

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
//
// P3b-2b 起它是**域内**方法（此前为了满足 `TranscriptSink` 端口叫 `AppendLine`）：端口随
// 装载/检索搬进来一起消失，导出面不再需要。
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

// ——— 装载（启动回读） ———

// loadTranscripts 在 daemon 启动（SetTranscriptDir 之后）回读 transcript 目录，重建会话列表与
// 元数据（标题/状态/时间），并把历史事件注入 eventbus——从而把「M1 内存态重启即失」放宽为
// 「transcript 落盘 → 重启重放」，让管理面在重启后仍有会话可见、旧会话可经 WS/history 回看。
// 这是轻量的「会话重启存活」，不引入 DB（落库属 M2）。加载为尽力而为：坏文件告警跳过，不阻断启动。
//
// **v17（transcript v2）**：事件行自带 `id`（身份）与 `seq`（位次），装载面**原样恢复两者**
// （`PublishAt`），不再按行号重铸 `seq`。于是"同一个事件在两次启动里位次不同"这件事被消掉，
// 客户端书签里的 `since_seq` 跨重启仍然指向同一条事件。
//
// **v21**：`seq` 是**会话内**位次，所以恢复注入只需要"同一会话内升序"（见下方排序），
// 跨会话的相对顺序不再影响任何号。旧文件里那一列是按旧口径（全局号）写的，装载面照原样恢复
// ——要把它拉直就走 `mate migrate`（导入路径按行序重排位次）。
func (pl *Plane) loadTranscripts() {
	if pl.tx == nil || pl.tx.dir == "" {
		return
	}
	files, err := filepath.Glob(filepath.Join(pl.tx.dir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		return
	}
	sort.Strings(files)
	// 先把全部文件读进内存、再按 `(会话, seq)` **分组有序**注入（理由见下）。
	var pending []pendingTranscriptEvent
	loaded := 0
	for _, path := range files {
		rec := loadTranscriptFile(path)
		if rec == nil {
			continue
		}
		pl.RegisterSession(rec.session)
		loaded++
		for _, line := range rec.lines {
			pending = append(pending, pendingTranscriptEvent{sessionID: rec.session.ID, line: line})
		}
	}
	// 按 `(会话 id, seq)` 升序注入（**不是**按文件名、也不是按逐文件顺序）：
	//   · 文件名字典序与会话的事件位次无关。逐文件注入时，**同一个会话**的若干文件之间
	//     可能乱序（同一会话理论上只有一份文件，但外部同步/复制会造出第二份），
	//     乱序送进 `PublishAt` 就会撞"取号回落"——位次被**改写**，于是"恢复出来的 seq"
	//     与"transcript 里写的 seq"又不一致了；
	//   · v21 起位次只在会话内可比，所以需要的是"**组内**升序"——按 `(会话, seq)` 排
	//     同时给了分组与组内有序，而且比 v21 之前的"全局归并"更弱、更好满足。
	// 同一个 `(会话, seq)` 只可能来自两份互相冲突的文件，用 sort.SliceStable + 文件名序
	// 定型，保证可复现。
	if pl.bus != nil {
		sort.SliceStable(pending, func(i, j int) bool {
			if pending[i].sessionID != pending[j].sessionID {
				return pending[i].sessionID < pending[j].sessionID
			}
			return pending[i].line.seq < pending[j].line.seq
		})
		for _, p := range pending {
			pl.bus.PublishAt(p.sessionID, p.line.seq, p.line.id, p.line.typ, p.line.version, p.line.payload)
		}
	}
	if n := legacyTranscriptSkips.Load(); n > 0 {
		// 汇总一条：逐文件的 WARN 会被日志轮转冲掉，而"这次启动被丢掉了几个会话"是
		// 排障时要看的那一眼（处置命令在逐文件 WARN 里）。
		glog.Warnw(context.Background(),
			"transcripts: 有旧形状会话 id 的文件被跳过（平台会话 id 只由 objid 铸：ses_<26 位 ULID>）",
			"skipped", n, "scanned", len(files), "dispose", legacyTranscriptDisposeHint)
	}
	if n := legacySchemaSkips.Load(); n > 0 {
		glog.Warnw(context.Background(),
			"transcripts: 有 v1 格式（事件行没有 id/seq 身份）的文件被跳过——v17 起事件身份随数据落盘，v1 的 seq 只是文件内位次，回读会把它当成会话水位",
			"skipped", n, "scanned", len(files), "dispose", legacyTranscriptDisposeHint)
	}
	glog.Debugw(context.Background(), "transcripts loaded", "loaded", loaded, "scanned", len(files), "events", len(pending))
}

// pendingTranscriptEvent 是"待注入 bus 的一条事件"（会话 id + 文件内读出的原语）。
//
// 存在的理由是把"读文件"与"注入 bus"分成两步：注入必须按**会话分组、组内按 seq 有序**
// （见 loadTranscripts）。
type pendingTranscriptEvent struct {
	sessionID string
	line      transcriptLine
}

// legacyTranscriptDisposeHint 旧 transcript 的**显式**处置命令。
//
// 刻意不做自动删除（ADR-5）：transcript 是 memory 模式的事实源，自动删是不可逆的；而
// 这条提示出现在逐文件的 WARN 里，刚好在用户看到"会话不见了"的那一刻。
const legacyTranscriptDisposeHint = "mate transcripts prune --older-than 1s"

// legacyTranscriptSkips 本次进程启动时因"会话 id 不是平台形状"被跳过的 transcript 文件数。
var legacyTranscriptSkips atomic.Int64

// LegacyTranscriptSkips 返回被跳过的旧形状 transcript 数（诊断与测试用）。
func LegacyTranscriptSkips() int64 { return legacyTranscriptSkips.Load() }

// legacySchemaSkips 本次进程启动时因"v1 文件格式"被跳过的 transcript 文件数（v17 起）。
var legacySchemaSkips atomic.Int64

// LegacySchemaSkips 返回被跳过的 v1 格式 transcript 数（诊断与测试用）。
func LegacySchemaSkips() int64 { return legacySchemaSkips.Load() }

// transcriptLine 是回读时保留的事件原语（**身份 + 位次 + 聚合版本 + 载荷**），用于注入 bus。
//
// `id`/`seq` 都是文件里写着的值，装载面一个都不重铸（v17）：`version` 仍按**该文件内的行序**
// 推导（重启稳定，且它只是聚合版本、不跨会话比较）。
//
// v21 起 `seq` 是**会话内**位次（0 起）。旧文件（v17–v20 写的）里那一列是全局号——装载面
// 仍然原样恢复它（file 是 memory 模式的事实源，改它等于改写用户手里的历史），于是那些会话
// 的位次会**带着旧口径的洞**；新事件接着最大号往后排。要把它拉直就走 `mate migrate` 导进
// SQLite（导入路径按行序重排位次），这与 v15「旧形状 id 不迁移」是同一条取舍。
type transcriptLine struct {
	id      string
	seq     int64
	typ     string
	version int64
	payload map[string]any
}

// transcriptFile 是回读一个 transcript 文件的结果。
type transcriptFile struct {
	session *SessionRec
	lines   []transcriptLine
}

// loadTranscriptFile 解析一个 JSONL transcript 文件（首行 header + 事件行），
// 返回会话记录与注入用事件；坏文件/非本仓库 schema 返回 nil。
func loadTranscriptFile(path string) *transcriptFile {
	f, err := os.Open(path)
	if err != nil {
		glog.Warnw(context.Background(), "transcript: load open failed", "path", path, "err", err)
		return nil
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20) // 允许大行（工具输出可能很长）

	var hdr *wireTranscriptHeader
	// sessionID 是**已过形状校验**的平台会话 id（v15 起只认 `ses_<26 位 ULID>`）。
	// 单独一个变量是为了让"未经校验就把 header 的 id 当平台 id"在类型上不可能发生：
	// `hdr.SessionID` 只在校验那一处被读。
	var sessionID string
	lines := make([]transcriptLine, 0, 64)
	var lastType string
	var lastTS string
	var firstUserText string
	var titleSet bool
	lineNo := 0

	for sc.Scan() {
		line := sc.Bytes()
		lineNo++
		if len(line) == 0 {
			continue
		}
		if hdr == nil {
			// 首行期望是 header（schema/version 存在）。
			var h wireTranscriptHeader
			if err := json.Unmarshal(line, &h); err == nil && h.Schema == wireTranscriptSchema && h.SessionID != "" {
				// v15（Q10，2026-10-11）：旧数据不要 —— 旧 transcript 的 header 里是裸 uuid，
				// **跳过该文件 + WARN**，既不做确定性派生（`FromLegacy`）也不放行直通。
				//
				// 放行的后果是静默的：数据面会出现 `ses_…` 与裸 uuid 两种会话 id，
				// "平台 id 形状单一"这条不变式在数据上被击穿，而代码侧的守卫看不见数据。
				// 派生的后果是"旧数据其实还在用"，与用户的决定直接矛盾。
				if !objid.IsPlatformID(h.SessionID) {
					legacyTranscriptSkips.Add(1)
					glog.Warnw(context.Background(),
						"transcript: 跳过旧形状会话 id（v15 起平台会话 id 形如 ses_<26 位 ULID>，旧数据不迁移）",
						"path", path, "session_id", h.SessionID, "dispose", legacyTranscriptDisposeHint)
					return nil
				}
				sessionID = h.SessionID
				hdr = &h
				continue
			}
			// v1 格式：schema 名是 v17 之前那一版 → 跳过整份 + WARN（**不能**回读：v1 的 seq
			// 只是文件内位次，而装载面现在会把它当全局水位注入 bus，两套语义混起来就是
			// "位次看起来对、跨重启却对不上"）。
			if err := json.Unmarshal(line, &h); err == nil && h.Schema == legacyWireTranscriptSchemaV1 {
				legacySchemaSkips.Add(1)
				glog.Warnw(context.Background(),
					"transcript: 跳过 v1 格式文件（v17 起事件行带 id/seq 身份；v1 的 seq 只是文件内位次，不是全局水位）",
					"path", path, "dispose", legacyTranscriptDisposeHint)
				return nil
			}
			// 非 header（异常）→ 该文件视为不可回读。
			return nil
		}
		var ev wireTranscriptLine
		if err := json.Unmarshal(line, &ev); err != nil || ev.Type == "" {
			continue // 跳过脏行（如误写的 header 行）
		}
		// v2 事件行必须带**合法身份 + 合法位次**。缺任何一样都说明这份文件不是 v2 写的
		// （手工改动 / 半截写入），整份跳过而不"补一个"——补出来的身份与位次是装载面自己
		// 发明的，与用户手里那份文件对不上，而这正是 v17 要消灭的东西。
		// v21 起 `seq >= 0` 即合法（0 是会话第一条事件的位次），所以下界是 0 而不是 1。
		if !strings.HasPrefix(ev.ID, objid.Event) || !objid.IsPlatformID(ev.ID) || ev.Seq < 0 {
			legacySchemaSkips.Add(1)
			glog.Warnw(context.Background(),
				"transcript: 事件行缺 v2 身份（id 形如 evt_<26 位 ULID> 且 seq >= 0），整份跳过",
				"path", path, "line", lineNo, "id", ev.ID, "seq", ev.Seq, "dispose", legacyTranscriptDisposeHint)
			return nil
		}
		lastType = ev.Type
		if ev.TS != "" {
			lastTS = ev.TS
		}
		if !titleSet && ev.Type == BusUserMessage {
			if text, _ := ev.Payload["text"].(string); text != "" {
				firstUserText = DeriveTitle(text)
				titleSet = true
			}
		}
		lines = append(lines, transcriptLine{
			id: ev.ID, seq: ev.Seq, typ: ev.Type,
			version: int64(len(lines)) + 1, payload: ev.Payload,
		})
	}
	if err := sc.Err(); err != nil {
		glog.Warnw(context.Background(), "transcript: load scan failed", "path", path, "err", err)
	}

	if hdr == nil {
		return nil
	}

	createdAt := parseRFC3339(hdr.CreatedAt)
	updatedAt := parseRFC3339(lastTS)
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}

	return &transcriptFile{
		session: &SessionRec{
			ID: sessionID,
			// memory 模式以 transcript 为事实源：工作空间与其"是否代建"标志都从 header 回灌
			// （旧文件缺 workspace_temporary → false，历史会话本就不是代建的）。
			Cwd:                hdr.Cwd,
			WorkspaceTemporary: hdr.WorkspaceTemporary,
			RuntimeID:          runtime.NormalizeID(hdr.Runtime),
			Status:             statusFromLastEventType(lastType),
			Title:              firstUserText,
			CreatedAt:          createdAt,
			UpdatedAt:          updatedAt,
		},
		lines: lines,
	}
}

// statusFromLastEventType 由最后一条事件类型推导会话终态；无终态事件 → in_progress
// （可能是会话进行中或进程被中断，用户可经「停止」自愈，见 serverBusy 逻辑）。
func statusFromLastEventType(ltype string) string {
	switch ltype {
	case BusRunError:
		return SessionStatusFailed
	case BusRunCancelled:
		return SessionStatusCancelled
	case BusRunEnded:
		return SessionStatusCompleted
	default:
		return SessionStatusInProgress
	}
}

// parseRFC3339 宽容解析时间戳（RFC3339 / RFC3339Nano）；失败返回零值。
func parseRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// ——— 导出面（供 `mate migrate` 等外部命令复读 transcript，避免重复解析） ———

// TranscriptEvent 是一条可复读的事件原语（身份 + 位次 + type + payload）。
//
// v17 起带上 `ID`/`Seq`：`mate migrate` 按 `ID` 判重（同 id 同行 → 跳过、不同 → 报错），
// 而 `Seq` 只做诊断输出（落库时**由 DB 重新分配**全局位次——位次的权威是 DB 那一列）。
type TranscriptEvent struct {
	ID      string
	Seq     int64
	Type    string
	Payload map[string]any
}

// TranscriptData 是单个 transcript 文件的复读结果（会话元数据 + 事件原语）。
type TranscriptData struct {
	SessionID string
	RuntimeID string
	Title     string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
	Lines     []TranscriptEvent
}

// ReadTranscriptFile 复读一个 JSONL transcript 文件（首行 header + 事件行）。
// 坏文件/非本仓库 schema/v1 格式返回 (nil, false)。
//
// P3b-2b：随装载面进域（`mate migrate` 的调用点从 `manager.ReadTranscriptFile` 改为
// `session.ReadTranscriptFile`）——它解析的是**本域的 wire 形状**，留在 manager 就等于让
// manager 反向认识域的数据格式。
func ReadTranscriptFile(path string) (*TranscriptData, bool) {
	rec := loadTranscriptFile(path)
	if rec == nil {
		return nil, false
	}
	data := &TranscriptData{
		SessionID: rec.session.ID,
		RuntimeID: runtime.NormalizeID(rec.session.RuntimeID),
		Title:     rec.session.Title,
		Status:    rec.session.Status,
		CreatedAt: rec.session.CreatedAt,
		UpdatedAt: rec.session.UpdatedAt,
		Lines:     make([]TranscriptEvent, 0, len(rec.lines)),
	}
	for _, l := range rec.lines {
		data.Lines = append(data.Lines, TranscriptEvent{ID: l.id, Seq: l.seq, Type: l.typ, Payload: l.payload})
	}
	return data, true
}

// beginTranscript 为会话写 transcript header（首行）；失败只告警不阻断（见 recorder.begin）。
func (pl *Plane) BeginTranscript(sessionID, runtimeID, cwd string, workspaceTemporary bool) {
	if pl.tx == nil {
		return
	}
	pl.tx.begin(sessionID, runtimeID, cwd, workspaceTemporary)
}

// SetTranscriptDir 启用会话 wire transcript 落盘（目录不存在会自动创建；
// dir 为空 = 禁用，且把已登记的 recorder 摘掉）。须在任何会话开始前调用（serve/demo 装配时）。
// 启用后立即回读已落盘 transcript，重建会话列表与历史（会话重启存活）。
func (pl *Plane) SetTranscriptDir(dir string) {
	if dir == "" {
		pl.tx = nil
		return
	}
	pl.tx = newTranscriptRecorder(dir)
	pl.loadTranscripts()
}

// CloseTranscripts 关闭全部 transcript 文件（daemon 退出/orchestrator.Close 收尾）。
// 先落掉合并器里的尾部文本增量，再关文件——否则长回复的最后一段会丢。
//
// 顺序不能调：`FlushChunks` 之后仍可能有事件落 transcript（flush 本身就是在 publish），
// 所以关文件必须在 flush **之后**。
func (pl *Plane) CloseTranscripts() {
	pl.FlushChunks()
	if pl.tx == nil {
		return
	}
	pl.tx.closeAll()
	pl.tx = nil
}

// transcriptDir 当前 transcript 目录（” = 未启用）。检索面判"有没有 transcript 可扫"用。
func (pl *Plane) transcriptDir() string {
	if pl.tx == nil {
		return ""
	}
	return pl.tx.dir
}
