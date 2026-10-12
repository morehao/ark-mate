package manager

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// loadTranscripts 在 daemon 启动（SetTranscriptDir 之后）回读 transcript 目录，重建会话列表与
// 元数据（标题/状态/时间），并把历史事件注入 eventbus——从而把「M1 内存态重启即失」放宽为
// 「transcript 落盘 → 重启重放」，让管理面在重启后仍有会话可见、旧会话可经 WS/history 回看。
// 这是轻量的「会话重启存活」，不引入 DB（落库属 M2）。加载为尽力而为：坏文件告警跳过，不阻断启动。
func (s *Service) loadTranscripts() {
	if s.tx == nil || s.tx.dir == "" {
		return
	}
	files, err := filepath.Glob(filepath.Join(s.tx.dir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		return
	}
	sort.Strings(files)
	for _, path := range files {
		rec := loadTranscriptFile(path)
		if rec == nil {
			continue
		}
		s.registerSession(rec.session)
		// 注入历史事件到 bus（agg=sessionID），使 history/WS 可对旧会话回放。
		if s.bus != nil {
			for _, line := range rec.lines {
				s.bus.Publish(line.typ, rec.session.ID, line.version, line.payload)
			}
		}
	}
	if n := legacyTranscriptSkips.Load(); n > 0 {
		// 汇总一条：逐文件的 WARN 会被日志轮转冲掉，而"这次启动被丢掉了几个会话"是
		// 排障时要看的那一眼（处置命令在逐文件 WARN 里）。
		glog.Warnw(context.Background(),
			"transcripts: 有旧形状会话 id 的文件被跳过（平台会话 id 只由 objid 铸：ses_<26 位 ULID>）",
			"skipped", n, "scanned", len(files), "dispose", legacyTranscriptDisposeHint)
	}
	glog.Debugw(context.Background(), "transcripts loaded", "count", len(files))
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

// transcriptLine 是回读时保留的事件原语（type + 聚合版本 + 载荷），用于注入 bus。
type transcriptLine struct {
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

	for sc.Scan() {
		line := sc.Bytes()
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
			// 非 header（异常）→ 该文件视为不可回读。
			return nil
		}
		var ev wireTranscriptLine
		if err := json.Unmarshal(line, &ev); err != nil || ev.Type == "" {
			continue // 跳过脏行（如误写的 header 行）
		}
		lastType = ev.Type
		if ev.TS != "" {
			lastTS = ev.TS
		}
		if !titleSet && ev.Type == BusUserMessage {
			if text, _ := ev.Payload["text"].(string); text != "" {
				firstUserText = deriveTitle(text)
				titleSet = true
			}
		}
		lines = append(lines, transcriptLine{typ: ev.Type, version: int64(len(lines)) + 1, payload: ev.Payload})
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
			ID:      sessionID,
			AgentID: hdr.AgentID,
			// memory 模式以 transcript 为事实源：工作空间与其"是否代建"标志都从 header 回灌
			// （旧文件缺 workspace_temporary → false，历史会话本就不是代建的）。
			Cwd:                hdr.Cwd,
			WorkspaceTemporary: hdr.WorkspaceTemporary,
			RuntimeHint:        runtime.NormalizeID(hdr.Runtime),
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

// TranscriptEvent 是一条可复读的事件原语（type + payload）。
type TranscriptEvent struct {
	Type    string
	Payload map[string]any
}

// TranscriptData 是单个 transcript 文件的复读结果（会话元数据 + 事件原语）。
type TranscriptData struct {
	SessionID   string
	AgentID     string
	RuntimeHint string
	Title       string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Lines       []TranscriptEvent
}

// ReadTranscriptFile 复读一个 JSONL transcript 文件（首行 header + 事件行）。
// 坏文件/非本仓库 schema 返回 (nil, false)。
func ReadTranscriptFile(path string) (*TranscriptData, bool) {
	rec := loadTranscriptFile(path)
	if rec == nil {
		return nil, false
	}
	data := &TranscriptData{
		SessionID:   rec.session.ID,
		AgentID:     rec.session.AgentID,
		RuntimeHint: runtime.NormalizeID(rec.session.RuntimeHint),
		Title:       rec.session.Title,
		Status:      rec.session.Status,
		CreatedAt:   rec.session.CreatedAt,
		UpdatedAt:   rec.session.UpdatedAt,
		Lines:       make([]TranscriptEvent, 0, len(rec.lines)),
	}
	for _, l := range rec.lines {
		data.Lines = append(data.Lines, TranscriptEvent{Type: l.typ, Payload: l.payload})
	}
	return data, true
}
