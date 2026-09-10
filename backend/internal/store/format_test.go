package store

import (
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/model"
)

// TestOnDiskFormatContract 锁定落库的**字节形态**。
//
// 这一层从 database/sql 换到 gorm 后，最容易的静默回归就是列表示被驱动的默认约定改写：
// 时间列若按 gorm/驱动的默认布局写成 `2006-01-02 15:04:05.999999999-07:00`（空格分隔），
// 事件游标与用量区间过滤（都按字符串比较时间）会整体错位；布尔列若落成 'true'/'false'
// 文本，`WHERE archived = 0/1` 会全部落空，且既有库文件读不出来。
//
// 因此这里不测「Go 值对不对」（那是别的测试的事），只测库里真实存的 typeof 与文本形态，
// 把 model 的 serializer 约定钉死在测试里。
func TestOnDiskFormatContract(t *testing.T) {
	s := openMem(t)
	ctx := ctxT()
	// 取一个带纳秒的时间：RFC3339Nano 会裁掉末尾多余的 0，必须能原样往返。
	at := time.Date(2026, 9, 10, 10, 42, 23, 123456789, time.UTC)

	if err := s.UpsertConversation(ctx, Conversation{
		ID: "c1", AgentID: "a1", Cwd: "/tmp/x", Status: "in_progress",
		Pinned: true, Archived: false, WorkspaceTemporary: true,
		CreatedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatalf("UpsertConversation: %v", err)
	}
	if _, err := s.AppendEvent(ctx, "c1", "assistant_message", map[string]any{"text": "hi"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := s.CreateRun(ctx, Run{ID: "r1", ConversationID: "c1", Status: "running", StartedAt: at}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	// 未结束的 run：ended_at 落空串（不是零值时间戳）。
	if err := s.CreateMessages(ctx, []Message{{
		ConversationID: "c1", RunID: "r1", Role: "user", Text: "hi", SeqLo: 1, SeqHi: 1, CreatedAt: at,
	}}); err != nil {
		t.Fatalf("CreateMessages: %v", err)
	}
	if err := s.AddMemory(ctx, Memory{ConversationID: "c1", Title: "t", Kind: "k", Decision: "allow", CreatedAt: at}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}

	// 每项：表、列、期望的 typeof、期望的文本值（"" = 只校验类型不校验值）。
	cases := []struct {
		table      string
		column     string
		wantType   string
		wantValue  string
		checkValue bool
	}{
		// 注意 updated_at：追加事件会顺带抬高会话水位并刷新它（touchConversationLastSeq
		// 的既有语义，也是会话列表按 updated_at 排序的依据），故这里只校验形态不校验值。
		{"conversations", "created_at", "text", at.Format(model.LayoutTS), true},
		{"conversations", "updated_at", "text", "", false},
		{"conversations", "pinned", "integer", "1", true},
		{"conversations", "archived", "integer", "0", true},
		{"conversations", "workspace_temporary", "integer", "1", true},
		{"events", "created_at", "text", "", false},
		{"runs", "started_at", "text", at.Format(model.LayoutTS), true},
		{"runs", "ended_at", "text", "", true}, // 未结束 → 空串，而非零值时间
		{"messages", "created_at", "text", at.Format(model.LayoutTS), true},
		{"permission_memories", "created_at", "text", at.Format(model.LayoutTS), true},
		{"schema_migrations", "applied_at", "text", "", false},
	}
	for _, tc := range cases {
		var gotType, gotValue string
		row := s.db.Raw("SELECT typeof(" + tc.column + "), CAST(" + tc.column + " AS TEXT) FROM " + tc.table + " LIMIT 1").Row()
		if err := row.Scan(&gotType, &gotValue); err != nil {
			t.Errorf("%s.%s 读取失败: %v", tc.table, tc.column, err)
			continue
		}
		if gotType != tc.wantType {
			t.Errorf("%s.%s 的 typeof = %q，期望 %q（落库表示被改写）", tc.table, tc.column, gotType, tc.wantType)
		}
		if tc.checkValue && gotValue != tc.wantValue {
			t.Errorf("%s.%s = %q，期望 %q", tc.table, tc.column, gotValue, tc.wantValue)
		}
		if tc.wantType == "text" && !tc.checkValue && gotValue != "" && model.ParseTime(gotValue).IsZero() {
			t.Errorf("%s.%s = %q 不是可解析的 RFC3339Nano 文本", tc.table, tc.column, gotValue)
		}
	}

	// 读回必须与写入逐纳秒相等（写入 → 文本 → 读回 的完整往返）。
	conv, ok, err := s.GetConversation(ctx, "c1")
	if err != nil || !ok {
		t.Fatalf("GetConversation: ok=%v err=%v", ok, err)
	}
	if !conv.CreatedAt.Equal(at) {
		t.Errorf("会话 created_at 往返失真: %s 期望 %s", conv.CreatedAt, at)
	}
	// 期间追加过事件，updated_at 被刷新为「此刻」；只需保证它是可解析的、且不早于 created_at。
	if conv.UpdatedAt.IsZero() {
		t.Error("会话 updated_at 读回为零值（文本未按 RFC3339Nano 解析）")
	}
	if conv.UpdatedAt.Before(conv.CreatedAt) {
		t.Errorf("会话 updated_at(%s) 早于 created_at(%s)", conv.UpdatedAt, conv.CreatedAt)
	}
	if !conv.Pinned || conv.Archived || !conv.WorkspaceTemporary {
		t.Errorf("布尔列往返失真: pinned=%v archived=%v temp=%v", conv.Pinned, conv.Archived, conv.WorkspaceTemporary)
	}

	runs, err := s.ListRuns(ctx, "c1")
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns: n=%d err=%v", len(runs), err)
	}
	if !runs[0].StartedAt.Equal(at) {
		t.Errorf("run.started_at 往返失真: %s 期望 %s", runs[0].StartedAt, at)
	}
	if !runs[0].EndedAt.IsZero() {
		t.Errorf("未结束的 run，ended_at 应为零值，实际 %s", runs[0].EndedAt)
	}

	evs, _, err := s.EventsAfter(ctx, "c1", 0, 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("EventsAfter: n=%d err=%v", len(evs), err)
	}
	if evs[0].CreatedAt.IsZero() {
		t.Error("event.created_at 读回为零值（文本未按 RFC3339Nano 解析）")
	}
}

// TestUpsertForImportKeepsModelSelection transcript 导入不得覆盖会话级模型选择。
//
// transcript header 里没有 provider/model/runtime_hint/provider_session_id 四个字段，
// 若导入沿用全列 upsert 语义，会把它们按零值覆盖——用户选过的模型在重复导入后消失。
func TestUpsertForImportKeepsModelSelection(t *testing.T) {
	s := openMem(t)
	ctx := ctxT()
	if err := s.UpsertConversation(ctx, Conversation{
		ID: "c1", AgentID: "a1", Status: "completed",
		Provider: "p1", Model: "m1", RuntimeHint: "opencode", ProviderSessionID: "ses_1",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertConversation: %v", err)
	}

	// 模拟导入：header 只带标题等字段，模型选择相关字段为空。
	if _, _, err := s.ImportBatch(ctx, []ImportItem{{
		Conversation: Conversation{ID: "c1", AgentID: "a1", Title: "导入标题", Status: "completed"},
		Events:       []Event{{Type: "assistant_message", PayloadJSON: `{"text":"x"}`}},
	}}, "", 0, 0); err != nil {
		t.Fatalf("ImportBatch: %v", err)
	}

	got, ok, err := s.GetConversation(ctx, "c1")
	if err != nil || !ok {
		t.Fatalf("GetConversation: ok=%v err=%v", ok, err)
	}
	if got.Provider != "p1" || got.Model != "m1" || got.RuntimeHint != "opencode" || got.ProviderSessionID != "ses_1" {
		t.Errorf("导入覆盖了会话级模型选择: provider=%q model=%q hint=%q session=%q",
			got.Provider, got.Model, got.RuntimeHint, got.ProviderSessionID)
	}
	if got.Title != "导入标题" {
		t.Errorf("导入应更新标题，实际 %q", got.Title)
	}
	if got.LastSeq == 0 {
		t.Error("导入后会话水位未推进")
	}
}
