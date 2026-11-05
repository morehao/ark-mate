package store

import (
	"context"
	"sort"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/model"
)

// ——— 内部模型调用流水（v37：llm_calls）———
//
// 一次调用一行；会话/旁路共用，`session_id` 可空。落库按「调用即流水」，聚合按「可跨行相加」——
// 这与 `runs.usage_json` 的上下文快照**语义相反**（后者同会话后到覆盖先到、不可相加），
// 见 internal/core/object/objusage 包注释。本文件只提供**写入**与**基础聚合**；`/usage/summary`
// 的正式改造（加入 llm_calls 维度）在后续步骤。

// InsertLLMCall 追加一条调用流水。流水只增不改，故直接插入。
func (s *Store) InsertLLMCall(ctx context.Context, e model.LLMCallEntity) error {
	return s.llmCalls.DB(ctx).Create(&e).Error
}

// LLMCallAgg 是一条按维度（purpose / model / provider）汇总的流水聚合行。
type LLMCallAgg struct {
	Key               string // 维度取值（purpose / model / provider）
	Count             int64
	InputTokens       int64
	OutputTokens      int64
	CachedReadTokens  int64
	CachedWriteTokens int64
	ThoughtTokens     int64
	TotalTokens       int64
	CostAmount        float64
	CostCurrency      string // 多币种混加时置 CostCurrencyMixed（与用量统计同口径）
}

// LLMCallAggRow 是 GROUP BY 的 Scan 目标（金额按币种单独聚合后再归并，见 summarizeLLMByCurrency）。
type LLMCallAggRow struct {
	Key               string
	Count             int64
	InputTokens       int64
	OutputTokens      int64
	CachedReadTokens  int64
	CachedWriteTokens int64
	ThoughtTokens     int64
	TotalTokens       int64
	CostAmount        float64
	CostCurrency      string
}

// AggregateLLMCallsBy 按指定列（purpose / model / provider 之一）分组聚合区间 [from, to) 内的流水。
//
// 只接受白名单列（防御 SQL 拼接注入）：dim 必须是 "purpose" / "model" / "provider"。
// `from`/`to` 为零值表示不限该侧；时间比较按落库布局格式化（at 是 RFC3339 文本）。
func (s *Store) AggregateLLMCallsBy(ctx context.Context, dim string, from, to time.Time) ([]LLMCallAgg, error) {
	col, ok := llmCallAggColumns[dim]
	if !ok {
		return nil, errBadLLMCallDim
	}
	q := s.llmCalls.DB(ctx).Table(model.TableNameLLMCall).
		Select(col + " AS key, COUNT(*) AS count, " +
			"SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens, " +
			"SUM(cached_read_tokens) AS cached_read_tokens, SUM(cached_write_tokens) AS cached_write_tokens, " +
			"SUM(thought_tokens) AS thought_tokens, SUM(total_tokens) AS total_tokens, " +
			"SUM(cost_amount) AS cost_amount, cost_currency AS cost_currency").
		Group(col + ", cost_currency")
	if !from.IsZero() {
		q = q.Where("at >= ?", model.FormatTime(from))
	}
	if !to.IsZero() {
		q = q.Where("at < ?", model.FormatTime(to))
	}
	var raw []LLMCallAggRow
	if err := q.Scan(&raw).Error; err != nil {
		return nil, err
	}
	return mergeLLMCallRows(raw), nil
}

// llmCallAggColumns 白名单：维度键 → 真实列名（防 SQL 注入）。
var llmCallAggColumns = map[string]string{
	"purpose":  "purpose",
	"model":    "model",
	"provider": "provider",
}

var errBadLLMCallDim = errLLMCallDim("llm_calls: 不支持的聚合维度（仅 purpose/model/provider）")

type errLLMCallDim string

func (e errLLMCallDim) Error() string { return string(e) }

