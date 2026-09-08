package manager

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/store"
)

// TestMessagesMaterializedAtTurnEnd 验证 turn 收口 messages 物化：
// 窗口内 user 输入成行（原文）；assistant 文本片段跨多条 message 增量拼接（同 msg_id 合并、
// 换 msg_id 分段）；thought/tool 交错事件进 assistant 行时间线；工具最终态抽入 tool_details；
// 同 run 重建幂等（重复收口不产生重复行）。
func TestMessagesMaterializedAtTurnEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "forge.db")
	ctx := context.Background()
	reg := runtime.NewRegistry(runtime.Spec{ID: "echo", Name: "Echo"})
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	svc := NewService(reg, eventbus.New())
	svc.EnableStore(st)
	svc.registerConversation(&ConversationRec{ID: "s-msg", AgentID: "agent-echo"})

	svc.publish(BusUserMessage, "s-msg", map[string]any{"text": "第一问：关键词甲"})
	svc.publish(BusThought, "s-msg", map[string]any{"text": "思考中", "msg_id": "m1"})
	svc.publish(BusToolCall, "s-msg", map[string]any{
		"tool_call_id": "tc1", "kind": "bash", "input": "ls", "text": "run ls"})
	svc.publish(BusMessage, "s-msg", map[string]any{"text": "第一段回答，含", "msg_id": "m1"})
	svc.publish(BusToolUpdate, "s-msg", map[string]any{
		"tool_call_id": "tc1", "kind": "bash", "output": "a.txt", "ok": true, "text": "done"})
	svc.publish(BusMessage, "s-msg", map[string]any{"text": "关键词乙", "msg_id": "m1"})
	svc.publish(BusTurnEnded, "s-msg", map[string]any{"stop_reason": "end_turn", "session_id": "s-msg"})

	// 第二段（另一轮）换 msg_id → 应拆出新 assistant 行。
	svc.publish(BusUserMessage, "s-msg", map[string]any{"text": "第二问"})
	svc.publish(BusMessage, "s-msg", map[string]any{"text": "另一条回答", "msg_id": "m2"})
	svc.publish(BusTurnEnded, "s-msg", map[string]any{"stop_reason": "end_turn"})

	// 直接读 messages 表断言（读模型查询）。
	hits, err := st.SearchUserMessages(ctx, "s-msg", "关键词")
	if err != nil {
		t.Fatalf("SearchUserMessages: %v", err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Text, "关键词甲") {
		t.Fatalf("user 行应有命中: %+v", hits)
	}

	rows, err := queryMessages(ctx, st, "s-msg")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 { // user×2 + assistant×2
		t.Fatalf("应 4 行: %+v", rows)
	}
	// 第一轮 assistant：同 msg_id=m1 的多条增量拼为一行，文本含两段、时间线含 thought/tool。
	var a0, a1, u0 store.Message
	for _, r := range rows {
		switch {
		case r.Role == "user" && strings.Contains(r.Text, "第一问"):
			u0 = r
		case r.Role == "assistant" && strings.Contains(r.Text, "第一段"):
			a0 = r
		case r.Role == "assistant" && strings.Contains(r.Text, "另一条"):
			a1 = r
		}
	}
	if u0.Text != "第一问：关键词甲" {
		t.Fatalf("user 原文不符: %q", u0.Text)
	}
	if !strings.Contains(a0.Text, "第一段回答，含") || !strings.Contains(a0.Text, "关键词乙") {
		t.Fatalf("assistant 同 msg_id 应拼接: %q", a0.Text)
	}
	if a1.SeqLo == 0 || a1.SeqHi <= a1.SeqLo {
		t.Fatalf("assistant2 窗口异常: %+v", a1)
	}
	if !strings.Contains(a0.EventsJSON, "session.thought") || !strings.Contains(a0.EventsJSON, "session.tool_call") {
		t.Fatalf("assistant 时间线应含 thought/tool: %.400s", a0.EventsJSON)
	}
	var tds []map[string]any
	if err := json.Unmarshal([]byte(a0.ToolDetailsJSON), &tds); err != nil || len(tds) != 1 {
		t.Fatalf("tool_details 应为 1 条: %v %s", err, a0.ToolDetailsJSON)
	}
	if tds[0]["tool_call_id"] != "tc1" || tds[0]["input"] != "ls" || tds[0]["output"] != "a.txt" {
		t.Fatalf("tool_details 内容不符: %+v", tds)
	}
	if a0.SeqHi-a0.SeqLo < 4 {
		t.Fatalf("assistant1 窗口应覆盖 whole turn: %d..%d", a0.SeqLo, a0.SeqHi)
	}
}

// queryMessages 是 store 内部读（测试专用：messages 表全量）。
func queryMessages(ctx context.Context, st *store.Store, conversationID string) ([]store.Message, error) {
	rows, err := st.ListMessages(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// TestSearchConversationsPrefersMessagesTable sqlite 模式：SearchConversations 正文命中走 messages 表
// （无 transcript 亦命中；片段为用户原文折叠预览）。
func TestSearchConversationsPrefersMessagesTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "forge.db")
	ctx := context.Background()
	reg := runtime.NewRegistry(runtime.Spec{ID: "echo", Name: "Echo"})
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	svc := NewService(reg, eventbus.New())
	svc.EnableStore(st)
	svc.registerConversation(&ConversationRec{ID: "s-s", AgentID: "agent-echo"})
	svc.publish(BusUserMessage, "s-s", map[string]any{"text": "帮我部署 forge 服务"})
	svc.publish(BusMessage, "s-s", map[string]any{"text": "好的"})
	svc.publish(BusTurnEnded, "s-s", map[string]any{"stop_reason": "end_turn"})

	hits, err := svc.SearchConversations(ctx, "部署", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "s-s" || !strings.Contains(hits[0].Snippet, "部署 forge") {
		t.Fatalf("sqlite 搜索应命中且带片段: %+v", hits)
	}
	if hits[0].Matches < 1 {
		t.Fatalf("matches 应 ≥1: %+v", hits[0])
	}
	// 无关词不命中。
	if h2, _ := svc.SearchConversations(ctx, "不存在词xyz", 5); len(h2) != 0 {
		t.Fatalf("无关词不应命中: %+v", h2)
	}
}
