package manager

import (
	"context"
	"encoding/json"
	"github.com/morehao/ark-mate/internal/domain/session"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
)

// ——— messages 白名单投影的判据（M3） ———
//
// 本文件的判据对应投影的三种**静默**失效方式（都不会让别处的测试变红）：
//
//	① 非对话事件又产出"空行"——界面上多出空气泡，检索与上下文计数被污染（用例 2/3）；
//	② 空正文行留下——同理，且 `mate doctor` 的"空正文"一段会报假阳性（用例 2/3）；
//	③ `ordinal` 每轮从 1 重新数（不是会话内连续）——序号"看起来有"，但"第几条"这个说法
//	   一跨轮就是错的（用例 4）。

// newMsgRig 造一个 sqlite 模式的服务（每用例一个独立库；messages 是读模型，判据都读库）。
func newMsgRig(t *testing.T, sessionID string) (*Service, *store.Store) {
	t.Helper()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	t.Cleanup(func() { _ = st.Close() })
	svc := NewService(testutil.StubRegistry(), eventbus.New())
	svc.EnableStore(st)
	svc.registerSession(&SessionRec{ID: sessionID})
	return svc, st
}

// TestMessagesMaterializedAtRunEnd 白名单投影的基本形状：user 原文一行；assistant 的
// `session.message` 增量按 msg_id 拼接/分段；**thought/tool/usage 不产行、也不进正文**；
// 所有行都有会话内连续的 `ordinal`。
func TestMessagesMaterializedAtRunEnd(t *testing.T) {
	ctx := context.Background()
	svc, st := newMsgRig(t, "s-msg")

	// 开轮三连（run.started → 开轮行 → 用户消息）：v22 起没有开轮行就没有窗口，
	// 终态到达时物化会整轮跳过（不是"少一行"，是一行都不产出）。
	r1, _, _ := beginRun(t, svc, "s-msg", "第一问：关键词甲")
	svc.publish(session.BusThought, "s-msg", map[string]any{"text": "思考中", "msg_id": "m1"})
	svc.publish(session.BusToolCall, "s-msg", map[string]any{
		"tool_call_id": "tc1", "kind": "bash", "input": "ls", "text": "run ls"})
	svc.publish(session.BusMessage, "s-msg", map[string]any{"text": "第一段回答，含", "msg_id": "m1"})
	svc.publish(session.BusToolUpdate, "s-msg", map[string]any{
		"tool_call_id": "tc1", "kind": "bash", "output": "a.txt", "ok": true, "text": "done"})
	svc.publish(session.BusMessage, "s-msg", map[string]any{"text": "关键词乙", "msg_id": "m1"})
	endRun(svc, "s-msg", r1, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})

	// 第二段（另一轮）换 msg_id → 应拆出新 assistant 行。
	r2, _, _ := beginRun(t, svc, "s-msg", "第二问")
	svc.publish(session.BusMessage, "s-msg", map[string]any{"text": "另一条回答", "msg_id": "m2"})
	endRun(svc, "s-msg", r2, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})

	// 检索面（生产上的唯一消费者）先看：user 行命中 UserMessage 检索。
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
	if len(rows) != 4 { // user×2 + assistant×2（thought / tool_call / tool_update **不产行**）
		t.Fatalf("应 4 行（白名单只有 user_message/message）: %+v", rows)
	}
	var a0, a1, u0 model.MessageEntity
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
	// 时间线只含本行的来源事件（`session.message` 增量）：thought/tool 不进投影。
	if strings.Contains(a0.EventsJSON, "session.thought") || strings.Contains(a0.EventsJSON, "session.tool_call") {
		t.Fatalf("白名单投影不该把 thought/tool 收进行的来源事件: %.400s", a0.EventsJSON)
	}
	if !strings.Contains(a0.EventsJSON, "session.message") {
		t.Fatalf("assistant 行的来源事件应是它自己的 message 增量: %.400s", a0.EventsJSON)
	}
	if a0.ToolDetailsJSON != "[]" {
		t.Fatalf("工具最终态不进消息读模型（工具卡由事件读面渲染）: %s", a0.ToolDetailsJSON)
	}
	// 窗口 = 该行自己的事件区间（assistant1 的两段增量之间隔着 tool_update，所以 seq_hi > seq_lo）。
	if a0.SeqLo < 0 || a0.SeqHi <= a0.SeqLo {
		t.Fatalf("assistant1 窗口应覆盖它的两段增量: %d..%d", a0.SeqLo, a0.SeqHi)
	}
	if a1.SeqLo <= 0 || a1.SeqHi != a1.SeqLo {
		t.Fatalf("assistant2 只有一条增量，窗口应是单点: %+v", a1)
	}
	// 序号：会话内 1..4 连续（**跨轮**），与结果集顺序一致；`kind` 仍是空串。
	for i, r := range rows {
		if r.Ordinal != int64(i)+1 {
			t.Fatalf("第 %d 行的 ordinal 应为 %d（会话内连续）: %+v", i, i+1, r)
		}
		if r.Kind != "" {
			t.Fatalf("kind 目前应恒为空串: %+v", r)
		}
	}
}

