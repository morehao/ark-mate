package store

import (
	"context"
	"sort"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/object/objusage"
)

// v3.1 P4：查询条件与结果行的形状上移到 `internal/model`（`domain/session` 经窄端口读库，
// 不能 import store）。这里保留别名，落库实现与调用点一字未改。
type (
	UsageQuery         = model.UsageQuery
	UsageBucket        = model.UsageBucket
	UsageSummaryResult = model.UsageSummaryResult
)

const (
	CostCurrencyDefault = model.CostCurrencyDefault
	CostCurrencyMixed   = model.CostCurrencyMixed
)

// ——— 用量统计聚合（M3 首项：GET /usage/summary 的数据面） ———
//
// 数据源：runs（每轮终态行，含 usage_json 的会话用量快照）。
// 语义：
//   - 计数/时长按「时间半开区间 [From, To)」内的 run 行聚合（started_at 落点）；
//   - 上下文用量（ContextUsed/ContextSize）是**快照**语义：取每会话在区间内**最后**一条
//     有上下文上报的 run 值（不累加历史），再对会话求和 → 反映「区间末各会话上下文占用」；
//   - 费用（CostAmount）同为会话累计快照，按会话取最后一条（不跨会话累加多轮，避免重复计费）；
//     多币种不混加：金额置零 + CostCurrency=MIXED + CostByCurrency 明细；
//   - 上报率分两个计数：UsageReported（上下文）/ CostReported（费用），二者不可互相替代；
//   - group_by=day 时日期键取 started_at 在 `Location`（客户端时区；nil = UTC）下的日历日。
//
// v19：随 agent 概念整体取消，本面不再有 `agent` 维度（不选 `agent_id`、不按它分组、
// 也不再有 `?agent_id=` 过滤）——分组只剩 day 一类。

// usageRow 聚合输入行（runs）。
type usageRow struct {
	sessionID string
	status    string
	startedAt time.Time
	endedAt   time.Time
	usage     objusage.UsageInfo
	// 字段级可用性：一次上报可能只带上下文或只带费用，聚合时按字段取「区间内最后一条」。
	hasContext bool
	hasCost    bool
}

// UsageSummary 按条件聚合用量（sqlite 模式）。
func (s *Store) UsageSummary(ctx context.Context, q UsageQuery) (*UsageSummaryResult, error) {
	rows, err := s.usageRows(ctx, q)
	if err != nil {
		return nil, err
	}
	res := &UsageSummaryResult{Totals: aggregateUsage(rows)}
	// 分桶的日历日属于**用户**，不属于服务器：UTC 的 16:00 在 UTC+8 已是次日 00:00。
	loc := q.Location
	if loc == nil {
		loc = time.UTC
	}
	byKey := make(map[string][]usageRow)
	for _, r := range rows {
		key := r.startedAt.In(loc).Format("2006-01-02")
		byKey[key] = append(byKey[key], r)
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b := aggregateUsage(byKey[k])
		b.Key = k
		res.Buckets = append(res.Buckets, b)
	}
	if res.Buckets == nil {
		res.Buckets = []UsageBucket{}
	}
	res.Totals.Key = "total"
	return res, nil
}

// usageRows 读取区间内的 run 行（含用量快照），转成本包聚合用的行结构。
//
// 半开区间 [From, To) 的边界比较由 dao 负责（必须以落库布局格式化后再比，
// 时间列是 RFC3339Nano 文本）。
func (s *Store) usageRows(ctx context.Context, q UsageQuery) ([]usageRow, error) {
	rows, err := s.runs.UsageRows(ctx, q.From, q.To)
	if err != nil {
		return nil, err
	}
	out := make([]usageRow, 0, len(rows))
	for _, r := range rows {
		row := usageRow{
			sessionID: r.SessionID,
			status:    r.Status,
			startedAt: r.StartedAt,
			endedAt:   r.EndedAt,
		}
		if r.Reported {
			row.usage = r.Usage
			// 字段级可用性：一次上报可能只带上下文或只带费用，聚合时按字段各取「区间内最后一条」。
			row.hasContext, row.hasCost = r.Usage.HasContext(), r.Usage.HasCost()
		}
		out = append(out, row)
	}
	return out, nil
}

// aggregateUsage 聚合一组 run 行：计数/时长全量累加；上下文/费用按会话取区间内**最后一次上报**
// （字段级：一次上报可能只带上下文或只带费用，互不覆盖），再对会话求和。
//
// 口径提醒：上下文是「区间末各会话占用之和」、费用是「区间末各会话累计费用之和」，两者都是
// **快照**而非区间增量——同一会话跨桶时会在每个桶各出现一次，因此 day 分桶的成本/上下文不可相加。
func aggregateUsage(rows []usageRow) UsageBucket {
	var b UsageBucket
	lastCtx := make(map[string]objusage.UsageInfo)
	lastCost := make(map[string]objusage.UsageInfo)
	ctxOrder := make([]string, 0, len(rows))
	costOrder := make([]string, 0, len(rows))
	for _, r := range rows {
		b.Runs++
		switch r.status {
		case "completed":
			b.Completed++
		case "failed":
			b.Failed++
		case "cancelled":
			b.Cancelled++
		case "in_progress":
			b.Running++
		}
		if !r.endedAt.IsZero() && r.endedAt.After(r.startedAt) {
			b.DurationMS += r.endedAt.Sub(r.startedAt).Milliseconds()
		}
		// 行按 started_at 升序 → 后到覆盖先到；上下文与费用各自独立覆盖。
		if r.hasContext {
			if _, seen := lastCtx[r.sessionID]; !seen {
				ctxOrder = append(ctxOrder, r.sessionID)
			}
			lastCtx[r.sessionID] = r.usage
		}
		if r.hasCost {
			if _, seen := lastCost[r.sessionID]; !seen {
				costOrder = append(costOrder, r.sessionID)
			}
			lastCost[r.sessionID] = r.usage
		}
	}
	b.Sessions = countDistinct(rows)
	b.UsageReported = len(ctxOrder)
	b.CostReported = len(costOrder)
	for _, cid := range ctxOrder {
		u := lastCtx[cid]
		b.ContextUsed += u.Used
		b.ContextSize += u.Size
	}
	byCurrency := make(map[string]float64, 1)
	for _, cid := range costOrder {
		u := lastCost[cid]
		cur := u.CostCurrency
		if cur == "" {
			cur = CostCurrencyDefault
		}
		byCurrency[cur] += u.CostAmount
	}
	switch len(byCurrency) {
	case 0:
		// 无费用上报：金额/币种留零值，前端展示 `—`。
	case 1:
		for cur, sum := range byCurrency {
			b.CostCurrency, b.CostAmount = cur, sum
		}
	default:
		// 多币种不可相加：金额置零、币种标 MIXED，明细走 CostByCurrency（不把不同币种加在一起）。
		b.CostCurrency, b.CostAmount = CostCurrencyMixed, 0
		b.CostByCurrency = byCurrency
	}
	return b
}

func countDistinct(rows []usageRow) int {
	seen := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		seen[r.sessionID] = struct{}{}
	}
	return len(seen)
}
