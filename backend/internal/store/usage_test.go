package store

import (
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
)

// seedRun 写入一条 run（含 agent 归属的会话与用量快照 JSON）。
func seedRun(t *testing.T, s *Store, runID, convID, agentID, status string, started, ended time.Time, usageJSON string) {
	t.Helper()
	seedConversationAgent(t, s, convID, agentID)
	if err := s.CreateRun(ctxT(), Run{
		ID: runID, ConversationID: convID, Status: status, UsageJSON: usageJSON,
		StartedAt: started, EndedAt: ended,
	}); err != nil {
		t.Fatalf("CreateRun(%s): %v", runID, err)
	}
}

func seedConversationAgent(t *testing.T, s *Store, id, agentID string) {
	t.Helper()
	if _, ok, err := s.GetConversation(ctxT(), id); err == nil && ok {
		return
	}
	sess := Conversation{ID: id, AgentID: agentID, Status: "completed",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := s.UpsertConversation(ctxT(), sess); err != nil {
		t.Fatalf("UpsertConversation: %v", err)
	}
}

// TestUsageSummaryAggregate 用量聚合：计数/时长全量累加；上下文/费用按会话取最后快照；
// 区间半开、agent 过滤、group_by=day 的 UTC 日期分桶。
func TestUsageSummaryAggregate(t *testing.T) {
	s := openMem(t)
	day := func(d int, h int) time.Time {
		return time.Date(2026, 9, d, h, 0, 0, 0, time.UTC)
	}
	usageA1 := `{"used":100,"size":1000}`
	usageA2 := `{"used":250,"size":1000,"cost_amount":0.5,"cost_currency":"USD"}`

	// agent-a：会话 c1 两轮（第二轮的快照覆盖第一轮）；会话 c2 一轮无用量。
	seedRun(t, s, "r1", "c1", "agent-a", "completed", day(1, 10), day(1, 10).Add(2*time.Second), usageA1)
	seedRun(t, s, "r2", "c1", "agent-a", "completed", day(2, 10), day(2, 10).Add(3*time.Second), usageA2)
	seedRun(t, s, "r3", "c2", "agent-a", "failed", day(2, 11), day(2, 11).Add(time.Second), "")
	// agent-b：一轮 cancelled。
	seedRun(t, s, "r4", "c3", "agent-b", "cancelled", day(3, 9), day(3, 9).Add(5*time.Second), `{"used":10,"size":100}`)

	res, err := s.UsageSummary(ctxT(), UsageQuery{GroupBy: "agent"})
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if res.Totals.Runs != 4 || res.Totals.Completed != 2 || res.Totals.Failed != 1 || res.Totals.Cancelled != 1 {
		t.Fatalf("总计计数不符: %+v", res.Totals)
	}
	if res.Totals.Conversations != 3 {
		t.Fatalf("会话数应为 3: %+v", res.Totals)
	}
	if res.Totals.DurationMS != 11000 {
		t.Fatalf("总时长应为 11000ms: %d", res.Totals.DurationMS)
	}
	// 快照语义：c1 取 r2（250/1000 + 0.5 USD），c3 取 10/100 → used=260 size=1100。
	if res.Totals.ContextUsed != 260 || res.Totals.ContextSize != 1100 || res.Totals.UsageReported != 2 {
		t.Fatalf("上下文快照聚合不符: %+v", res.Totals)
	}
	if res.Totals.CostAmount != 0.5 || res.Totals.CostCurrency != "USD" {
		t.Fatalf("费用快照不符: %+v", res.Totals)
	}
	if len(res.Buckets) != 2 || res.Buckets[0].Key != "agent-a" || res.Buckets[1].Key != "agent-b" {
		t.Fatalf("agent 分桶不符: %+v", res.Buckets)
	}
	if res.Buckets[0].Runs != 3 || res.Buckets[0].AgentID != "agent-a" || res.Buckets[0].ContextUsed != 250 {
		t.Fatalf("agent-a 桶不符: %+v", res.Buckets[0])
	}
	if res.Buckets[0].UsageReported != 1 {
		t.Fatalf("agent-a 应只有 1 个会话有上报: %+v", res.Buckets[0])
	}

	// agent 过滤。
	res, err = s.UsageSummary(ctxT(), UsageQuery{AgentID: "agent-b", GroupBy: "agent"})
	if err != nil {
		t.Fatalf("UsageSummary(agent-b): %v", err)
	}
	if res.Totals.Runs != 1 || len(res.Buckets) != 1 || res.Buckets[0].Key != "agent-b" {
		t.Fatalf("agent 过滤不符: %+v", res)
	}

	// 区间半开 [day2 00:00, day3 00:00)：只含 r2/r3。
	res, err = s.UsageSummary(ctxT(), UsageQuery{From: day(2, 0), To: day(3, 0), GroupBy: "agent"})
	if err != nil {
		t.Fatalf("UsageSummary(range): %v", err)
	}
	if res.Totals.Runs != 2 || res.Totals.DurationMS != 4000 {
		t.Fatalf("区间过滤不符: %+v", res.Totals)
	}
	// 半开：to 当日 00:00 不含该日 run。
	res, _ = s.UsageSummary(ctxT(), UsageQuery{To: day(2, 0), GroupBy: "agent"})
	if res.Totals.Runs != 1 {
		t.Fatalf("to 应为开区间: %+v", res.Totals)
	}

	// group_by=day：UTC 日期分桶（day1/2/3）。
	res, err = s.UsageSummary(ctxT(), UsageQuery{GroupBy: "day"})
	if err != nil {
		t.Fatalf("UsageSummary(day): %v", err)
	}
	if len(res.Buckets) != 3 || res.Buckets[0].Key != "2026-09-01" || res.Buckets[2].Key != "2026-09-03" {
		t.Fatalf("日期分桶不符: %+v", res.Buckets)
	}
	if res.Buckets[1].Runs != 2 || res.Buckets[1].AgentID != "" {
		t.Fatalf("day 桶不应带 agent_id: %+v", res.Buckets[1])
	}
	// day 桶内快照按会话取最后一条（r2 覆盖 r1）。
	if res.Buckets[1].ContextUsed != 250 {
		t.Fatalf("day 桶快照应取桶内最后一条: %+v", res.Buckets[1])
	}

	// 空区间 → 零值总计 + 空桶（不报错）。
	res, err = s.UsageSummary(ctxT(), UsageQuery{From: day(10, 0), To: day(11, 0), GroupBy: "agent"})
	if err != nil || res.Totals.Runs != 0 || len(res.Buckets) != 0 {
		t.Fatalf("空区间应为零值: %+v err=%v", res, err)
	}
}

// TestUsageInfoReported 用量快照「无上报」判定（全零不计入上报会话）。
func TestUsageInfoReported(t *testing.T) {
	if (acl.UsageInfo{}).Reported() {
		t.Fatal("全零不应视为上报")
	}
	if !(acl.UsageInfo{Used: 1}).Reported() || !(acl.UsageInfo{Size: 1}).Reported() ||
		!(acl.UsageInfo{CostAmount: 0.01}).Reported() {
		t.Fatal("任一字段非零即视为上报")
	}
}
