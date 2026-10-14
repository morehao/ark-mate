package store

import (
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/object/objid"
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
	s := openTestDB(t)
	ctx := ctxT()
	// 取一个带纳秒的时间：RFC3339Nano 会裁掉末尾多余的 0，必须能原样往返。
	at := time.Date(2026, 9, 10, 10, 42, 23, 123456789, time.UTC)

	if err := s.UpsertSession(ctx, model.SessionEntity{
		ID: "c1", Cwd: "/tmp/x", Status: "in_progress",
		Pinned: true, Archived: false, WorkspaceTemporary: true,
		// v7 三列（M3-1）：来源轴 + 钉住的能力明细与指纹。它们是**文本列**（不是布尔/时间），
		// 但仍要在这里露一次脸：JSON 明细将来若被改成 BLOB/结构化列，落库形态一变就再也读不回来。
		Origin: "automation:nightly", CapabilityJSON: `{"skills":["s1"]}`, CapabilityFingerprint: "fp-abc",
		CreatedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	if _, err := s.AppendEvent(ctx, objid.New(objid.Event), "c1", "assistant_message", map[string]any{"text": "hi"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := s.CreateRun(ctx, model.RunEntity{ID: "r1", SessionID: "c1", Status: "running", StartedAt: at}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	// 未结束的 run：ended_at 落空串（不是零值时间戳）。
	if err := s.CreateMessages(ctx, []model.MessageEntity{{
		ID:        objid.New(objid.Message),
		SessionID: "c1", RunID: "r1", Role: "user", Text: "hi", SeqLo: 1, SeqHi: 1, CreatedAt: at,
	}}); err != nil {
		t.Fatalf("CreateMessages: %v", err)
	}
	if err := s.AddMemory(ctx, model.PermissionMemoryEntity{ID: objid.New(objid.PermissionMemory), SessionID: "c1", Title: "t", Kind: "k", Decision: "allow", CreatedAt: at}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	// v8 三张表（M6-1）：这里**直接调 dao**（而不是 store 门面），为的是钉住精确的落库文本
	// ——门面内部取"此刻"，而这一层要断言的是"写入的纳秒原样存下来"。
	if ok, err := s.skills.CreateCAS(ctx, &model.SkillEntity{
		ID: "sk-1", Body: `{"name":"格式探针"}`, Revision: 1, UpdatedAt: at,
	}); err != nil || !ok {
		t.Fatalf("写 skills 行: ok=%v err=%v", ok, err)
	}
	if ok, err := s.mcpServers.CreateCAS(ctx, &model.McpServerEntity{
		ID: "mcp-1", Body: `{"transport":"stdio"}`, Revision: 1, UpdatedAt: at,
	}); err != nil || !ok {
		t.Fatalf("写 mcp_servers 行: ok=%v err=%v", ok, err)
	}
	if ok, err := s.automations.CreateCAS(ctx, &model.AutomationEntity{
		ID: "auto-1", Body: `{"name":"格式探针"}`, Revision: 1, UpdatedAt: at,
		LastFiredAt: at.Format(model.LayoutTS), LastStatus: "completed", ConsecutiveFailures: 2,
		RecentRuns: `[{"status":"completed"}]`,
	}); err != nil || !ok {
		t.Fatalf("写 automations 行: ok=%v err=%v", ok, err)
	}
	if err := s.definitionRevision.Append(ctx, &model.DefinitionRevisionEntity{
		Kind: "skill", DefinitionID: "sk-1", Action: "create", Actor: "test", At: at, Digest: "d",
	}); err != nil {
		t.Fatalf("写 definition_revisions 行: %v", err)
	}
	// v12/v13（设置 · 模型）：供应商行 + 应用层模型清单行。
	//
	// **落库格式的硬契约仍然是两条**：时间列 TEXT + RFC3339Nano(UTC)、布尔列 INTEGER 0/1。
	// v12 把模型面的**布尔列删掉了**（`is_default` 换成 meta 的单一值）、v13 又把两张表列化
	// （多了两个时间列），所以这条用例里布尔列的样本仍由「会话置顶」承担——契约本身不变，
	// 样本源换一个；而**新增的时间列必须走同一个 serializer**（`created_at` 纳秒原样往返，
	// 见下面 cases 里的两行）。
	if ok, err := s.providers.CreateCAS(ctx, &model.ProviderEntity{
		ID: "pv-1", Name: "DeepSeek", Protocol: "openai", BaseURL: "https://api.deepseek.com/v1",
		Revision: 1, CreatedAt: at, UpdatedAt: at,
	}); err != nil || !ok {
		t.Fatalf("写 providers 行: ok=%v err=%v", ok, err)
	}
	if ok, err := s.models.CreateCAS(ctx, &model.ModelEntity{
		ID: "md-1", ProviderID: "pv-1", ModelID: "deepseek-v4-flash",
		Name: "Flash", InputModalities: "text", Revision: 1, CreatedAt: at, UpdatedAt: at,
	}); err != nil || !ok {
		t.Fatalf("写 models 行: ok=%v err=%v", ok, err)
	}

	// 每项：表、列、期望的 typeof、期望的文本值（"" = 只校验类型不校验值）。
	cases := []struct {
		table      string
		column     string
		wantType   string
		wantValue  string
		checkValue bool
	}{
		// 注意 updated_at：追加事件会顺带抬高会话水位并刷新它（touchSessionLastSeq
		// 的既有语义，也是会话列表按 updated_at 排序的依据），故这里只校验形态不校验值。
		{"sessions", "created_at", "text", at.Format(model.LayoutTS), true},
		{"sessions", "updated_at", "text", "", false},
		{"sessions", "pinned", "integer", "1", true},
		{"sessions", "archived", "integer", "0", true},
		{"sessions", "workspace_temporary", "integer", "1", true},
		{"sessions", "origin", "text", "automation:nightly", true},
		{"sessions", "capability_json", "text", `{"skills":["s1"]}`, true},
		{"sessions", "capability_fingerprint", "text", "fp-abc", true},
		// v13 列化新增的时间列：必须与既有时间列**同一形态**（TEXT + RFC3339Nano(UTC)），
		// 纳秒原样往返——用别的 serializer 会写成空格分隔的本地时区串，区间比较整体错位。
		{"providers", "created_at", "text", at.Format(model.LayoutTS), true},
		{"providers", "updated_at", "text", at.Format(model.LayoutTS), true},
		{"models", "created_at", "text", at.Format(model.LayoutTS), true},
		{"models", "updated_at", "text", at.Format(model.LayoutTS), true},
		{"events", "created_at", "text", "", false},
		{"runs", "started_at", "text", at.Format(model.LayoutTS), true},
		{"runs", "ended_at", "text", "", true}, // 未结束 → 空串，而非零值时间
		{"messages", "created_at", "text", at.Format(model.LayoutTS), true},
		{"permission_memories", "created_at", "text", at.Format(model.LayoutTS), true},
		{"schema_migrations", "applied_at", "text", "", false},
		// v8（M6-1）：定义行的两个提升列 + 审计的时间列。`revision` 是 INTEGER（CAS 判据，
		// 与 `WHERE revision = ?` 的整型入参一致），时间列与既有各表同一布局。
		{"skills", "revision", "integer", "1", true},
		{"skills", "updated_at", "text", at.Format(model.LayoutTS), true},
		{"mcp_servers", "revision", "integer", "1", true},
		{"mcp_servers", "updated_at", "text", at.Format(model.LayoutTS), true},
		// v10（M6-7）：定义列与运行态列。运行态列全是**非空**的（未触发过 = 空串 / 0 / '[]'），
		// 不给 NULL 留第二种表示；`last_fired_at` 与其它时间列同一布局。
		{"automations", "revision", "integer", "1", true},
		{"automations", "updated_at", "text", at.Format(model.LayoutTS), true},
		{"automations", "last_fired_at", "text", at.Format(model.LayoutTS), true},
		{"automations", "last_status", "text", "completed", true},
		{"automations", "consecutive_failures", "integer", "2", true},
		{"automations", "recent_runs", "text", `[{"status":"completed"}]`, true},
		{"definition_revisions", "seq", "integer", "1", true},
		{"definition_revisions", "at", "text", at.Format(model.LayoutTS), true},
		// v12（设置 · 模型）：供应商行与**应用层**清单行。
		//
		// v12 把模型面的布尔列删掉了（`is_default` 换成 meta 的单一值），所以这里不再有
		// "裸 SQL 维护的布尔列"这个样本——但**契约本身不变**（布尔列一律 INTEGER 0/1），
		// 样本改由 `sessions.pinned` 承担（见上面的用例）。
		{"providers", "revision", "integer", "1", true},
		{"providers", "updated_at", "text", at.Format(model.LayoutTS), true},
		{"models", "provider_id", "text", "pv-1", true},
		{"models", "model_id", "text", "deepseek-v4-flash", true},
		{"models", "revision", "integer", "1", true},
		{"models", "updated_at", "text", at.Format(model.LayoutTS), true},
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
	conv, ok, err := s.GetSession(ctx, "c1")
	if err != nil || !ok {
		t.Fatalf("GetSession: ok=%v err=%v", ok, err)
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
	if conv.Origin != "automation:nightly" || conv.CapabilityJSON != `{"skills":["s1"]}` || conv.CapabilityFingerprint != "fp-abc" {
		t.Errorf("v7 三列往返失真: origin=%q json=%q fp=%q", conv.Origin, conv.CapabilityJSON, conv.CapabilityFingerprint)
	}

	// v8：定义行的时间列同样要逐纳秒往返（这是"M6 换掉 mtime 判据"之后
	// `updated_at` 唯一的对外可见用途——列表排序）。
	sk, found, err := s.GetSkillDefinition(ctx, "sk-1")
	if err != nil || !found {
		t.Fatalf("GetSkillDefinition: found=%v err=%v", found, err)
	}
	if !sk.UpdatedAt.Equal(at) {
		t.Errorf("skill.updated_at 往返失真: %s 期望 %s", sk.UpdatedAt, at)
	}
	if sk.Revision != 1 || sk.Body != `{"name":"格式探针"}` {
		t.Errorf("skill 提升列/正文往返失真: rev=%d body=%q", sk.Revision, sk.Body)
	}
	mcp, found, err := s.GetMcpServerDefinition(ctx, "mcp-1")
	if err != nil || !found {
		t.Fatalf("GetMcpServerDefinition: found=%v err=%v", found, err)
	}
	if !mcp.UpdatedAt.Equal(at) {
		t.Errorf("mcp.updated_at 往返失真: %s 期望 %s", mcp.UpdatedAt, at)
	}
	// v10：运行态列的往返（时间列逐纳秒、失败计数是整数、运行史原样）。
	auto, found, err := s.GetAutomationDefinition(ctx, "auto-1")
	if err != nil || !found {
		t.Fatalf("GetAutomationDefinition: found=%v err=%v", found, err)
	}
	if auto.LastFiredAt != at.Format(model.LayoutTS) || auto.ConsecutiveFailures != 2 ||
		auto.LastStatus != "completed" || auto.RecentRuns != `[{"status":"completed"}]` {
		t.Errorf("automation 运行态列往返失真: %+v", auto)
	}
	if auto.Revision != 1 || !auto.UpdatedAt.Equal(at) {
		t.Errorf("automation 定义列往返失真: rev=%d at=%s", auto.Revision, auto.UpdatedAt)
	}
	// v12/v13：provider 资源行与模型清单行的往返（时间列逐纳秒、身份列原样、内容列原样）。
	pv, found, err := s.GetProviderDefinition(ctx, "pv-1")
	if err != nil || !found {
		t.Fatalf("GetProviderDefinition: found=%v err=%v", found, err)
	}
	if pv.Revision != 1 || !pv.UpdatedAt.Equal(at) || !pv.CreatedAt.Equal(at) ||
		pv.Protocol != "openai" || pv.BaseURL != "https://api.deepseek.com/v1" || pv.Name != "DeepSeek" {
		t.Errorf("provider 行往返失真: rev=%d created=%s updated=%s %+v", pv.Revision, pv.CreatedAt, pv.UpdatedAt, pv)
	}
	md, found, err := s.GetModelDefinition(ctx, "md-1")
	if err != nil || !found {
		t.Fatalf("GetModelDefinition: found=%v err=%v", found, err)
	}
	if md.ProviderID != "pv-1" || md.ModelID != "deepseek-v4-flash" ||
		md.Revision != 1 || !md.UpdatedAt.Equal(at) || !md.CreatedAt.Equal(at) ||
		md.Name != "Flash" || md.InputModalities != "text" {
		t.Errorf("model 行往返失真: %+v", md)
	}

	revs, err := s.ListDefinitionRevisions(ctx, "skill", "sk-1")
	if err != nil || len(revs) != 1 {
		t.Fatalf("ListDefinitionRevisions: n=%d err=%v", len(revs), err)
	}
	if !revs[0].At.Equal(at) {
		t.Errorf("definition_revisions.at 往返失真: %s 期望 %s", revs[0].At, at)
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
// transcript header 里没有 provider_id/model_id/runtime_id/runtime_session_id 四个字段，
// 若导入沿用全列 upsert 语义，会把它们按零值覆盖——用户选过的模型在重复导入后消失。
func TestUpsertForImportKeepsModelSelection(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	if err := s.UpsertSession(ctx, model.SessionEntity{
		ID: "c1", Status: "completed",
		ProviderID: "p1", ModelID: "m1", RuntimeID: "rtx", RuntimeSessionID: "rt_ses_1",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	// 模拟导入：header 只带标题等字段，模型选择相关字段为空。
	// 事件行带身份（v17 起导入按 id 判重，缺 id 整批回滚）。
	if _, _, err := s.ImportBatch(ctx, []ImportItem{{
		Session: model.SessionEntity{ID: "c1", Title: "导入标题", Status: "completed"},
		Events: []model.EventEntity{
			{ID: objid.New(objid.Event), Type: "assistant_message", PayloadJSON: `{"text":"x"}`},
		},
	}}, "", 0, 0); err != nil {
		t.Fatalf("ImportBatch: %v", err)
	}

	got, ok, err := s.GetSession(ctx, "c1")
	if err != nil || !ok {
		t.Fatalf("GetSession: ok=%v err=%v", ok, err)
	}
	if got.ProviderID != "p1" || got.ModelID != "m1" || got.RuntimeID != "rtx" || got.RuntimeSessionID != "rt_ses_1" {
		t.Errorf("导入覆盖了会话级模型选择: provider_id=%q model_id=%q runtime_id=%q session=%q",
			got.ProviderID, got.ModelID, got.RuntimeID, got.RuntimeSessionID)
	}
	if got.Title != "导入标题" {
		t.Errorf("导入应更新标题，实际 %q", got.Title)
	}
	if got.LastSeq == 0 {
		t.Error("导入后会话水位未推进")
	}
}
