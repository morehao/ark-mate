package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/dbclient"
)

// ——— v13 迁移（A8/A9） ———
//
// 这一组是本次改造**唯一的不可逆数据变换**的判据，所以夹具刻意覆盖设计点名的每种 body 形状
// （空 / 只有 name / 只有 note / 两者 / 含未知键）+ 一条**正文里带明文密钥**的行 +
// 一条 v12 里就是孤儿的 model 行，并且走**完整**的 v11 → v12 → v13 链（而不是直接构造 v12 行）：
// v13 面对的是 v12 改写后的形状，所以"v11 的 body 长什么样"只有走一遍 v12 才算真的被覆盖到。

// applyMigrationVersion 只应用某一个版本的迁移（走真实 DDL + 数据步骤 + 记录版本）。
func applyMigrationVersion(t *testing.T, s *Store, version int) {
	t.Helper()
	for _, m := range schemaMigrations {
		if m.version != version {
			continue
		}
		if err := s.applyMigration(context.Background(), m); err != nil {
			t.Fatalf("应用迁移 v%d: %v", version, err)
		}
		return
	}
	t.Fatalf("没有 v%d 这个迁移（版本表被改过？）", version)
}

// TestMigrateV13 A8：逐行等价 + 未知键/明文/近似时间/模态全部如实登记。
func TestMigrateV13(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	path := filepath.Join(home, "mate.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	defer func() { _ = s.Close() }()

	// 一个**旧的 v12 备份**：用来断言 `MigrationBackupPaths` 同时列 v12 与 v13，且新的在前（A9）。
	oldBackup := path + ".bak-v12-20260101T000000Z"
	if err := os.WriteFile(oldBackup, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	// ① 造一个真实的 v11 库 + v11 形状的行（含 v11 的历史键）。
	applySchemaUpTo(t, s, 11)
	exec := func(q string, args ...any) {
		t.Helper()
		if err := s.db.Exec(q, args...).Error; err != nil {
			t.Fatalf("夹具 SQL 失败: %v（%s）", err, q)
		}
	}
	exec(`INSERT INTO providers (id, body, revision, updated_at) VALUES (?, ?, 1, ?)`,
		"p-chain", `{"type":"deepseek","key_ref":"vault:providers/p-chain","model":"v11-历史陷阱键","note":"链路"}`,
		"2026-01-02T03:04:05.123456789Z")
	exec(`INSERT INTO models (id, runtime, provider_id, body, is_default, revision, updated_at)
	      VALUES (?, 'dsh', 'p-chain', ?, 1, 3, ?)`,
		"m-chain", `{"model_id":"deepseek-chat","name":"链路模型","extra":{"x":1}}`, "2026-01-02T03:04:06.123456789Z")

	// ② 真实跑一遍 v12（DDL + 归并 + 对账），于是库是**真的 v12**。
	applyMigrationVersion(t, s, 12)

	// ③ 在 v12 上补出设计点名的其余 body 形状 + 明文密钥行 + 孤儿 model 行。
	exec(`INSERT INTO providers (id, body, revision, updated_at) VALUES
		('p-empty',  '{}', 1, '2026-02-01T00:00:00Z'),
		('p-name',   '{"protocol":"openai","name":"只有名字"}', 2, '2026-02-02T00:00:00Z'),
		('p-note',   '{"protocol":"openai","note":"只有备注"}', 1, '2026-02-03T00:00:00Z'),
		('p-both',   '{"protocol":"openai","name":"两者","note":"备注","base_url":"https://both.example.com/v1"}', 5, '2026-02-04T00:00:00Z'),
		('p-unknown','{"protocol":"openai","weird":1,"model":"m","base_url":"https://u.example.com/v1"}', 1, '2026-02-05T00:00:00Z'),
		('p-plain',  '{"protocol":"openai","key":"sk-legacy-plaintext-probe"}', 1, '2026-02-06T00:00:00Z')`)
	exec(`INSERT INTO models (id, provider_id, model_id, body, revision, updated_at) VALUES
		('m-name',   'p-name', 'n-1', '{"name":"只有名字"}', 1, '2026-03-01T00:00:00Z'),
		('m-note',   'p-note', 'o-1', '{"note":"只有备注"}', 2, '2026-03-02T00:00:00Z'),
		('m-empty',  'p-both', 'e-1', '{}', 1, '2026-03-03T00:00:00Z'),
		('m-orphan', 'nope',   'x-1', '{"name":"孤儿"}', 1, '2026-03-04T00:00:00Z')`)

	beforeProv, beforeModels := countRows(t, s, "providers"), countRows(t, s, "models")

	// ④ v13。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("v12 → v13 升级失败: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}

	// ⑤ 行数与内容：**逐行对账**（列化不该改变任何平台字段）。
	if after := countRows(t, s, "providers"); after != beforeProv {
		t.Fatalf("供应商行数变了：%d → %d", beforeProv, after)
	}
	if after := countRows(t, s, "models"); after != beforeModels {
		t.Fatalf("模型行数变了：%d → %d", beforeModels, after)
	}

	type provWant struct {
		name, protocol, baseURL, note string
		rev                           int64
	}
	// `p-chain` 走的是**完整链**：v11 的 `type: deepseek` 在 v12 被映射成 openai +
	// 冻结端点表推导的 base_url，v13 再把它提成列；历史 `model` 键在 v12 就已被丢弃。
	// `p-empty` 没有 protocol → v13 的解析按 v12 导入映射的同一默认（openai）。
	// `p-plain` 的明文**不搬**（只有 id 进 notes）。
	provWants := map[string]provWant{
		"p-chain":   {"", "openai", "https://api.deepseek.com/v1", "链路", 1},
		"p-empty":   {"", "openai", "", "", 1},
		"p-name":    {"只有名字", "openai", "", "", 2},
		"p-note":    {"", "openai", "", "只有备注", 1},
		"p-both":    {"两者", "openai", "https://both.example.com/v1", "备注", 5},
		"p-unknown": {"", "openai", "https://u.example.com/v1", "", 1},
		"p-plain":   {"", "openai", "", "", 1},
	}
	rows, err := s.ListProviderDefinitions(ctx)
	if err != nil {
		t.Fatalf("列供应商: %v", err)
	}
	if len(rows) != len(provWants) {
		t.Fatalf("供应商条数不符: %d（期望 %d）", len(rows), len(provWants))
	}
	for i := range rows {
		w, ok := provWants[rows[i].ID]
		if !ok {
			t.Fatalf("出现了夹具之外的供应商行: %+v", rows[i])
		}
		got := rows[i]
		if got.Name != w.name || got.Protocol != w.protocol || got.BaseURL != w.baseURL ||
			got.Note != w.note || got.Revision != w.rev {
			t.Fatalf("供应商 %s 的列值不符：got %+v，want %+v", got.ID, got, w)
		}
		// 时间列：v11/v12 没有插入时刻的留痕，只能取 updated_at 近似值（如实登记，见 notes）。
		if model.FormatTime(got.CreatedAt) != model.FormatTime(got.UpdatedAt) {
			t.Fatalf("供应商 %s 的 created_at 应取 updated_at 近似值: %s vs %s",
				got.ID, model.FormatTime(got.CreatedAt), model.FormatTime(got.UpdatedAt))
		}
		if got.CreatedAt.IsZero() {
			t.Fatalf("供应商 %s 的 created_at 不该是零值", got.ID)
		}
	}

	type modelWant struct {
		providerID, modelID, name, note, modalities string
		rev                                         int64
	}
	modelWants := map[string]modelWant{
		// 链路上的那条：v11 的 `extra` 无 v12 出口（v12 的正文只留 name），v13 再提成列。
		"m-chain":  {"p-chain", "deepseek-chat", "链路模型", "", "text", 3},
		"m-name":   {"p-name", "n-1", "只有名字", "", "text", 1},
		"m-note":   {"p-note", "o-1", "", "只有备注", "text", 2},
		"m-empty":  {"p-both", "e-1", "", "", "text", 1},
		"m-orphan": {"nope", "x-1", "孤儿", "", "text", 1},
	}
	mrows, err := s.ListModelDefinitions(ctx)
	if err != nil {
		t.Fatalf("列模型: %v", err)
	}
	if len(mrows) != len(modelWants) {
		t.Fatalf("模型条数不符: %d（期望 %d）", len(mrows), len(modelWants))
	}
	for i := range mrows {
		w, ok := modelWants[mrows[i].ID]
		if !ok {
			t.Fatalf("出现了夹具之外的模型行: %+v", mrows[i])
		}
		got := mrows[i]
		if got.ProviderID != w.providerID || got.ModelID != w.modelID || got.Name != w.name ||
			got.Note != w.note || got.InputModalities != w.modalities || got.Revision != w.rev {
			t.Fatalf("模型 %s 的列值不符：got %+v，want %+v", got.ID, got, w)
		}
	}

	// ⑥ notes：四类事实都要登记（且**不含密钥值**）。
	notes, ok, err := s.MigrationNotesV13(ctx)
	if err != nil || !ok {
		t.Fatalf("读 v13 笔记: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(notes.BackupPath, ".bak-v13-") {
		t.Fatalf("备份路径应进 notes: %q", notes.BackupPath)
	}
	if len(notes.LegacyPlaintextKeys) != 1 || notes.LegacyPlaintextKeys[0] != "p-plain" {
		t.Fatalf("明文密钥行应逐条登记（只有 id）: %v", notes.LegacyPlaintextKeys)
	}
	if len(notes.DroppedBodyKeys) != 1 || !strings.Contains(notes.DroppedBodyKeys[0], "p-unknown") ||
		!strings.Contains(notes.DroppedBodyKeys[0], "weird") {
		t.Fatalf("未知键应登记键名: %v", notes.DroppedBodyKeys)
	}
	if len(notes.ModalityDefaulted) != len(modelWants) {
		t.Fatalf("按默认 text 填的条目应逐条登记: %v", notes.ModalityDefaulted)
	}
	if len(notes.OrphanModels) != 1 || !strings.Contains(notes.OrphanModels[0], "m-orphan") {
		t.Fatalf("孤儿 model 行应登记（不阻断）: %v", notes.OrphanModels)
	}
	if len(notes.CreatedAtApproximated) == 0 {
		t.Fatal("created_at 近似值应逐行登记")
	}

	// ⑦ **明文密钥在 DB 里不可达**：新表、meta、notes 里都搜不到它。
	assertNoPlaintextInDB(t, s, "sk-legacy-plaintext-probe")

	// ⑧ A9：备份路径同时列 v12 与 v13，且新的（v13）在前。
	paths := s.MigrationBackupPaths()
	if len(paths) < 2 {
		t.Fatalf("应同时列出 v13 与 v12 的备份: %v", paths)
	}
	if !strings.Contains(paths[0], ".bak-v13-") {
		t.Fatalf("新的备份应在最前（v13 优先）: %v", paths)
	}
	foundOld := false
	for _, p := range paths {
		if p == oldBackup {
			foundOld = true
		}
	}
	if !foundOld {
		t.Fatalf("旧的 v12 备份也应被列出: %v", paths)
	}
	if s.MigrationBackupPath() == "" {
		t.Fatal("本次迁移的备份路径应留在 Store 上")
	}
}

// TestMigrateV13RejectsNonOpenAIProtocol 前置检查：v12 里 protocol 不是 `openai` 的行 →
// **拒绝迁移**（非破坏、可解释、可重试），而不是撞上 CHECK 约束报一句难懂的错。
//
// 这也是"不静默改协议"的机器判据：协议是安全相关的（它决定把密钥发到哪种形态的端点）。
func TestMigrateV13RejectsNonOpenAIProtocol(t *testing.T) {
	ctx := context.Background()
	// 用**未迁移**的库自己造 v12 形态（`openTestDB` 会直接迁到最新，那时表已是 v13）。
	path := filepath.Join(t.TempDir(), "mate.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	defer func() { _ = s.Close() }()
	// 真的跑到 v12（DDL **与** 数据步骤），再插入一条 v12 形态的坏协议行——
	// v12 自己的改写恒写 `protocol: openai`，所以这条只能出现在它之后（导入路径就是了）。
	applySchemaUpTo(t, s, 11)
	applyMigrationVersion(t, s, 12)
	if err := s.db.Exec(
		`INSERT INTO providers (id, body, revision, updated_at) VALUES (?, ?, 1, ?)`,
		"p-bad", `{"protocol":"anthropic"}`, "2026-01-01T00:00:00Z").Error; err != nil {
		t.Fatal(err)
	}
	migErr := s.Migrate(ctx)
	if migErr == nil {
		t.Fatal("非 OpenAI 协议的供应商应让迁移拒绝（而不是静默改协议）")
	}
	if !strings.Contains(migErr.Error(), "p-bad") || !strings.Contains(migErr.Error(), "拒绝迁移") {
		t.Fatalf("错误应点名并说明「拒绝迁移」: %v", migErr)
	}
	// 整体回滚：库仍是 v12（新表没建出来）。
	var n int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='providers_v13'`).
		Row().Scan(&n); err != nil || n != 0 {
		t.Fatalf("拒绝迁移后不该留下临时表: n=%d err=%v", n, err)
	}
}

// countRows 数一张表的行数。
func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.Raw(`SELECT COUNT(*) FROM ` + table).Row().Scan(&n); err != nil {
		t.Fatalf("数 %s 行数: %v", table, err)
	}
	return n
}

// assertNoPlaintextInDB 扫全库所有表的所有列值 + meta 的 v13 笔记，断言某段明文一次都不出现。
func assertNoPlaintextInDB(t *testing.T, s *Store, secret string) {
	t.Helper()
	var tables []string
	if err := s.db.Raw(`SELECT name FROM sqlite_master WHERE type='table'`).Scan(&tables).Error; err != nil {
		t.Fatalf("读表清单: %v", err)
	}
	for _, table := range tables {
		rows, err := s.db.Raw(`SELECT * FROM ` + table).Rows()
		if err != nil {
			t.Fatalf("读 %s: %v", table, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				t.Fatalf("%s 扫行: %v", table, err)
			}
			for i, v := range vals {
				var text string
				switch tv := v.(type) {
				case nil:
					continue
				case []byte:
					text = string(tv)
				case string:
					text = tv
				default:
					text = string(rune(0)) // 非文本列不可能带密钥
				}
				if strings.Contains(text, secret) {
					_ = rows.Close()
					t.Fatalf("明文密钥出现在 %s.%s：%q（v13 起它只该存在于备份库的 providers.body.key）",
						table, cols[i], text)
				}
			}
		}
		_ = rows.Close()
	}
	// notes 单独再看一眼（它是"事实可读、值不可读"的那一半）。
	raw, ok, err := s.GetMeta(context.Background(), model.MetaKeyMigrationNotesV13)
	if err != nil || !ok {
		t.Fatalf("读 v13 笔记 meta: ok=%v err=%v", ok, err)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("迁移笔记里出现了明文密钥: %s", raw)
	}
	var parsed MigrationNotesV13
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("笔记不是合法 JSON: %v", err)
	}
}
