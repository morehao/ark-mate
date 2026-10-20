package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
)

// openV24ForV25Test 造一个**真实的 v24 库**（完整跑 v1..v24）。
//
// 本步要读的旧形态是「`models` 还没有两个容量列」，对着已经加过列的库测等于什么都没测。
func openV24ForV25Test(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v24.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 24)
	return s
}

// TestMigrateV25AddsNullableCapacityColumns v25（容量由平台声明）：`models` 补两列，
// **可空、无 DEFAULT**，存量行上恒为 NULL（不是 0）。
//
// 每一段判据对应一种静默退化：
//
//	① 两列都在，且 `notnull=0`、`dflt_value` 为空——写成 `NOT NULL DEFAULT 0` 或 `DEFAULT 262144`
//	   会让"未声明"与"声明成 0 / 某个数"在库里长得一模一样（守卫④ 也钉这一条）；
//	② 存量行读出来是 **NULL**（`*int64 == nil`），不是 0——这是"空 = 不下发该字段"的落库形态；
//	③ 其余列零变化（本步只加列，不该碰任何既有值）；
//	④ 幂等重跑不报错、值不变。
func TestMigrateV25AddsNullableCapacityColumns(t *testing.T) {
	ctx := context.Background()
	s := openV24ForV25Test(t)

	at := "2026-05-06T10:00:00Z"
	if err := s.db.Exec(`INSERT INTO providers (id, name, protocol, base_url, note, revision, created_at, updated_at, key)
		VALUES ('deepseek-main', 'DeepSeek', 'openai', 'https://api.deepseek.com/v1', '', 1, ?, ?, '')`, at, at).Error; err != nil {
		t.Fatalf("写 provider 夹具: %v", err)
	}
	if err := s.db.Exec(`INSERT INTO models (id, provider_id, model, name, note, input_modalities, revision, created_at, updated_at)
		VALUES ('mdl_01J9Z6Q0M7X2V8K4B3N5TRW2Q1', 'deepseek-main', 'gpt-4o', '大模型', '备注', 'text,image', 2, ?, ?)`,
		at, at).Error; err != nil {
		t.Fatalf("写 model 夹具: %v", err)
	}

	// 钉住"我们确实从 v24 出发"：v24 的 models 没有这两列。
	for _, col := range []string{"max_input_tokens", "max_output_tokens"} {
		var n int64
		if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_table_info('models') WHERE name=?`, col).
			Row().Scan(&n); err != nil || n != 0 {
			t.Fatalf("v24 的 models 不该有 %s 列（夹具造错版本了？）: n=%d err=%v", col, n, err)
		}
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("迁移到 v25: %v", err)
	}

	// ① 两列都在、可空、无默认值。
	type colInfo struct {
		NotNull int
		Dflt    *string
		Type    string
	}
	for _, col := range []string{"max_input_tokens", "max_output_tokens"} {
		var info colInfo
		if err := s.db.Raw(`SELECT "notnull", dflt_value, type FROM pragma_table_info('models') WHERE name=?`, col).
			Row().Scan(&info.NotNull, &info.Dflt, &info.Type); err != nil {
			t.Fatalf("读 %s 的列定义: %v", col, err)
		}
		if info.NotNull != 0 {
			t.Fatalf("%s 必须可空（NOT NULL 会逼着每一行携带一个假值）", col)
		}
		if info.Dflt != nil {
			t.Fatalf("%s 必须**无 DEFAULT**（0 当不了「未声明」的哨兵），实际 %q", col, *info.Dflt)
		}
		if info.Type != "INTEGER" {
			t.Fatalf("%s 的类型应为 INTEGER，实际 %q", col, info.Type)
		}
	}

	// ② 存量行：两个容量都是 NULL（**不是 0**）。
	row, found, err := s.GetModelDefinition(ctx, "mdl_01J9Z6Q0M7X2V8K4B3N5TRW2Q1")
	if err != nil || !found {
		t.Fatalf("读回模型行: found=%v err=%v", found, err)
	}
	if row.MaxInputTokens != nil || row.MaxOutputTokens != nil {
		t.Fatalf("存量行的容量应为 NULL（不是 0）：in=%v out=%v", row.MaxInputTokens, row.MaxOutputTokens)
	}

	// ③ 其余列零变化。
	if row.ProviderID != "deepseek-main" || row.Model != "gpt-4o" || row.Name != "大模型" ||
		row.Note != "备注" || row.InputModalities != "text,image" || row.Revision != 2 {
		t.Fatalf("本步只该加列，既有列却变了：%+v", row)
	}

	// ④ 幂等。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("再跑一次 Migrate 应幂等: %v", err)
	}
}
