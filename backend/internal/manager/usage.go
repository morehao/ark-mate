package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/ark-forge/internal/store"
)

// ——— M3 首项：用量统计（GET /usage/summary） ———
//
// 数据面：runs 表（每轮终态行）+ conversations.agent_id + runs.usage_json（ACP 会话用量快照）。
// 语义边界（不臆造）：
//   - 计数/时长 = 区间内 run 行聚合；上下文/费用 = 每会话最近一次上报的**快照**（不累加历史）；
//   - 无底座上报（usage_json 为空）→ usage_reported=0，上下文/费用为零值，不估算；
//   - 内存模式（无 store）无 runs 读模型 → 明确 40501 usage_stats_unsupported（不返回假零值）。

// UsageSummary 聚合用量统计。
func (s *Service) UsageSummary(ctx context.Context, q handler.UsageQueryReq) (*handler.UsageSummaryResp, error) {
	if s.st == nil {
		return nil, s.restErr(acl.NewError(acl.ClassUnsupported, "usage_stats_unsupported",
			"[usage_stats_unsupported] 内存模式（未启用 store）无 runs 读模型，用量统计不可用（请以 sqlite 模式启动）", nil))
	}
	from, err := parseUsageTime(q.From)
	if err != nil {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "invalid_usage_range",
			fmt.Sprintf("[invalid_usage_range] from 需为 RFC3339 或 YYYY-MM-DD：%q", q.From), nil))
	}
	to, err := parseUsageTime(q.To)
	if err != nil {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "invalid_usage_range",
			fmt.Sprintf("[invalid_usage_range] to 需为 RFC3339 或 YYYY-MM-DD：%q", q.To), nil))
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "invalid_usage_range",
			"[invalid_usage_range] from 必须早于 to（半开区间 [from, to)）", nil))
	}
	groupBy := q.GroupBy
	if groupBy == "" {
		groupBy = "agent"
	}
	if groupBy != "agent" && groupBy != "day" {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "invalid_usage_group_by",
			fmt.Sprintf("[invalid_usage_group_by] group_by 仅支持 agent|day：%q", q.GroupBy), nil))
	}
	if q.AgentID != "" {
		if _, ok := s.AgentByID(q.AgentID); !ok {
			return nil, s.restErr(acl.NewError(acl.ClassRequest, "agent_not_found",
				fmt.Sprintf("[agent_not_found] 未知 agent %q", q.AgentID), nil))
		}
	}
	res, err := s.st.UsageSummary(ctx, store.UsageQuery{
		From: from, To: to, AgentID: q.AgentID, GroupBy: groupBy,
	})
	if err != nil {
		return nil, s.restErr(acl.NewError(acl.ClassService, "usage_query_failed",
			fmt.Sprintf("[usage_query_failed] 用量聚合失败: %v", err), err))
	}
	out := &handler.UsageSummaryResp{
		AgentID: q.AgentID,
		GroupBy: groupBy,
		Source:  "sqlite",
		Totals:  toUsageBucketResp(res.Totals),
		Buckets: make([]handler.UsageBucketResp, 0, len(res.Buckets)),
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
func toUsageBucketResp(b store.UsageBucket) handler.UsageBucketResp {
	return handler.UsageBucketResp{
		Key: b.Key, AgentID: b.AgentID, Runs: b.Runs, Completed: b.Completed, Failed: b.Failed,
		Cancelled: b.Cancelled, Running: b.Running, Conversations: b.Conversations,
		DurationMS: b.DurationMS, ContextUsed: b.ContextUsed, ContextSize: b.ContextSize,
		UsageReported: b.UsageReported, CostAmount: b.CostAmount, CostCurrency: b.CostCurrency,
	}
}

// parseUsageTime 解析统计时间参数：空 = 零值（不限）；YYYY-MM-DD = 该日 00:00 UTC；
// 其他 = RFC3339。日期形式让 `to=2026-09-09` 覆盖当天（半开区间语义自然成立）。
func parseUsageTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC(), nil
	}
	return time.Parse(time.RFC3339, v)
}
