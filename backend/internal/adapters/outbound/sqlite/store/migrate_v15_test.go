package store

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/morehao/ark-mate/internal/platform/object/objid"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/db"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
)

// v15 的形状不变式：平台模型 id = `mdl_` + 26 位 Crockford Base32。
var v15ModelIDShape = regexp.MustCompile(`^mdl_[0-9A-HJKMNP-TV-Z]{26}$`)

// openV14ForV15Test 造一个**真实的 v14 库**（跑完整的 v1..v14，含 v12/v13 的 Go 数据步骤）。
//
// 为什么不用 `applySchemaUpTo`：那一位只跑 DDL，而 v12/v13 的"换表"发生在 Go 步骤里——
// 只跑 DDL 会留下 `models_v12`/`models_v13` 这种中间形态，与线上 v14 库不是一回事。
func openV14ForV15Test(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v14.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 14)
	return s
}

// TestMigrateV15RecastsModelIDs R7：`models` 不进 DROP 清单 ⇒ 它的行与内容全部保留，
// 只有主键被重铸，且**唯一存活的引用** `meta.default_model` 同事务改指新 id。
//
// 为什么这条必须单独钉：`models` 是本次唯一"没有文件来源"的数据面（providers 有
// providers.json、定义类有定义目录），丢了只能人工重建；而"引用没跟着改"的后果是静默的
// ——默认模型解析到一行不存在的记录，界面上「默认」标记消失、会话起不来。
func TestMigrateV15RecastsModelIDs(t *testing.T) {
	ctx := context.Background()
	s := openV14ForV15Test(t)

	at := "2026-03-04T00:00:00Z"
	if err := s.db.Exec(`INSERT INTO models (id, provider_id, model_id, name, note, input_modalities, revision, created_at, updated_at) VALUES
		('m-old-a', 'p1', 'deepseek-chat', '主力', '备注甲', 'text,image', 3, ?, ?),
		('m-old-b', 'p1', 'deepseek-reasoner', '', '', 'text', 1, ?, ?)`, at, at, at, at).Error; err != nil {
		t.Fatalf("写模型夹具: %v", err)
	}
	if err := s.SetMeta(ctx, model.MetaKeyDefaultModel, "m-old-a"); err != nil {
		t.Fatalf("写默认模型: %v", err)
	}

	// v15 清 `imported_transcripts`（不清 → `mate migrate` 空转）。
	//
	// 这里**还**写一行 `events_last_seq`（字面量，v21 起仓里已没有这个常量）：v15 已经不再清它
	// ——它的取号语义整体被 v21 的 `event_sequence` 取代，删除搬到了 v21 那一步（无条件执行）。
	// 判据在下面 ⑤：这一行必须**活过** v15，并被 v21 删掉（`migrate_v21_test.go` 钉后半句）。
	if err := s.db.Exec(`INSERT INTO meta (key, value) VALUES ('events_last_seq', '136')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(ctx, model.MetaKeyImportMarker, "sha256:deadbeef"); err != nil {
		t.Fatal(err)
	}
	// 注意列名：v14 的库上它仍叫 `last_conversation_id`（v15 才 RENAME 成 `last_session_id`）。
	if err := s.db.Exec(`INSERT INTO automations (id, body, revision, updated_at, last_fired_at, last_status, last_reason,
		last_conversation_id, consecutive_failures, recent_runs)
		VALUES ('a1', '{}', 1, ?, '2026-03-01T00:00:00Z', 'completed', '跑过了', 'ses_old', 2, '[{"x":1}]')`,
		at).Error; err != nil {
		t.Fatalf("写 automation 夹具: %v", err)
	}
	// 同样用 v14 的历史表名（`sessions` 要等 v15 才建出来）。
	if err := s.db.Exec(`INSERT INTO conversations (id, agent_id, cwd, status, created_at, updated_at)
		VALUES ('ses_v14_row', 'a', '/tmp/w', 'completed', ?, ?)`, at, at).Error; err != nil {
		t.Fatalf("写会话夹具: %v", err)
	}

	type modelRow struct {
		ID, ProviderID, ModelID, Name, Note, InputModalities string
		Revision                                             int64
		CreatedAt, UpdatedAt                                 string
	}
	readModels := func() []modelRow {
		t.Helper()
		var rows []modelRow
		if err := s.db.Raw(`SELECT id, provider_id, model_id, name, note, input_modalities, revision, created_at, updated_at
			FROM models ORDER BY provider_id, model_id`).Scan(&rows).Error; err != nil {
			t.Fatalf("读模型行: %v", err)
		}
		return rows
	}
	before := readModels()
	if len(before) != 2 {
		t.Fatalf("夹具应有 2 条模型行，实际 %d", len(before))
	}

	// 只跑 v15（本用例的判据全在它身上）。
	migrateUpTo(t, s, 15)
	migrateUpTo(t, s, 15) // 幂等

	// ① 行数与内容列逐行相等（只有 id 变了）。
	after := readModels()
	if len(after) != len(before) {
		t.Fatalf("models 行数变了：%d → %d", len(before), len(after))
	}
	for i := range before {
		b, a := before[i], after[i]
		if b.ProviderID != a.ProviderID || b.ModelID != a.ModelID || b.Name != a.Name || b.Note != a.Note ||
			b.InputModalities != a.InputModalities || b.Revision != a.Revision ||
			b.CreatedAt != a.CreatedAt || b.UpdatedAt != a.UpdatedAt {
			t.Fatalf("模型行在重铸中失真：\n before %+v\n after  %+v", b, a)
		}
		if b.ID == a.ID {
			t.Fatalf("第 %d 行的 id 没有被重铸：%s", i, a.ID)
		}
	}

	// ② 全表 id 都是平台形状。
	for _, r := range after {
		if !v15ModelIDShape.MatchString(r.ID) {
			t.Fatalf("重铸后的模型 id 形状不对（期望 %s）：%q", v15ModelIDShape, r.ID)
		}
		if !objid.IsPlatformID(r.ID) {
			t.Fatalf("重铸后的模型 id 没通过 objid.IsPlatformID：%q", r.ID)
		}
	}

	// ③ `meta.default_model` 指向**新** id（不是删、不是留着旧值）。
	//    映射关系按 (provider_id, model_id) 反查：v15 只换主键，这两个身份列逐行不变。
	def, ok, err := s.GetMeta(ctx, model.MetaKeyDefaultModel)
	if err != nil || !ok {
		t.Fatalf("默认模型应仍在：ok=%v err=%v", ok, err)
	}
	var wantID string
	for _, r := range after {
		if r.ModelID == "deepseek-chat" {
			wantID = r.ID
		}
	}
	if wantID == "" {
		t.Fatal("找不到重铸后的 deepseek-chat 行")
	}
	if def != wantID {
		t.Fatalf("meta.default_model 没有跟到新 id：got %q want %q", def, wantID)
	}
	if def == "m-old-a" {
		t.Fatal("meta.default_model 仍是旧 id（引用没重写）")
	}

	// ④ 被重建的 7 张表清空、`conversations` 不在、`sessions` 在。
	var n int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM sessions`).Row().Scan(&n); err != nil || n != 0 {
		t.Fatalf("sessions 应被重建为空：n=%d err=%v", n, err)
	}
	if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='conversations'`).
		Row().Scan(&n); err != nil || n != 0 {
		t.Fatalf("conversations 应已不存在：n=%d err=%v", n, err)
	}
	for _, tbl := range []string{"events", "runs", "messages", "permission_memories", "inputs", "workspaces"} {
		if err := s.db.Raw(`SELECT COUNT(*) FROM ` + tbl).Row().Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s 应被重建为空：n=%d err=%v", tbl, n, err)
		}
	}

	// ⑤ `imported_transcripts` 被清（漏掉它是**静默**失效：`mate migrate` 认为已导入然后空转）。
	if v, ok, err := s.GetMeta(ctx, model.MetaKeyImportMarker); err != nil || ok {
		t.Fatalf("meta.%s 应被清掉：ok=%v value=%q err=%v", model.MetaKeyImportMarker, ok, v, err)
	}
	//    而 `events_last_seq` **必须活着**（v15 只管清空 events 表的数据；那个计数行的语义
	//    由 v21 接手删除）。写死字面量：这是"停在 v15 那一版"的夹具，常量已在 v21 随语义删除。
	if v, ok, err := s.GetMeta(ctx, "events_last_seq"); err != nil || !ok || v != "136" {
		t.Fatalf("v15 不该动 events_last_seq（删除归 v21）：ok=%v value=%q err=%v", ok, v, err)
	}

	// ⑥ automations：运行态列被清，**`last_fired_at` 保留**（ADR-10：它是"这个 occurrence
	//    认领过没有"的唯一判据，清掉会让已跑过的 `once` 任务重启后再跑一次）。
	var fired, status, reason, lastSess, recent string
	var failures int64
	if err := s.db.Raw(`SELECT last_fired_at, last_status, last_reason, last_session_id, consecutive_failures, recent_runs
		FROM automations WHERE id = 'a1'`).
		Row().Scan(&fired, &status, &reason, &lastSess, &failures, &recent); err != nil {
		t.Fatalf("读 automation 运行态: %v", err)
	}
	if fired != "2026-03-01T00:00:00Z" {
		t.Fatalf("last_fired_at 必须保留（ADR-10），实际 %q", fired)
	}
	if status != "" || reason != "" || lastSess != "" || failures != 0 || recent != "[]" {
		t.Fatalf("automation 运行态列应被清空：status=%q reason=%q session=%q failures=%d recent=%q",
			status, reason, lastSess, failures, recent)
	}

	// ⑦ 版本到位。
	var maxV int
	if err := s.db.Raw(`SELECT MAX(version) FROM ` + model.TableNameSchemaMigration).Row().Scan(&maxV); err != nil {
		t.Fatalf("读版本: %v", err)
	}
	if maxV != 15 {
		t.Fatalf("本用例跑到 v15，实际版本 %d", maxV)
	}
}

// TestMigrateV15IsIdempotentOnFreshDB 全新库从 v1 顺序重放到 v15 不得报错。
//
// 这条专门抓"历史步骤被实体改名污染"：把 `migrate_v14.go` 的 `SELECT COUNT(*) FROM conversations`
// 换成 `model.TableNameSession` 之后，新库重放到 v14 时会去查一张**还不存在**的表 → 建库失败，
// 而本机老库（v14 早已 applied）永远测不出这条（守卫⑤ 是它的静态判据）。
func TestMigrateV15IsIdempotentOnFreshDB(t *testing.T) {
	s := openTestDB(t) // openTestDB 走 Open → Migrate（全量重放）
	ctx := context.Background()
	if v, err := s.SchemaVersion(ctx); err != nil || v != latestSchemaVersion() {
		t.Fatalf("新库版本应为 %d：v=%d err=%v", latestSchemaVersion(), v, err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
	// 最终形态的表集合（v20 起 16 张：v19 删了 `agents`、v20 加了 `mcp_secrets`）。这条判据问的
	// 是"最终形态"而不是"v15 当时"，所以它随每一次追加迁移漂移（v15 那一步自己的断言见上面
	// 按版本重放的那些用例）。
	var n int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN
		('meta','sessions','events','runs','messages','permission_memories','inputs','workspaces',
		 'skills','mcp_servers','mcp_secrets','automations','providers','models','definition_revisions','schema_migrations')`).
		Row().Scan(&n); err != nil || n != 16 {
		t.Fatalf("新库应有 16 张表：n=%d err=%v", n, err)
	}
}
