package store

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/object/objusage"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// seedRun 写入一条 run（连带把会话补齐：run 挂在会话上，用量快照也按会话取最后一条）。
// seedRunSeq 给种子行发**互不相同**的窗口起点。
//
// v22 起 `(session_id, first_seq)` 是唯一键，而老夹具一个都不设（全 0）——第二行起会被
// `ON CONFLICT DO NOTHING` **静默跳过**：看起来"种了 4 行"，库里其实只有 1 行，
// 而失败信息只会说"计数不符"。这正是这条唯一键要挡的那种静默，夹具必须自己守规矩。
var seedRunSeq atomic.Int64

func seedRun(t *testing.T, s *Store, runID, sessionID, status string, started, ended time.Time, usageJSON string) {
	t.Helper()
	seedCompletedSession(t, s, sessionID)
	if err := s.CreateRun(ctxT(), model.RunEntity{
		ID: runID, SessionID: sessionID, Status: status, UsageJSON: usageJSON,
		FirstSeq: seedRunSeq.Add(1), LastSeq: seedRunSeq.Add(1),
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

// TestUsageBucketsFollowClientZone day 桶的日历日属于**客户端**：UTC 的 16:30 在 UTC+8
// 已是次日 00:30。按 UTC 分桶（旧行为）会把 UTC+8 用户每天 00:00–08:00 的轮次算进前一天，
// 而界面上看不出来——数字只是"对不上"。
func TestUsageBucketsFollowClientZone(t *testing.T) {
	s := openTestDB(t)
	// 2026-09-01 16:30 UTC = 2026-09-02 00:30 (UTC+8)。
	at := time.Date(2026, 9, 1, 16, 30, 0, 0, time.UTC)
	seedRun(t, s, "r1", "c1", "completed", at, at.Add(time.Second), `{"used":1,"size":10}`)

	utc, err := s.UsageSummary(ctxT(), UsageQuery{})
	if err != nil {
		t.Fatalf("UsageSummary(utc): %v", err)
	}
	if len(utc.Buckets) != 1 || utc.Buckets[0].Key != "2026-09-01" {
		t.Fatalf("缺省（UTC）分桶应为 2026-09-01: %+v", utc.Buckets)
	}

	east8, err := s.UsageSummary(ctxT(), UsageQuery{Location: time.FixedZone("client", 8*3600)})
	if err != nil {
		t.Fatalf("UsageSummary(+08:00): %v", err)
	}
	if len(east8.Buckets) != 1 || east8.Buckets[0].Key != "2026-09-02" {
		t.Fatalf("UTC+8 下同一轮应落在 2026-09-02: %+v", east8.Buckets)
	}
	// 分桶只影响 key，不影响聚合值。
	if east8.Buckets[0].Runs != 1 || east8.Totals.Runs != 1 {
		t.Fatalf("时区不得改变聚合值: %+v", east8)
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

// TestUsageCountsInProgressAsRunning 修掉「Running 恒 0」之后，进行中的轮必须计入
// `Running`。写入方（`OpenRun`）写 `in_progress`，v30 又把历史 `running` 归一到了同一个值，
// 所以这里只认 `in_progress`——再写一个 `running` 分支就是同一件事的第二份事实。
func TestUsageCountsInProgressAsRunning(t *testing.T) {
	s := openTestDB(t)
	day := func(h int) time.Time { return time.Date(2026, 9, 1, h, 0, 0, 0, time.UTC) }
	seedRun(t, s, "r_open", "c_open", "in_progress", day(10), time.Time{}, "")
	seedRun(t, s, "r_done", "c_done", "completed", day(11), day(11).Add(time.Second), "")

	res, err := s.UsageSummary(ctxT(), UsageQuery{})
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if res.Totals.Runs != 2 || res.Totals.Running != 1 || res.Totals.Completed != 1 {
		t.Fatalf("计数不符: %+v（want runs=2 running=1 completed=1）", res.Totals)
	}
}