// mergeLLMCallRows 把「同一 key、不同币种」的多行并成一行：金额分币种累加，多币种 -> 置零 + MIXED
// （与用量统计同一口径：多币种不混加，避免把不同币种的金额相加成没有意义的数）。
func mergeLLMCallRows(raw []LLMCallAggRow) []LLMCallAgg {
	type acc struct {
		agg  LLMCallAgg
		cost map[string]float64
	}
	order := make([]string, 0, len(raw))
	byKey := make(map[string]*acc, len(raw))
	for _, r := range raw {
		a, ok := byKey[r.Key]
		if !ok {
			a = &acc{agg: LLMCallAgg{Key: r.Key}, cost: map[string]float64{}}
			byKey[r.Key] = a
			order = append(order, r.Key)
		}
		a.agg.Count += r.Count
		a.agg.InputTokens += r.InputTokens
		a.agg.OutputTokens += r.OutputTokens
		a.agg.CachedReadTokens += r.CachedReadTokens
		a.agg.CachedWriteTokens += r.CachedWriteTokens
		a.agg.ThoughtTokens += r.ThoughtTokens
		a.agg.TotalTokens += r.TotalTokens
		if r.CostAmount != 0 {
			a.cost[r.CostCurrency] += r.CostAmount
		}
	}
	out := make([]LLMCallAgg, 0, len(order))
	for _, k := range order {
		a := byKey[k]
		a.agg.CostAmount, a.agg.CostCurrency = foldCurrency(a.cost)
		out = append(out, a.agg)
	}
	return out
}

// foldCurrency 把分币种金额折成一个 (amount, currency)：单币种直接返回；多币种 -> (0, MIXED)。
func foldCurrency(cost map[string]float64) (float64, string) {
	if len(cost) == 0 {
		return 0, ""
	}
	if len(cost) == 1 {
		for cur, amt := range cost {
			return amt, cur
		}
	}
	return 0, model.CostCurrencyMixed
}

// LLMUsageAggregate 聚合区间 [from, to) 内 llm_calls 的 token 明细：总计 + 按用途分组。
//
// 数据源是**流水**（一次调用一行、可跨行相加），与会话快照口径（runs.usage_json）分开。
// 覆盖含"无会话"的旁路调用（session_id 为空）——这正是本聚合要暴露的那部分。
func (s *Store) LLMUsageAggregate(ctx context.Context, from, to time.Time) (model.LLMUsageSummary, error) {
	// 总量（不分组）：用按 purpose 的行再横向汇总，避免多一次全表扫描。
	byPurpose, err := s.AggregateLLMCallsBy(ctx, "purpose", from, to)
	if err != nil {
		return model.LLMUsageSummary{}, err
	}
	out := model.LLMUsageSummary{ByPurpose: make([]model.LLMPurposeUsage, 0, len(byPurpose))}
	for _, a := range byPurpose {
		out.Calls += a.Count
		out.Tokens.InputTokens += a.InputTokens
		out.Tokens.OutputTokens += a.OutputTokens
		out.Tokens.CachedReadTokens += a.CachedReadTokens
		out.Tokens.CachedWriteTokens += a.CachedWriteTokens
		out.Tokens.ThoughtTokens += a.ThoughtTokens
		out.Tokens.TotalTokens += a.TotalTokens
		out.ByPurpose = append(out.ByPurpose, model.LLMPurposeUsage{
			Purpose:      a.Key,
			Calls:        a.Count,
			Tokens:       toLLMTokenUsage(a),
			CostAmount:   a.CostAmount,
			CostCurrency: a.CostCurrency,
		})
	}
	// purpose 维度按调用数降序（稳定可读）；调用数相同按用途名。
	sort.Slice(out.ByPurpose, func(i, j int) bool {
		if out.ByPurpose[i].Calls != out.ByPurpose[j].Calls {
			return out.ByPurpose[i].Calls > out.ByPurpose[j].Calls
		}
		return out.ByPurpose[i].Purpose < out.ByPurpose[j].Purpose
	})
	return out, nil
}

func toLLMTokenUsage(a LLMCallAgg) model.LLMTokenUsage {
	return model.LLMTokenUsage{
		InputTokens: a.InputTokens, OutputTokens: a.OutputTokens,
		CachedReadTokens: a.CachedReadTokens, CachedWriteTokens: a.CachedWriteTokens,
		ThoughtTokens: a.ThoughtTokens, TotalTokens: a.TotalTokens,
	}
}
