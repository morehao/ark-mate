package svcusage

import (
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/contract/dtousage"
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
	}
}