// TestMaterializeIgnoresNonMessageEvents 窗口里的非消息事件（`config_options`、`usage`、
// `permission_request`）**一行都不产出**。
//
// （计划里这条用例叫 `TestMaterializeIgnoresNon<那个被撤掉的词>Events`——照抄会撞 v15 的词汇收口
// `TestNoConversationVocabulary`，而那条守卫拦的正是这种回流；名字改成"非 message 事件"，
// 判据一字未变。）
//
// 这是白名单投影最容易被"顺手改回去"的一处：让那些事件进 `default` 分支"开一个空行承载时间线"
// 看起来无害，实际会在每个轮次里造出空 assistant 行。
func TestMaterializeIgnoresNonMessageEvents(t *testing.T) {
	ctx := context.Background()
	svc, st := newMsgRig(t, "s-ignore")

	runID, _, _ := beginRun(t, svc, "s-ignore", "问一句")
	svc.publish(session.BusConfigOptions, "s-ignore", map[string]any{"options": []any{}})
	svc.publish(session.BusUsage, "s-ignore", map[string]any{"input_tokens": 3, "output_tokens": 5})
	svc.publish(session.BusPermissionRequest, "s-ignore", map[string]any{"request_id": "p1", "title": "t"})
	svc.publish(session.BusMessage, "s-ignore", map[string]any{"text": "答一句", "msg_id": "m1"})
	endRun(svc, "s-ignore", runID, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})

	rows, err := queryMessages(ctx, st, "s-ignore")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("只该有 user + assistant 两行: %+v", rows)
	}
	for _, r := range rows {
		if r.Text == "" {
			t.Fatalf("不该有空正文行: %+v", r)
		}
		if strings.Contains(r.EventsJSON, "config_options") || strings.Contains(r.EventsJSON, "usage") {
			t.Fatalf("非对话事件不该进任何行: %.300s", r.EventsJSON)
		}
	}
}

// TestMaterializeDropsEmptyAssistantRow 纯 thought/usage 的一步（没有 `session.message`）→
// 不产生 assistant 行（中断/失败轮尤其常见：底座回了思考但没回正文）。
//
// 反证：把 `default` 分支改回"开空行"、或去掉 `flush()` 里的空正文判断，本用例立刻红。
func TestMaterializeDropsEmptyAssistantRow(t *testing.T) {
	ctx := context.Background()
	svc, st := newMsgRig(t, "s-empty")

	runID, _, _ := beginRun(t, svc, "s-empty", "只思考不回答")
	svc.publish(session.BusThought, "s-empty", map[string]any{"text": "想了很多", "msg_id": "m1"})
	svc.publish(session.BusUsage, "s-empty", map[string]any{"input_tokens": 1})
	endRun(svc, "s-empty", runID, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})

	rows, err := queryMessages(ctx, st, "s-empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Role != "user" {
		t.Fatalf("没有正文的一步不该产行（只留 user 行）: %+v", rows)
	}
	// 空文本的 message 增量同样不产行（`text = ''` 是白名单事件，但不产空行）。
	r2, _, _ := beginRun(t, svc, "s-empty", "空回复")
	svc.publish(session.BusMessage, "s-empty", map[string]any{"text": "", "msg_id": "m2"})
	endRun(svc, "s-empty", r2, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})
	if rows, err = queryMessages(ctx, st, "s-empty"); err != nil || len(rows) != 2 {
		t.Fatalf("空正文增量不该产行: %d 行 err=%v", len(rows), err)
	}
}

// TestMaterializeFailedRunWithNoAssistantText 失败轮（`run.error`）没有正文 → 只有 user 行；
// 错误码记在 `runs.error_code` 上，读模型不复制一份"错误消息"。
func TestMaterializeFailedRunWithNoAssistantText(t *testing.T) {
	ctx := context.Background()
	svc, st := newMsgRig(t, "s-fail")

	runID, _, _ := beginRun(t, svc, "s-fail", "会失败的一问")
	svc.publish(session.BusToolCall, "s-fail", map[string]any{"tool_call_id": "tc1", "title": "bash"})
	endRun(svc, "s-fail", runID, session.BusRunError, map[string]any{"code": "runtime_error", "class": "runtime"})

	rows, err := queryMessages(ctx, st, "s-fail")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Role != "user" {
		t.Fatalf("失败轮无正文时只该有 user 行: %+v", rows)
	}
	runs, err := st.ListRuns(ctx, "s-fail")
	if err != nil || len(runs) != 1 {
		t.Fatalf("轮次行应在: %+v err=%v", runs, err)
	}
	if runs[0].Status != session.SessionStatusFailed || runs[0].ErrorCode != "runtime_error" {
		t.Fatalf("失败事实应在 runs 行上: %+v", runs[0])
	}
}

