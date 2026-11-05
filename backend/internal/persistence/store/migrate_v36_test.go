package store

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/object/objusage"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// TestRunsTokenUsageJSONColumn v36：runs 表有 token_usage_json 列，且 FinishRun 能把它写进去、
// 读回来——与 usage_json（上下文快照）分开落列的回归钉。
func TestRunsTokenUsageJSONColumn(t *testing.T) {
	s := openTestDB(t) // 全新库：Migrate 跑到最新（含 v36）
	ctx := context.Background()
	seedSession(t, s, "c1")

	runID := "run_tu1"
	if err := s.CreateRun(ctx, model.RunEntity{
		ID: runID, SessionID: "c1", UserText: "q", Status: "in_progress",
		FirstSeq: 0, LastSeq: -1, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	tu := objusage.TokenUsage{InputTokens: 12, OutputTokens: 34, TotalTokens: 46, ThoughtTokens: 5}
	tuJSON := `{"input_tokens":12,"output_tokens":34,"total_tokens":46,"thought_tokens":5}`
	if err := s.FinishRun(ctx, runID, model.RunFinishPatch{
		LastSeq: 1, Status: "completed", UsageJSON: `{"used":1,"size":100}`,
		TokenUsageJSON: tuJSON, EndedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	row, ok, err := s.RunByID(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("RunByID: ok=%v err=%v", ok, err)
	}
	if row.TokenUsageJSON != tuJSON {
		t.Fatalf("token_usage_json 未落库: %q", row.TokenUsageJSON)
	}
	// 快照列与流水列互不覆盖。
	if row.UsageJSON == "" || row.UsageJSON == row.TokenUsageJSON {
		t.Fatalf("usage_json 与 token_usage_json 应分列: usage=%q token=%q", row.UsageJSON, row.TokenUsageJSON)
	}
	_ = tu
}
