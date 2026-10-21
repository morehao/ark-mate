package run

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/platform/object/objusage"
	"github.com/morehao/ark-mate/internal/adapters/outbound/acp"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/golib/gerror"
)

// TestUsageSummaryService 用量统计服务面（M3 首项）：
// store 启用时聚合 runs（计数/时长）+ 会话用量快照；参数校验与降级路径明确。
func TestUsageSummaryService(t *testing.T) {
	ctx := context.Background()
	reg := testutil.StubRegistry()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer st.Close()
	svc := newServiceForTest(reg, eventbus.New())
	enableStore(svc, st)
	orch := newOrchestratorForTest(svc, reg)
	t.Cleanup(orch.Close)

	// 两个会话：a1（echo，两轮，用量快照 250/1000）与 a2（echo，一轮无上报）。
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "u1"})
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "u2"})
	orch.runner.RecordUsage("u1", objusage.UsageInfo{Used: 100, Size: 1000})

	// 每轮都走开轮三连（v22：轮次是实体，没有开轮行就没有 runs 行可聚合）。
	runA1, _, _ := beginRun(t, svc, "u1", "第一问")
	svc.sessionPlane.Publish(session.BusMessage, "u1", map[string]any{"text": "答一"})
	endRun(svc, "u1", runA1, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})

	orch.runner.RecordUsage("u1", objusage.UsageInfo{Used: 250, Size: 1000, CostAmount: 0.5, CostCurrency: "USD"})
	runA2, _, _ := beginRun(t, svc, "u1", "第二问")
	endRun(svc, "u1", runA2, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})

	runB1, _, _ := beginRun(t, svc, "u2", "第三问")
	endRun(svc, "u2", runB1, session.BusRunError, map[string]any{"code": "agent_error", "class": "agent"})

	resp, err := sessionPlaneOf(svc).UsageSummary(ctx, session.UsageQuery{})
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if resp.Source != "sqlite" || resp.GroupBy != "day" {
		t.Fatalf("响应元信息不符（默认分组是 day）: %+v", resp)
	}
	if resp.Totals.Runs != 3 || resp.Totals.Completed != 2 || resp.Totals.Failed != 1 {
		t.Fatalf("总计计数不符: %+v", resp.Totals)
	}
	if resp.Totals.Sessions != 2 || resp.Totals.UsageReported != 1 {
		t.Fatalf("会话/上报数不符: %+v", resp.Totals)
	}
	if resp.Totals.CostReported != 1 {
		t.Fatalf("费用上报数应单独统计: %+v", resp.Totals)
	}
	// 口径回显：上下文/费用均为区间末快照；`group_by=day` 的桶**不可加**。
	if resp.ContextSemantics != "end_of_window_snapshot_sum" || resp.CostSemantics != "session_cumulative_end_of_window" {
		t.Fatalf("口径字段不符: %+v", resp)
	}
	if resp.BucketsAdditive {
		t.Fatalf("day 分组桶不可相加（跨天会话的快照会在多个日期桶重复出现）: %+v", resp)
	}
	// 快照语义：u1 取最后一轮 250/1000，u2 无上报 → 总计 250/1000。
	if resp.Totals.ContextUsed != 250 || resp.Totals.ContextSize != 1000 || resp.Totals.CostAmount != 0.5 {
		t.Fatalf("用量快照不符: %+v", resp.Totals)
	}
	if len(resp.Buckets) != 1 || len(resp.Buckets[0].Key) != 10 {
		t.Fatalf("默认应按 UTC 日期分桶: %+v", resp.Buckets)
	}

	// group_by=day：UTC 日期桶（今日 1 个桶）；快照口径下日桶不可相加（前端据此标注）。
	dayResp, err := sessionPlaneOf(svc).UsageSummary(ctx, session.UsageQuery{GroupBy: "day"})
	if err != nil || len(dayResp.Buckets) != 1 || len(dayResp.Buckets[0].Key) != 10 {
		t.Fatalf("day 分桶不符: %+v err=%v", dayResp, err)
	}
	if dayResp.BucketsAdditive {
		t.Fatalf("day 分组桶不可相加: %+v", dayResp)
	}

	// 日期形式时间参数：from/to 回显为 UTC RFC3339（date → 当日 00:00）。
	r2, err := sessionPlaneOf(svc).UsageSummary(ctx, session.UsageQuery{From: "2000-01-01", To: "2000-01-02"})
	if err != nil {
		t.Fatalf("日期参数应合法: %v", err)
	}
	if r2.From != "2000-01-01T00:00:00Z" || r2.To != "2000-01-02T00:00:00Z" || r2.Totals.Runs != 0 {
		t.Fatalf("日期参数解析/区间不符: %+v", r2)
	}

	// 参数校验：非法时间 / from ≥ to / 非法 group_by（REST 面 code + msg 内稳定码）。
	// 已下线的查询键那一条单列在 `usage_removed_dimension_test.go`：它必须写出历史键的名字，
	// 而本文件属「会话身份只剩 runtime_id」结构判据的近邻面，保持零命中更干净。
	cases := []struct {
		name  string
		q     session.UsageQuery
		code  int
		token string
	}{
		{"bad_from", session.UsageQuery{From: "2026/09/09"}, 40001, "[invalid_usage_range]"},
		{"bad_to", session.UsageQuery{To: "yesterday"}, 40001, "[invalid_usage_range]"},
		{"from_after_to", session.UsageQuery{From: "2026-09-10T00:00:00Z", To: "2026-09-09T00:00:00Z"}, 40001, "[invalid_usage_range]"},
		{"bad_group", session.UsageQuery{GroupBy: "tool"}, 40001, "[invalid_usage_group_by]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sessionPlaneOf(svc).UsageSummary(ctx, tc.q)
			var ge gerror.Error
			if !errors.As(err, &ge) || ge.Code != tc.code || !strings.Contains(ge.Msg, tc.token) {
				t.Fatalf("期望 code=%d 含 %s，got %v", tc.code, tc.token, err)
			}
		})
	}

	// 空区间（没有落在窗口里的轮次）→ 零值，不报错。
	resp, err = sessionPlaneOf(svc).UsageSummary(ctx, session.UsageQuery{From: "2000-01-01", To: "2000-01-02"})
	if err != nil || resp.Totals.Runs != 0 || len(resp.Buckets) != 0 {
		t.Fatalf("空区间应为零值: %+v err=%v", resp, err)
	}

	// 内存模式（无 store）→ 明确 unsupported（不返回假零值）。
	svcMem := newServiceForTest(reg, eventbus.New())
	_, err = sessionPlaneOf(svcMem).UsageSummary(ctx, session.UsageQuery{})
	var ge gerror.Error
	if !errors.As(err, &ge) || ge.Code != 40501 || !strings.Contains(ge.Msg, "[usage_stats_unsupported]") {
		t.Fatalf("无 store 应 40501/usage_stats_unsupported，got %v", err)
	}
}

