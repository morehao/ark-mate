package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
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
	header := `{"schema":"ark-forge.wire-transcript.v1","version":1,"session_id":"ses_old1","agent_id":"agent-opencode","runtime":"opencode","cwd":"/tmp","created_at":"2026-09-07T03:12:40Z"}`
	lines := []string{
		header,
		`{"seq":2,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"列出当前目录文件"}}`,
		`{"seq":3,"ts":"2026-09-07T03:12:42Z","type":"session.thought","payload":{"text":"thinking"}}`,
		`{"seq":4,"ts":"2026-09-07T03:12:43Z","type":"turn.ended","payload":{"stop_reason":"end_turn"}}`,
	}
	writeTranscriptJSONL(t, dir, "ses_old1.jsonl", lines)

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)

	conversations, err := svc.ListConversations(false)
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
	hist, err := svc.ConversationHistory(t.Context(), "ses_old1", handler.HistoryQuery{Limit: 100})
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
	header := `{"schema":"ark-forge.wire-transcript.v1","version":1,"session_id":"ses_old2","agent_id":"agent-opencode","runtime":"opencode","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, "ses_old2.jsonl", []string{
		header,
		`{"seq":2,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"hi"}}`,
	})
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)
	conversations, _ := svc.ListConversations(false)
	if len(conversations) != 1 || conversations[0].Status != ConversationStatusInProgress {
		t.Fatalf("无终态应为 in_progress: %+v", conversations)
	}
}

// TestLoadTranscriptsIdempotent 对同一会话重复回灌不重复注册。
func TestLoadTranscriptsIdempotent(t *testing.T) {
	dir := t.TempDir()
	header := `{"schema":"ark-forge.wire-transcript.v1","version":1,"session_id":"ses_old3","agent_id":"agent-opencode","runtime":"opencode","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, "ses_old3.jsonl", []string{header})
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetTranscriptDir(dir)
	svc.SetTranscriptDir(dir) // 二次调用：也应幂等（无重复注入/覆盖）
	conversations, _ := svc.ListConversations(false)
	if len(conversations) != 1 {
		t.Fatalf("重复回灌导致的会话数异常: %d", len(conversations))
	}
}
