package store

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// TestLLMCallsTableAndAggregate v37：全新库迁移出 llm_calls；插入若干流水后按维度聚合
// （token 明细可跨行相加，这正与 runs.usage_json 的快照语义相反）。
func TestLLMCallsTableAndAggregate(t *testing.T) {
	s := openTestDB(t) // 全新库：Migrate 跑到最新（含 v37）
	ctx := context.Background()

	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	mk := func(purpose, modelName, cur string, in, out int64, cost float64) model.LLMCallEntity {
		return model.LLMCallEntity{
			ID: objid.New(objid.Call), At: at, Purpose: purpose, SessionID: "", RunID: "",
			Provider: "deepseek", Model: modelName,
			InputTokens: in, OutputTokens: out, TotalTokens: in + out,
			CostAmount: cost, CostCurrency: cur, LatencyMS: 12, Status: "ok",
		}
	}
	rows := []model.LLMCallEntity{
		mk("judge", "m1", "USD", 10, 5, 0.01),
		mk("judge", "m1", "USD", 20, 8, 0.02),
		mk("title", "m2", "USD", 4, 2, 0),
	}
	for _, r := range rows {
		if err := s.InsertLLMCall(ctx, r); err != nil {
			t.Fatalf("InsertLLMCall: %v", err)
		}
	}

	// 按 purpose 聚合：judge 两条相加，title 一条。
	aggs, err := s.AggregateLLMCallsBy(ctx, "purpose", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("AggregateLLMCallsBy: %v", err)
	}
	byKey := map[string]LLMCallAgg{}
	for _, a := range aggs {
		byKey[a.Key] = a
	}
	if j := byKey["judge"]; j.Count != 2 || j.InputTokens != 30 || j.OutputTokens != 13 || j.TotalTokens != 43 {
		t.Fatalf("judge 聚合不符: %+v", j)
	}
	if j := byKey["judge"]; j.CostAmount < 0.0299 || j.CostAmount > 0.0301 || j.CostCurrency != "USD" {
		t.Fatalf("judge 费用聚合不符: %+v", j)
	}
	if ti := byKey["title"]; ti.Count != 1 || ti.InputTokens != 4 {
		t.Fatalf("title 聚合不符: %+v", ti)
	}

	// 非法维度 → 明确错误（白名单防注入）。
	if _, err := s.AggregateLLMCallsBy(ctx, "session_id; DROP TABLE llm_calls", time.Time{}, time.Time{}); err == nil {
		t.Fatal("非白名单维度应返回错误")
	}
}

// TestLLMCallsAggregateMixedCurrency 多币种不混加：金额置零 + MIXED（与用量统计同口径）。
func TestLLMCallsAggregateMixedCurrency(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	for _, r := range []model.LLMCallEntity{
		{ID: objid.New(objid.Call), At: at, Purpose: "judge", Model: "m", Provider: "p",
			CostAmount: 1.0, CostCurrency: "USD", Status: "ok"},
		{ID: objid.New(objid.Call), At: at, Purpose: "judge", Model: "m", Provider: "p",
			CostAmount: 2.0, CostCurrency: "CNY", Status: "ok"},
	} {
		if err := s.InsertLLMCall(ctx, r); err != nil {
			t.Fatalf("InsertLLMCall: %v", err)
		}
	}
	aggs, err := s.AggregateLLMCallsBy(ctx, "purpose", time.Time{}, time.Time{})
	if err != nil || len(aggs) != 1 {
		t.Fatalf("聚合: aggs=%v err=%v", aggs, err)
	}
	if aggs[0].CostAmount != 0 || aggs[0].CostCurrency != model.CostCurrencyMixed {
		t.Fatalf("多币种应置零 + MIXED，得到 %+v", aggs[0])
	}
}

// TestLLMUsageAggregateAndSummary 聚合接口 + 融入 UsageSummary.LLM：旁路（无会话）流水也进 token 明细。
func TestLLMUsageAggregateAndSummary(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	mk := func(purpose string, in, out int64) model.LLMCallEntity {
		return model.LLMCallEntity{
			ID: objid.New(objid.Call), At: at, Purpose: purpose,
			Provider: "deepseek", Model: "m",
			InputTokens: in, OutputTokens: out, TotalTokens: in + out, Status: "ok",
		}
	}
	for _, r := range []model.LLMCallEntity{
		mk("judge", 10, 5),
		mk("judge", 20, 8),
		mk("title", 4, 2),
	} {
		if err := s.InsertLLMCall(ctx, r); err != nil {
			t.Fatalf("InsertLLMCall: %v", err)
		}
	}

	agg, err := s.LLMUsageAggregate(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("LLMUsageAggregate: %v", err)
	}
	if agg.Calls != 3 || agg.Tokens.InputTokens != 34 || agg.Tokens.OutputTokens != 15 || agg.Tokens.TotalTokens != 49 {
		t.Fatalf("总计不符: %+v", agg)
	}
	if len(agg.ByPurpose) != 2 || agg.ByPurpose[0].Purpose != "judge" || agg.ByPurpose[0].Calls != 2 {
		t.Fatalf("按用途分组不符（judge 应在首、2 次）: %+v", agg.ByPurpose)
	}

	// 融入 UsageSummary.LLM（会话快照那份不因 llm_calls 而变）。
	res, err := s.UsageSummary(ctx, UsageQuery{})
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if res.LLM.Calls != 3 || res.LLM.Tokens.TotalTokens != 49 {
		t.Fatalf("UsageSummary.LLM 未融入: %+v", res.LLM)
	}
}

// TestLLMUsageFailedDegradedSessionCalls 失败/降级/会话调用切分。
func TestLLMUsageFailedDegradedSessionCalls(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	mk := func(purpose, status, sessionID string) model.LLMCallEntity {
		return model.LLMCallEntity{
			ID: objid.New(objid.Call), At: at, Purpose: purpose, SessionID: sessionID,
			Provider: "deepseek", Model: "m", InputTokens: 1, TotalTokens: 1, Status: status,
		}
	}
	for _, r := range []model.LLMCallEntity{
		mk("summary", "ok", ""),          // 旁路成功
		mk("summary", "degraded", ""),    // 旁路降级
		mk("judge", "error", "sess_1"),   // 会话失败
		mk("agent_turn", "ok", "sess_1"), // 会话成功
	} {
		if err := s.InsertLLMCall(ctx, r); err != nil {
			t.Fatalf("InsertLLMCall: %v", err)
		}
	}
	agg, err := s.LLMUsageAggregate(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("LLMUsageAggregate: %v", err)
	}
	if agg.Calls != 4 || agg.Failed != 1 || agg.Degraded != 1 || agg.SessionCalls != 2 {
		t.Fatalf("失败/降级/会话调用统计不符: calls=%d failed=%d degraded=%d session=%d",
			agg.Calls, agg.Failed, agg.Degraded, agg.SessionCalls)
	}
	// 按用途：judge 应为 1 失败。
	for _, p := range agg.ByPurpose {
		if p.Purpose == "judge" && p.Failed != 1 {
			t.Fatalf("judge 失败数不符: %+v", p)
		}
		if p.Purpose == "summary" && p.Degraded != 1 {
			t.Fatalf("summary 降级数不符: %+v", p)
		}
	}
}