// TestOrchestratorUsageEvent 用量事件载荷与最近快照记录（底座上报 → 域事件 → 落库取值）。
func TestOrchestratorUsageEvent(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "c1"})
	orch.handles.BindProvider("native-1", "c1")

	// live-only：订阅之后才触发 sink，断言的是"这一条新事件"。
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)
	sink := orch.acpEventSink("stub")
	sink(acp.Event{Kind: acp.KindUsage, SessionID: "native-1",
		Payload: objusage.UsageInfo{Used: 42, Size: 200, CostAmount: 0.25, CostCurrency: "USD"}})

	select {
	case e := <-sub.C:
		if e.Type != session.BusUsage || e.AggregID != "c1" {
			t.Fatalf("期望 %s/c1，got %s/%s", session.BusUsage, e.Type, e.AggregID)
		}
		p, _ := e.Payload.(map[string]any)
		if p["session_id"] != "c1" || p["runtime"] != "stub" ||
			p["used"] != int64(42) || p["size"] != int64(200) ||
			p["cost_amount"] != 0.25 || p["cost_currency"] != "USD" {
			t.Fatalf("用量载荷不符: %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到用量事件")
	}
	if u, ok := orch.LatestUsage("c1"); !ok || u.Used != 42 || u.Size != 200 {
		t.Fatalf("最近快照未记录: %+v ok=%v", u, ok)
	}

	// 后到覆盖先到；全零快照忽略（不污染统计）。
	sink(acp.Event{Kind: acp.KindUsage, SessionID: "native-1", Payload: objusage.UsageInfo{Used: 80, Size: 200}})
	if u, _ := orch.LatestUsage("c1"); u.Used != 80 {
		t.Fatalf("后到应覆盖: %+v", u)
	}
	sink(acp.Event{Kind: acp.KindUsage, SessionID: "native-1", Payload: objusage.UsageInfo{}})
	if u, _ := orch.LatestUsage("c1"); u.Used != 80 {
		t.Fatalf("全零快照不应覆盖: %+v", u)
	}

	// 无 payload（旧底座/无上报）→ 事件仍发布，但不记录。
	sink(acp.Event{Kind: acp.KindUsage, SessionID: "native-1"})
	if _, ok := orch.LatestUsage("c1"); !ok {
		t.Fatal("已有快照不应被清除")
	}
	if u, _ := orch.LatestUsage("c-unknown"); u.Reported() {
		t.Fatalf("未知会话不应有快照: %+v", u)
	}
}
