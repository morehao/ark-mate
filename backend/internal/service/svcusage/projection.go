package svcusage

import (
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtousage"
)

// ——— 会话域用量视图 → 对外 DTO（v3.2 刀 9）———
func usageQuery(q dtousage.UsageQueryReq) session.UsageQuery {
	return session.UsageQuery{
		From: q.From, To: q.To, TZOffset: q.TZOffset, GroupBy: q.GroupBy,
	}
}

func usageBucket(b session.UsageBucket) dtousage.UsageBucketResp {
	return dtousage.UsageBucketResp{
		Key: b.Key, Runs: b.Runs, Completed: b.Completed, Failed: b.Failed,
		Cancelled: b.Cancelled, Running: b.Running, Sessions: b.Sessions,
		DurationMS: b.DurationMS, ContextUsed: b.ContextUsed, ContextSize: b.ContextSize,
		UsageReported: b.UsageReported, CostReported: b.CostReported,
		CostAmount: b.CostAmount, CostCurrency: b.CostCurrency, CostByCurrency: b.CostByCurrency,
	}
}

func usageSummary(v *session.UsageSummary) *dtousage.UsageSummaryResp {
	if v == nil {
		return nil
	}
	buckets := make([]dtousage.UsageBucketResp, 0, len(v.Buckets))
	for _, b := range v.Buckets {
		buckets = append(buckets, usageBucket(b))
	}
	return &dtousage.UsageSummaryResp{
		From: v.From, To: v.To, GroupBy: v.GroupBy, TZOffset: v.TZOffset, Source: v.Source,
		ContextSemantics: v.ContextSemantics, CostSemantics: v.CostSemantics,
		BucketsAdditive: v.BucketsAdditive, Totals: usageBucket(v.Totals), Buckets: buckets,
		LLM: usageLLM(v.LLM),
	}
}

// usageLLM 内部模型调用流水汇总 → DTO。
func usageLLM(v session.UsageLLM) dtousage.UsageLLMResp {
	tokens := func(t session.UsageTokens) dtousage.UsageTokensResp {
		return dtousage.UsageTokensResp{
			InputTokens: t.InputTokens, OutputTokens: t.OutputTokens,
			CachedReadTokens: t.CachedReadTokens, CachedWriteTokens: t.CachedWriteTokens,
			ThoughtTokens: t.ThoughtTokens, TotalTokens: t.TotalTokens,
		}
	}
	by := make([]dtousage.UsagePurposeResp, 0, len(v.ByPurpose))
	for _, p := range v.ByPurpose {
		by = append(by, dtousage.UsagePurposeResp{
			Purpose: p.Purpose, Calls: p.Calls, Failed: p.Failed, Degraded: p.Degraded, Tokens: tokens(p.Tokens),
			CostAmount: p.CostAmount, CostCurrency: p.CostCurrency,
		})
	}
	return dtousage.UsageLLMResp{Calls: v.Calls, Failed: v.Failed, Degraded: v.Degraded, SessionCalls: v.SessionCalls, Tokens: tokens(v.Tokens), ByPurpose: by}
}
