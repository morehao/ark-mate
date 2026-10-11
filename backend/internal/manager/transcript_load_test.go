package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoconversation"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/pkg/runtime"
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
	header := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"ses_old1","agent_id":"agent-opencode","runtime":"opencode","cwd":"/tmp","created_at":"2026-09-07T03:12:40Z"}`
	lines := []string{
		header,
		`{"seq":2,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"列出当前目录文件"}}`,
		`{"seq":3,"ts":"2026-09-07T03:12:42Z","type":"session.thought","payload":{"text":"thinking"}}`,
		`{"seq":4,"ts":"2026-09-07T03:12:43Z","type":"turn.ended","payload":{"stop_reason":"end_turn"}}`,
	}
	writeTranscriptJSONL(t, dir, "ses_old1.jsonl", lines)

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)

	conversations, err := svc.ListConversations(false, "", "")
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("期望回灌 1 个会话，got %d: %+v", len(conversations), conversations)
	}
	s := conversations[0]
	if s.ID != "ses_old1" || s.Title != "列出当前目录文件" || s.Status != ConversationStatusCompleted {
		t.Fatalf("回灌元数据不符: %+v", s)
	}
	if s.CreatedAt == "" || s.UpdatedAt == "" {
		t.Fatalf("回灌时间缺失: %+v", s)
	}

	// 事件已注入 bus：history 可按 agg 回放。
	hist, err := svc.ConversationHistory(t.Context(), "ses_old1", dtoconversation.HistoryQuery{Limit: 100})
	if err != nil {
		t.Fatalf("ConversationHistory: %v", err)
	}
	if len(hist.Events) != 3 {
		t.Fatalf("期望注入 3 条事件，got %d", len(hist.Events))
	}
	if hist.Events[0].AggregID != "ses_old1" || hist.Events[2].Type != BusTurnEnded {
		t.Fatalf("注入事件不符: %+v", hist.Events)
	}
}

// TestLoadTranscriptsNoTerminal 无终态事件 → 状态 in_progress（供 UI 停止自愈）。
func TestLoadTranscriptsNoTerminal(t *testing.T) {
	dir := t.TempDir()
	header := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"ses_old2","agent_id":"agent-opencode","runtime":"opencode","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, "ses_old2.jsonl", []string{
		header,
		`{"seq":2,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"hi"}}`,
	})
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)
	conversations, _ := svc.ListConversations(false, "", "")
	if len(conversations) != 1 || conversations[0].Status != ConversationStatusInProgress {
		t.Fatalf("无终态应为 in_progress: %+v", conversations)
	}
}

// TestLoadTranscriptsIdempotent 对同一会话重复回灌不重复注册。
func TestLoadTranscriptsIdempotent(t *testing.T) {
	dir := t.TempDir()
	header := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"ses_old3","agent_id":"agent-opencode","runtime":"opencode","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, "ses_old3.jsonl", []string{header})
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)
	svc.SetTranscriptDir(dir) // 二次调用：也应幂等（无重复注入/覆盖）
	conversations, _ := svc.ListConversations(false, "", "")
	if len(conversations) != 1 {
		t.Fatalf("重复回灌导致的会话数异常: %d", len(conversations))
	}
}

// TestLoadTranscriptsWorkspace memory 模式以 transcript 为事实源：工作空间与其
// 「是否平台代建」权威标志都从 header 回灌；旧文件缺该字段 → 非临时（历史会话语义正确）。
func TestLoadTranscriptsWorkspace(t *testing.T) {
	dir := t.TempDir()
	temporaryHeader := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"ses_ws_temp","agent_id":"agent-opencode","runtime":"opencode","cwd":"/home/u/.ark-mate/workspaces/ses_ws_temp","workspace_temporary":true,"created_at":"2026-09-07T03:12:40Z"}`
	legacyHeader := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"ses_ws_legacy","agent_id":"agent-opencode","runtime":"opencode","cwd":"/work/alpha","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, "ses_ws_temp.jsonl", []string{temporaryHeader})
	writeTranscriptJSONL(t, dir, "ses_ws_legacy.jsonl", []string{legacyHeader})

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)

	byID := map[string]dtoconversation.ConversationResp{}
	conversations, err := svc.ListConversations(false, "", "")
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	for _, c := range conversations {
		byID[c.ID] = c
	}
	if got := byID["ses_ws_temp"]; got.Cwd != "/home/u/.ark-mate/workspaces/ses_ws_temp" || !got.WorkspaceTemporary {
		t.Fatalf("代建标记/工作空间未回灌: %+v", got)
	}
	if got := byID["ses_ws_legacy"]; got.Cwd != "/work/alpha" || got.WorkspaceTemporary {
		t.Fatalf("旧文件应视为非临时: %+v", got)
	}

	// 按工作空间过滤对回灌会话同样生效（recent/browse 面依赖此能力）。
	only, _ := svc.ListConversations(false, "/work/alpha", "")
	if len(only) != 1 || only[0].ID != "ses_ws_legacy" {
		t.Fatalf("回灌会话按 cwd 过滤不符: %+v", only)
	}
}
