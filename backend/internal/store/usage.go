package store

import (
	"context"
	"sort"
	"time"

	"github.com/morehao/ark-mate/pkg/object/objusage"
)

// ——— 用量统计聚合（M3 首项：GET /usage/summary 的数据面） ———
//
// 数据源：runs（每轮终态行，含 usage_json 的会话用量快照）+ sessions.agent_id。
// 语义：
//   - 计数/时长按「时间半开区间 [From, To)」内的 run 行聚合（started_at 落点）；
//   - 上下文用量（ContextUsed/ContextSize）是**快照**语义：取每会话在区间内**最后**一条
//     有上下文上报的 run 值（不累加历史），再对会话求和 → 反映「区间末各会话上下文占用」；
//   - 费用（CostAmount）同为会话累计快照，按会话取最后一条（不跨会话累加多轮，避免重复计费）；
//     多币种不混加：金额置零 + CostCurrency=MIXED + CostByCurrency 明细；
//   - 上报率分两个计数：UsageReported（上下文）/ CostReported（费用），二者不可互相替代；
//   - group_by=day 时日期键取 started_at 的 UTC 日期（确定性，不受服务器时区影响）。

// UsageQuery 用量聚合查询条件。
type UsageQuery struct {
	From    time.Time // 零值 = 不限起点
	To      time.Time // 零值 = 不限终点（半开区间）
	AgentID string    // 空 = 全部 agent
	GroupBy string    // "agent"（默认）| "day"
}

// 费用币种约定：
//   - 底座未给币种（空串）按 USD 归一（与前端 `$` 展示口径一致，避免同一美元被拆成两个币种）；
//   - 一个桶内出现多种币种时不可相加：金额置零、币种标 CostCurrencyMixed，明细走 CostByCurrency。
const (
	// CostCurrencyDefault 底座未上报币种时的归一币种。
	CostCurrencyDefault = "USD"
	// CostCurrencyMixed 桶内多币种哨兵（此时 CostAmount 为 0，明细见 CostByCurrency）。
	CostCurrencyMixed = "MIXED"
)

// UsageBucket 一行聚合结果（group_by=agent → Key=agent_id；=day → Key=YYYY-MM-DD，AgentID 空）。
type UsageBucket struct {
	Key           string `json:"key"`
	AgentID       string `json:"agent_id,omitempty"`
	Runs          int    `json:"runs"`
	Completed     int    `json:"completed"`
	Failed        int    `json:"failed"`
	Cancelled     int    `json:"cancelled"`
	Running       int    `json:"running"`
	Sessions      int    `json:"sessions"`
	DurationMS    int64  `json:"duration_ms"`
	ContextUsed   int64  `json:"context_used"`
	ContextSize   int64  `json:"context_size"`
	UsageReported int    `json:"usage_reported"`
	// CostReported 上报过费用的会话数（与 UsageReported 分开：二者上报率不同）。
	CostReported int     `json:"cost_reported"`
	CostAmount   float64 `json:"cost_amount"`
	CostCurrency string  `json:"cost_currency,omitempty"`
	// CostByCurrency 多币种（CostCurrency=CostCurrencyMixed）时的分币种金额；单币种为空。
	CostByCurrency map[string]float64 `json:"cost_by_currency,omitempty"`
}

// UsageSummaryResult 聚合结果：全区间总计 + 分组桶（agent 键升序 / day 键升序）。
type UsageSummaryResult struct {
	Totals  UsageBucket   `json:"totals"`
	Buckets []UsageBucket `json:"buckets"`
}

// usageRow 聚合输入行（runs ⨝ sessions）。
type usageRow struct {
	sessionID string
	agentID   string
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
	groupBy := q.GroupBy
	if groupBy == "" {
		groupBy = "agent"
	}
	byKey := make(map[string][]usageRow)
	for _, r := range rows {
		var key string
		if groupBy == "day" {
			key = r.startedAt.UTC().Format("2006-01-02")
		} else {
			key = r.agentID
		}
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
		if groupBy != "day" {
			b.AgentID = k
		}
		res.Buckets = append(res.Buckets, b)
	}
	if res.Buckets == nil {
		res.Buckets = []UsageBucket{}
	}
	res.Totals.Key = "total"
	return res, nil
}

// usageRows 读取区间内的 run 行（含 agent 归属与用量快照），转成本包聚合用的行结构。
//
// 半开区间 [From, To) 的边界比较由 dao 负责（必须以落库布局格式化后再比，
// 时间列是 RFC3339Nano 文本）。
func (s *Store) usageRows(ctx context.Context, q UsageQuery) ([]usageRow, error) {
	rows, err := s.runs.UsageRows(ctx, q.From, q.To, q.AgentID)
	if err != nil {
		return nil, err
	}
	out := make([]usageRow, 0, len(rows))
	for _, r := range rows {
		row := usageRow{
			sessionID: r.SessionID,
			agentID:   r.AgentID,
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
		case "running":
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
