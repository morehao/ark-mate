package session

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtousage"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/code"
)

// ——— M3 首项：用量统计（GET /usage/summary） ———
//
// 数据面：runs 表（每轮终态行）+ runs.usage_json（ACP 会话用量快照）。
// 语义边界（不臆造）：
//   - 计数/时长 = 区间内 run 行聚合；上下文/费用 = 每会话最近一次上报的**快照**（不累加历史）；
//   - 无底座上报（usage_json 为空）→ usage_reported=0，上下文/费用为零值，不估算；
//   - 内存模式（无 store）无 runs 读模型 → 明确 40501 usage_stats_unsupported（不返回假零值）。
//
// 时区：日期参数与 day 桶键都按**客户端时区**解释（`?tz_offset=`，东为正的分钟数；缺省 0/UTC）。
// 界面给的是用户日历上的日期，按 UTC 解释会让每天 00:00–08:00（UTC+8）的轮次算进前一天。
//
// v19：随 agent 概念整体取消，本面没有 agent 维度——`?agent_id=` 显式拒绝、分组只有 day。

// 口径常量（随响应回显，供前端如实标注，避免把快照当区间增量使用）。
const (
	// usageContextSemantics 上下文口径：区间末各会话占用之和（快照）。
	usageContextSemantics = "end_of_window_snapshot_sum"
	// usageCostSemantics 费用口径：区间末各会话累计费用之和（快照）。
	usageCostSemantics = "session_cumulative_end_of_window"
)

// UsageSummary 聚合用量统计。
func (pl *Plane) UsageSummary(ctx context.Context, q dtousage.UsageQueryReq) (*dtousage.UsageSummaryResp, error) {
	if pl.st == nil {
		return nil, code.FromError(acl.NewError(acl.ClassUnsupported, "usage_stats_unsupported",
			"[usage_stats_unsupported] 内存模式（未启用 store）无 runs 读模型，用量统计不可用（请以 sqlite 模式启动）", nil))
	}
	tz, loc, err := parseUsageZone(q.TZOffset)
	if err != nil {
		return nil, code.FromError(err)
	}
	from, err := parseUsageTime(q.From, loc)
	if err != nil {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "invalid_usage_range",
			fmt.Sprintf("[invalid_usage_range] from 需为 RFC3339 或 YYYY-MM-DD：%q", q.From), nil))
	}
	to, err := parseUsageTime(q.To, loc)
	if err != nil {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "invalid_usage_range",
			fmt.Sprintf("[invalid_usage_range] to 需为 RFC3339 或 YYYY-MM-DD：%q", q.To), nil))
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "invalid_usage_range",
			"[invalid_usage_range] from 必须早于 to（半开区间 [from, to)）", nil))
	}
	// 已下线的查询键：非空即显式拒绝（静默忽略会让用户以为"筛选生效了"）。
	//
	// 判据本体在 `reject_removed_keys.go`（本文件属「会话身份只剩 runtime_id」那条结构判据的
	// 扫描面，零命中判据要求它连一个被撤掉的标识符都不写；显式拒绝面统一收口在那一处）。
	if err := rejectRemovedUsageDimension(q); err != nil {
		return nil, code.FromError(err)
	}
	groupBy := q.GroupBy
	if groupBy == "" {
		groupBy = "day"
	}
	if groupBy != "day" {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "invalid_usage_group_by",
			fmt.Sprintf("[invalid_usage_group_by] group_by 仅支持 day：%q", q.GroupBy), nil))
	}
	res, err := pl.st.UsageSummary(ctx, store.UsageQuery{From: from, To: to, Location: loc})
	if err != nil {
		return nil, code.FromError(acl.NewError(acl.ClassService, "usage_query_failed",
			fmt.Sprintf("[usage_query_failed] 用量聚合失败: %v", err), err))
	}
	out := &dtousage.UsageSummaryResp{
		GroupBy:          groupBy,
		TZOffset:         tz,
		Source:           "sqlite",
		ContextSemantics: usageContextSemantics,
		CostSemantics:    usageCostSemantics,
		// day 分组：跨天会话的累计快照会在多个日期桶重复出现 → 桶之间不可加（前端据此如实标注）。
		BucketsAdditive: false,
		Totals:          toUsageBucketResp(res.Totals),
		Buckets:         make([]dtousage.UsageBucketResp, 0, len(res.Buckets)),
	}
	if !from.IsZero() {
		out.From = from.UTC().Format(time.RFC3339)
	}
	if !to.IsZero() {
		out.To = to.UTC().Format(time.RFC3339)
	}
	for _, b := range res.Buckets {
		out.Buckets = append(out.Buckets, toUsageBucketResp(b))
	}
	return out, nil
}

// toUsageBucketResp 读模型 → REST DTO。
func toUsageBucketResp(b store.UsageBucket) dtousage.UsageBucketResp {
	return dtousage.UsageBucketResp{
		Key: b.Key, Runs: b.Runs, Completed: b.Completed, Failed: b.Failed,
		Cancelled: b.Cancelled, Running: b.Running, Sessions: b.Sessions,
		DurationMS: b.DurationMS, ContextUsed: b.ContextUsed, ContextSize: b.ContextSize,
		UsageReported: b.UsageReported, CostReported: b.CostReported,
		CostAmount: b.CostAmount, CostCurrency: b.CostCurrency, CostByCurrency: b.CostByCurrency,
	}
}

// parseUsageZone 解析客户端时区偏移（分钟，东为正）：空 = 0/UTC；越界（±14h 之外）或非整数
// → 40001 `invalid_usage_tz_offset`（显式拒绝，不静默按 UTC 处理——那正好是这条要修的偏差）。
func parseUsageZone(v string) (int, *time.Location, error) {
	if v == "" {
		return 0, time.UTC, nil
	}
	off, err := strconv.Atoi(v)
	if err != nil || off < -14*60 || off > 14*60 {
		return 0, nil, acl.NewError(acl.ClassRequest, "invalid_usage_tz_offset",
			fmt.Sprintf("[invalid_usage_tz_offset] tz_offset 需为 -840..840 的整数分钟（东为正）：%q", v), nil)
	}
	if off == 0 {
		return 0, time.UTC, nil
	}
	// FixedZone 的名字进不了对外载荷（响应只回数字），它只用于把瞬时投影到用户本地日历。
	return off, time.FixedZone("client", off*60), nil
}

// parseUsageTime 解析统计时间参数：空 = 零值（不限）；YYYY-MM-DD = **用户时区**该日 00:00；
// 其他 = RFC3339（自带偏移，按它自己的瞬时解析）。
//
// 日期形式必须按客户端时区解释：界面给的是用户日历上的「9 月 1 日」，不是 UTC 的 9 月 1 日。
// 按 UTC 解释的后果是整段区间偏移 8 小时（UTC+8）、日桶边界错位——数字看起来"少了一点"，
// 界面上没有任何一行会说它错在哪。
func parseUsageTime(v string, loc *time.Location) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if loc == nil {
		loc = time.UTC
	}
	if t, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}
