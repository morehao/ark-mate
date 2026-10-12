package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// 测试用平台会话 id：v15 起平台 id 只由 objid 铸（`ses_<26 位 Crockford Base32>`）。
// 夹具用**合法形状的固定字面量**（而不是 objid.New），是为了让 JSONL 字面量保持可读、可 grep。
const (
	sesLegacyA  = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PA"
	sesLegacyB  = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PB"
	sesLegacyC  = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PC"
	sesWsTemp   = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PT"
	sesWsLegacy = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PS"
)

// writeTranscriptJSONL 直接落一个合法 transcript 文件（header + 事件行），用于回读测试。
func writeTranscriptJSONL(t *testing.T, dir, filename string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestLoadTranscriptsReseed 回读 transcript → 会话列表重建（标题/状态/时间）+ 事件注入 bus。
func TestLoadTranscriptsReseed(t *testing.T) {
	dir := t.TempDir()
	header := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"` + sesLegacyA + `","agent_id":"agent-opencode","runtime":"opencode","cwd":"/tmp","created_at":"2026-09-07T03:12:40Z"}`
	lines := []string{
		header,
		`{"seq":2,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"列出当前目录文件"}}`,
		`{"seq":3,"ts":"2026-09-07T03:12:42Z","type":"session.thought","payload":{"text":"thinking"}}`,
		`{"seq":4,"ts":"2026-09-07T03:12:43Z","type":"run.ended","payload":{"stop_reason":"end_turn"}}`,
	}
	writeTranscriptJSONL(t, dir, sesLegacyA+".jsonl", lines)

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)

	sessions, err := svc.ListSessions(false, "", "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("期望回灌 1 个会话，got %d: %+v", len(sessions), sessions)
	}
	s := sessions[0]
	if s.ID != sesLegacyA || s.Title != "列出当前目录文件" || s.Status != SessionStatusCompleted {
		t.Fatalf("回灌元数据不符: %+v", s)
	}
	if s.CreatedAt == "" || s.UpdatedAt == "" {
		t.Fatalf("回灌时间缺失: %+v", s)
	}

	// 事件已注入 bus：history 可按 agg 回放。
	hist, err := svc.SessionHistory(t.Context(), sesLegacyA, dtosession.HistoryQuery{Limit: 100})
	if err != nil {
		t.Fatalf("SessionHistory: %v", err)
	}
	if len(hist.Events) != 3 {
		t.Fatalf("期望注入 3 条事件，got %d", len(hist.Events))
	}
	if hist.Events[0].AggregID != sesLegacyA || hist.Events[2].Type != BusRunEnded {
		t.Fatalf("注入事件不符: %+v", hist.Events)
	}
}

// TestLoadTranscriptsSkipsLegacyShapeID v15（Q10）：旧形状的会话 id **跳过 + 计数**，不做派生。
//
// 为什么必须拒收：旧 transcript 的 header 里是裸 uuid，放行会让数据面同时存在
// `ses_…` 与裸 uuid 两种平台会话 id —— 形状不变式只写在代码里，数据里的形状没有任何守卫看得见。
func TestLoadTranscriptsSkipsLegacyShapeID(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"c-6f1e-legacy","agent_id":"agent-opencode","runtime":"opencode","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, "legacy.jsonl", []string{
		legacy,
		`{"seq":2,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"hi"}}`,
	})

	before := LegacyTranscriptSkips()
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)
	sessions, _ := svc.ListSessions(false, "", "")
	if len(sessions) != 0 {
		t.Fatalf("旧形状会话不该被装载: %+v", sessions)
	}
	if got := LegacyTranscriptSkips() - before; got != 1 {
		t.Fatalf("被跳过的旧形状文件计数不符: got %d want 1", got)
	}
}

// TestLoadTranscriptsNoTerminal 无终态事件 → 状态 in_progress（供 UI 停止自愈）。
func TestLoadTranscriptsNoTerminal(t *testing.T) {
	dir := t.TempDir()
	header := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"` + sesLegacyB + `","agent_id":"agent-opencode","runtime":"opencode","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, sesLegacyB+".jsonl", []string{
		header,
		`{"seq":2,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"hi"}}`,
	})
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)
	sessions, _ := svc.ListSessions(false, "", "")
	if len(sessions) != 1 || sessions[0].Status != SessionStatusInProgress {
		t.Fatalf("无终态应为 in_progress: %+v", sessions)
	}
}

// TestLoadTranscriptsIdempotent 对同一会话重复回灌不重复注册。
func TestLoadTranscriptsIdempotent(t *testing.T) {
	dir := t.TempDir()
	header := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"` + sesLegacyC + `","agent_id":"agent-opencode","runtime":"opencode","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, sesLegacyC+".jsonl", []string{header})
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)
	svc.SetTranscriptDir(dir) // 二次调用：也应幂等（无重复注入/覆盖）
	sessions, _ := svc.ListSessions(false, "", "")
	if len(sessions) != 1 {
		t.Fatalf("重复回灌导致的会话数异常: %d", len(sessions))
	}
}

// TestLoadTranscriptsWorkspace memory 模式以 transcript 为事实源：工作空间与其
// 「是否平台代建」权威标志都从 header 回灌；旧文件缺该字段 → 非临时（历史会话语义正确）。
func TestLoadTranscriptsWorkspace(t *testing.T) {
	dir := t.TempDir()
	temporaryHeader := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"` + sesWsTemp + `","agent_id":"agent-opencode","runtime":"opencode","cwd":"/home/u/.ark-mate/workspaces/` + sesWsTemp + `","workspace_temporary":true,"created_at":"2026-09-07T03:12:40Z"}`
	legacyHeader := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"` + sesWsLegacy + `","agent_id":"agent-opencode","runtime":"opencode","cwd":"/work/alpha","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, sesWsTemp+".jsonl", []string{temporaryHeader})
	writeTranscriptJSONL(t, dir, sesWsLegacy+".jsonl", []string{legacyHeader})

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)

	byID := map[string]dtosession.SessionResp{}
	sessions, err := svc.ListSessions(false, "", "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	for _, c := range sessions {
		byID[c.ID] = c
	}
	if got := byID[sesWsTemp]; got.Cwd != "/home/u/.ark-mate/workspaces/"+sesWsTemp || !got.WorkspaceTemporary {
		t.Fatalf("代建标记/工作空间未回灌: %+v", got)
	}
	if got := byID[sesWsLegacy]; got.Cwd != "/work/alpha" || got.WorkspaceTemporary {
		t.Fatalf("旧文件应视为非临时: %+v", got)
	}

	// 按工作空间过滤对回灌会话同样生效（recent/browse 面依赖此能力）。
	only, _ := svc.ListSessions(false, "/work/alpha", "")
	if len(only) != 1 || only[0].ID != sesWsLegacy {
		t.Fatalf("回灌会话按 cwd 过滤不符: %+v", only)
	}
}