// TestMessageOrdinalIsDensePerSession 三轮会话 → `ordinal` == 1..N 连续（跨轮，不是每轮从头数）。
//
// 反证：去掉 `baseOrdinal`（每轮从 1 起）→ 本用例红（第二/三轮的序号会与第一轮重号，
// `UNIQUE(session_id, ordinal)` 会先一步把插入拦下来）。
func TestMessageOrdinalIsDensePerSession(t *testing.T) {
	ctx := context.Background()
	svc, st := newMsgRig(t, "s-ord")

	for i := 0; i < 3; i++ {
		suffix := string(rune('A' + i))
		runID, _, _ := beginRun(t, svc, "s-ord", "第 "+suffix+" 问")
		svc.publish(session.BusMessage, "s-ord", map[string]any{"text": "答" + suffix, "msg_id": "m" + suffix})
		endRun(svc, "s-ord", runID, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})
	}

	rows, err := queryMessages(ctx, st, "s-ord")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 { // 3 轮 × (user + assistant)
		t.Fatalf("应 6 行: %d", len(rows))
	}
	for i, r := range rows {
		if r.Ordinal != int64(i)+1 {
			t.Fatalf("ordinal 必须会话内连续（1..N）：第 %d 行是 %d", i, r.Ordinal)
		}
	}
}

// TestRebuildIsIdempotent 连续两次整会话重建 → 行集合（除 `id` 外逐列）完全相同。
//
// `id` 每次重铸是刻意的（投影身份不对外），所以判据钉的是**内容**：重复重建不该改变任何
// 面向用户的列（文本/序号/窗口/来源/时刻）。
func TestRebuildIsIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, st := newMsgRig(t, "s-idem")

	r1, _, _ := beginRun(t, svc, "s-idem", "第一问")
	svc.publish(session.BusThought, "s-idem", map[string]any{"text": "想", "msg_id": "t1"})
	svc.publish(session.BusMessage, "s-idem", map[string]any{"text": "答一", "msg_id": "m1"})
	endRun(svc, "s-idem", r1, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})
	r2, _, _ := beginRun(t, svc, "s-idem", "第二问")
	svc.publish(session.BusMessage, "s-idem", map[string]any{"text": "答二", "msg_id": "m2"})
	endRun(svc, "s-idem", r2, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})

	before, err := queryMessages(ctx, st, "s-idem")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("前提：终态物化应已铺出消息行")
	}
	for i := 0; i < 2; i++ {
		if err := svc.sessionPlane.RebuildSessionReadModels(ctx, "s-idem"); err != nil {
			t.Fatalf("RebuildSessionReadModels: %v", err)
		}
	}
	after, err := queryMessages(ctx, st, "s-idem")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("重复重建改了行数：%d → %d", len(before), len(after))
	}
	for i := range before {
		b, a := before[i], after[i]
		if b.ID == a.ID {
			t.Fatalf("重建应重铸行 id（投影身份不对外，但确实每次重铸）: %s", b.ID)
		}
		b.ID, a.ID = "", ""
		bj, _ := json.Marshal(b)
		aj, _ := json.Marshal(a)
		if string(bj) != string(aj) {
			t.Fatalf("重复重建改了内容：\n before %s\n after  %s", bj, aj)
		}
	}
}

// queryMessages 是 store 内部读（测试专用：messages 表全量，seq 序）。
func queryMessages(ctx context.Context, st *store.Store, sessionID string) ([]model.MessageEntity, error) {
	return st.ListMessages(ctx, sessionID)
}

// TestSearchSessionsPrefersMessagesTable sqlite 模式：SearchSessions 正文命中走 messages 表
// （无 transcript 亦命中；片段为用户原文折叠预览）。
func TestSearchSessionsPrefersMessagesTable(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()
	st := testutil.OpenStore(t, dbPath)
	defer st.Close()
	svc := NewService(reg, eventbus.New())
	svc.EnableStore(st)
	svc.registerSession(&SessionRec{ID: "s-s"})
	runID, _, _ := beginRun(t, svc, "s-s", "帮我部署 mate 服务")
	svc.publish(session.BusMessage, "s-s", map[string]any{"text": "好的"})
	endRun(svc, "s-s", runID, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})

	hits, err := sessionPlaneOf(svc).SearchSessions(ctx, "部署", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "s-s" || !strings.Contains(hits[0].Snippet, "部署 mate") {
		t.Fatalf("sqlite 搜索应命中且带片段: %+v", hits)
	}
	if hits[0].Matches < 1 {
		t.Fatalf("matches 应 ≥1: %+v", hits[0])
	}
	// 无关词不命中。
	if h2, _ := sessionPlaneOf(svc).SearchSessions(ctx, "不存在词xyz", 5); len(h2) != 0 {
		t.Fatalf("无关词不应命中: %+v", h2)
	}
}
