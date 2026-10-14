package store

import (
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/object/objusage"
)

// seedRun 写入一条 run（连带把会话补齐：run 挂在会话上，用量快照也按会话取最后一条）。
func seedRun(t *testing.T, s *Store, runID, sessionID, status string, started, ended time.Time, usageJSON string) {
	t.Helper()
	seedCompletedSession(t, s, sessionID)
	if err := s.CreateRun(ctxT(), model.RunEntity{
		ID: runID, SessionID: sessionID, Status: status, UsageJSON: usageJSON,
		StartedAt: started, EndedAt: ended,
	}); err != nil {
		t.Fatalf("CreateRun(%s): %v", runID, err)
	}
}

func seedCompletedSession(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, ok, err := s.GetSession(ctxT(), id); err == nil && ok {
		return
	}
	sess := model.SessionEntity{ID: id, Status: "completed",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := s.UpsertSession(ctxT(), sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
}

// TestUsageSummaryAggregate 用量聚合：计数/时长全量累加；上下文/费用按会话取最后快照；
// 区间半开、UTC 日期分桶（v19 起分组只有 day 一类——agent 维度随概念一并撤销）。
func TestUsageSummaryAggregate(t *testing.T) {
	s := openTestDB(t)
	day := func(d int, h int) time.Time {
		return time.Date(2026, 9, d, h, 0, 0, 0, time.UTC)
	}
	usageA1 := `{"used":100,"size":1000}`
	usageA2 := `{"used":250,"size":1000,"cost_amount":0.5,"cost_currency":"USD"}`

	// 会话 c1 两轮（第二轮的快照覆盖第一轮）；会话 c2 一轮无用量。
	seedRun(t, s, "r1", "c1", "completed", day(1, 10), day(1, 10).Add(2*time.Second), usageA1)
	seedRun(t, s, "r2", "c1", "completed", day(2, 10), day(2, 10).Add(3*time.Second), usageA2)
	seedRun(t, s, "r3", "c2", "failed", day(2, 11), day(2, 11).Add(time.Second), "")
	// 会话 c3：一轮 cancelled。
	seedRun(t, s, "r4", "c3", "cancelled", day(3, 9), day(3, 9).Add(5*time.Second), `{"used":10,"size":100}`)

	res, err := s.UsageSummary(ctxT(), UsageQuery{})
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if res.Totals.Runs != 4 || res.Totals.Completed != 2 || res.Totals.Failed != 1 || res.Totals.Cancelled != 1 {
		t.Fatalf("总计计数不符: %+v", res.Totals)
	}
	if res.Totals.Sessions != 3 {
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
	if res.Totals.CostReported != 1 {
		t.Fatalf("费用上报会话数应单独统计（仅 c1）: %+v", res.Totals)
	}
	// 日期分桶：键是 started_at 的 UTC 日期（day1/2/3），桶里只有计数与快照，没有身份维度。
	if len(res.Buckets) != 3 || res.Buckets[0].Key != "2026-09-01" || res.Buckets[2].Key != "2026-09-03" {
		t.Fatalf("日期分桶不符: %+v", res.Buckets)
	}
	if res.Buckets[0].Runs != 1 || res.Buckets[1].Runs != 2 || res.Buckets[2].Runs != 1 {
		t.Fatalf("逐日轮次不符: %+v", res.Buckets)
	}
	// 桶内快照按会话取最后一条（r2 覆盖 r1）：桶级口径与总计一致。
	if res.Buckets[1].ContextUsed != 250 || res.Buckets[1].UsageReported != 1 {
		t.Fatalf("day 桶快照应取桶内最后一条: %+v", res.Buckets[1])
	}

	// 区间半开 [day2 00:00, day3 00:00)：只含 r2/r3。
	res, err = s.UsageSummary(ctxT(), UsageQuery{From: day(2, 0), To: day(3, 0)})
	if err != nil {
		t.Fatalf("UsageSummary(range): %v", err)
	}
	if res.Totals.Runs != 2 || res.Totals.DurationMS != 4000 {
		t.Fatalf("区间过滤不符: %+v", res.Totals)
	}
	// 半开：to 当日 00:00 不含该日 run。
	res, _ = s.UsageSummary(ctxT(), UsageQuery{To: day(2, 0)})
	if res.Totals.Runs != 1 {
		t.Fatalf("to 应为开区间: %+v", res.Totals)
	}

	// 空区间 → 零值总计 + 空桶（不报错）。
	res, err = s.UsageSummary(ctxT(), UsageQuery{From: day(10, 0), To: day(11, 0)})
	if err != nil || res.Totals.Runs != 0 || len(res.Buckets) != 0 {
		t.Fatalf("空区间应为零值: %+v err=%v", res, err)
	}
}

// TestUsageInfoReported 用量快照「无上报」判定（全零不计入上报会话）；
// 上下文与费用分开判定，供统计分别计数。
func TestUsageInfoReported(t *testing.T) {
	if (objusage.UsageInfo{}).Reported() {
		t.Fatal("全零不应视为上报")
	}
	if !(objusage.UsageInfo{Used: 1}).Reported() || !(objusage.UsageInfo{Size: 1}).Reported() ||
		!(objusage.UsageInfo{CostAmount: 0.01}).Reported() {
		t.Fatal("任一字段非零即视为上报")
	}
	// 字段级判定：只报上下文 / 只报费用各自成立，且互不冒充。
	ctxOnly := objusage.UsageInfo{Used: 250, Size: 1000}
	if !ctxOnly.HasContext() || ctxOnly.HasCost() {
		t.Fatalf("只报上下文应仅 HasContext: %+v", ctxOnly)
	}
	costOnly := objusage.UsageInfo{CostAmount: 0.5, CostCurrency: "USD"}
	if costOnly.HasContext() || !costOnly.HasCost() {
		t.Fatalf("只报费用应仅 HasCost: %+v", costOnly)
	}
	// 压缩后 Used=0 但窗口大小已知 → 仍属有效上下文上报。
	if !(objusage.UsageInfo{Size: 128000}).HasContext() {
		t.Fatal("Used=0、Size>0 应视为有效上下文上报")
	}
}

// TestUsageSummaryFieldWiseSnapshotAndCurrency 字段级快照 + 多币种不混加：
//   - 一次上报只带上下文时，不应抹掉该会话此前上报的费用（反之亦然）；
//   - 空币种归一 USD；出现多种币种时金额置零、币种 MIXED、明细走 CostByCurrency。
func TestUsageSummaryFieldWiseSnapshotAndCurrency(t *testing.T) {
	s := openTestDB(t)
	day := func(d, h int) time.Time {
		return time.Date(2026, 10, d, h, 0, 0, 0, time.UTC)
	}
	// c1：r1 带上下文 + USD 费用；r2 只带上下文（费用应保留 r1）。
	seedRun(t, s, "r1", "c1", "completed", day(1, 10), day(1, 10).Add(time.Second),
		`{"used":100,"size":1000,"cost_amount":0.5,"cost_currency":"USD"}`)
	seedRun(t, s, "r2", "c1", "completed", day(1, 11), day(1, 11).Add(time.Second),
		`{"used":250,"size":1000}`)
	// c2：只报 EUR 费用（无上下文）。
	seedRun(t, s, "r3", "c2", "completed", day(1, 12), day(1, 12).Add(time.Second),
		`{"cost_amount":0.2,"cost_currency":"EUR"}`)
	// c3：带上下文 + 未给币种的费用（空币种 → USD）。
	seedRun(t, s, "r4", "c3", "completed", day(1, 13), day(1, 13).Add(time.Second),
		`{"used":30,"size":300,"cost_amount":0.1}`)

	res, err := s.UsageSummary(ctxT(), UsageQuery{})
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	// 上下文：c1 取 r2（250/1000）、c3 取 30/300 → 280/1300，2 个会话上报。
	if res.Totals.ContextUsed != 280 || res.Totals.ContextSize != 1300 || res.Totals.UsageReported != 2 {
		t.Fatalf("上下文字段级快照不符: %+v", res.Totals)
	}
	// 费用：c1 的 USD 0.5 不被 r2 抹掉 + c3 的 0.1（归一 USD）+ c2 的 0.2 EUR → 多币种。
	if res.Totals.CostReported != 3 {
		t.Fatalf("费用上报会话数应为 3: %+v", res.Totals)
	}
	if res.Totals.CostCurrency != CostCurrencyMixed || res.Totals.CostAmount != 0 {
		t.Fatalf("多币种应置零并标 MIXED: %+v", res.Totals)
	}
	if res.Totals.CostByCurrency["USD"] != 0.6 || res.Totals.CostByCurrency["EUR"] != 0.2 {
		t.Fatalf("分币种明细不符: %+v", res.Totals.CostByCurrency)
	}

	// 单币种（含空币种归一）→ 正常求和。
	s2 := openTestDB(t)
	seedRun(t, s2, "r1", "c1", "completed", day(1, 10), day(1, 10).Add(time.Second),
		`{"used":10,"size":100,"cost_amount":0.5,"cost_currency":"USD"}`)
	seedRun(t, s2, "r2", "c2", "completed", day(1, 11), day(1, 11).Add(time.Second),
		`{"used":10,"size":100,"cost_amount":0.1}`)
	res2, err := s2.UsageSummary(ctxT(), UsageQuery{})
	if err != nil {
		t.Fatalf("UsageSummary(single currency): %v", err)
	}
	if res2.Totals.CostCurrency != CostCurrencyDefault || res2.Totals.CostAmount != 0.6 || res2.Totals.CostByCurrency != nil {
		t.Fatalf("单币种应求和且无明细: %+v", res2.Totals)
	}
}
