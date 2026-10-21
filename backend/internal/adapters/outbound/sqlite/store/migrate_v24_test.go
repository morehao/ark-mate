package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/db"
)

// openV23ForV24Test 造一个**真实的 v23 库**（完整跑 v1..v23，含 v21–v23 的 Go 数据步骤）。
//
// 为什么不用 `openTestDB`（它直接落到最新版）：本步要读的正是**旧形态**——`models` 那一列还叫
// `model_id`。对着已经改过的库测等于什么都没测。
func openV23ForV24Test(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v23.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 23)
	return s
}

// TestMigrateV24RenamesModelIDToModel v24（命名收口）：`models.model_id` → `models.model`。
//
// **纯改名**——所以每一段判据都在问"除了名字，还有什么变了"，任何一处非空答案都是本步做错了：
//
//	① 列集合只差一个名字（`model` 在、`model_id` 不在）；
//	② 取值逐字相等（改名不是改值；含 `/` 与多字节的上游名，`RENAME COLUMN` 不该碰内容）；
//	③ 唯一索引仍在、名字不变，定义跟到 `(provider_id, model)`（`RENAME COLUMN` 自动重写）；
//	④ 行数 / `provider_id` / `revision` / 时间戳零变化；
//	⑤ 幂等：再跑一次 `Migrate` 不改任何东西。
func TestMigrateV24RenamesModelIDToModel(t *testing.T) {
	ctx := context.Background()
	s := openV23ForV24Test(t)

	at := "2026-05-06T10:00:00Z"
	// 三行、两个供应商：上游名里带 `/`（`llama/3-8b`）与多字节（`推理模型`）——它们在 YAML/JSON 里
	// 都要转义，是"改名顺手把值也过了一遍编码"这类错误的探针。
	fixtures := []struct {
		id, provider, model, name string
		rev                       int64
	}{
		{"mdl_01J9Z6Q0M7X2V8K4B3N5TRW2Q1", "deepseek-main", "deepseek-chat", "对话", 1},
		{"mdl_01J9Z6Q0M7X2V8K4B3N5TRW2Q2", "deepseek-main", "llama/3-8b", "", 3},
		{"mdl_01J9Z6Q0M7X2V8K4B3N5TRW2Q3", "qwen-gw", "推理模型", "多字节", 2},
	}
	for _, p := range []string{"deepseek-main", "qwen-gw"} {
		if err := s.db.Exec(`INSERT INTO providers (id, name, protocol, base_url, note, revision, created_at, updated_at, key)
			VALUES (?, '', 'openai', '', '', 1, ?, ?, '')`, p, at, at).Error; err != nil {
			t.Fatalf("写 provider 夹具 %s: %v", p, err)
		}
	}
	for _, f := range fixtures {
		if err := s.db.Exec(`INSERT INTO models (id, provider_id, model_id, name, note, input_modalities, revision, created_at, updated_at)
			VALUES (?,?,?,?, '', 'text', ?, ?, ?)`,
			f.id, f.provider, f.model, f.name, f.rev, at, at).Error; err != nil {
			t.Fatalf("写 model 夹具 %s: %v", f.id, err)
		}
	}

	// 先钉住"我们确实从旧形态出发"：v23 库有 `model_id`、没有 `model`。
	for _, want := range []struct {
		col  string
		have bool
	}{{"model_id", true}, {"model", false}} {
		var n int64
		if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_table_info('models') WHERE name=?`, want.col).
			Row().Scan(&n); err != nil {
			t.Fatalf("读 v23 列集合: %v", err)
		}
		if (n > 0) != want.have {
			t.Fatalf("v23 的 models 应 %s `%s` 列（夹具造错版本了？）", map[bool]string{true: "有", false: "没有"}[want.have], want.col)
		}
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("迁移到 v24: %v", err)
	}

	// ① 列集合：`model` 在、`model_id` 不在；其余列一个不少。
	cols := map[string]bool{}
	rows, err := s.db.Raw(`PRAGMA table_info(models)`).Rows()
	if err != nil {
		t.Fatalf("PRAGMA table_info(models): %v", err)
	}
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("扫列: %v", err)
		}
		cols[name] = true
	}
	_ = rows.Close()
	if !cols["model"] || cols["model_id"] {
		t.Fatalf("v24 的 models 列集合不对：model=%v model_id=%v", cols["model"], cols["model_id"])
	}
	for _, c := range []string{"id", "provider_id", "name", "note", "input_modalities", "revision", "created_at", "updated_at"} {
		if !cols[c] {
			t.Fatalf("v24 丢了列 %q（本步只许改名）", c)
		}
	}

	// ② 取值逐字相等（改名不是改值）。
	type gotRow struct {
		ID, ProviderID, Model, Name string
		Revision                    int64
		CreatedAt, UpdatedAt        string
	}
	var got []gotRow
	if err := s.db.Raw(`SELECT id, provider_id, model, name, revision, created_at, updated_at
		FROM models ORDER BY id`).Scan(&got).Error; err != nil {
		t.Fatalf("读改名后的行: %v", err)
	}
	if len(got) != len(fixtures) {
		t.Fatalf("行数变了: %d（期望 %d）", len(got), len(fixtures))
	}
	for i, f := range fixtures {
		g := got[i]
		if g.ID != f.id || g.ProviderID != f.provider || g.Model != f.model ||
			g.Name != f.name || g.Revision != f.rev || g.CreatedAt != at || g.UpdatedAt != at {
			t.Fatalf("第 %d 行在改名中失真：\n got %+v\n want %+v", i, g, f)
		}
	}

	// ③ 唯一索引：名字不变，定义跟到 `(provider_id, model)`。
	var idxSQL string
	if err := s.db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_models_provider_model'`).
		Row().Scan(&idxSQL); err != nil {
		t.Fatalf("v24 之后唯一索引不见了: %v", err)
	}
	if !strings.Contains(idxSQL, "provider_id") || !strings.Contains(idxSQL, "model") {
		t.Fatalf("唯一索引定义没有跟到新列名: %q", idxSQL)
	}
	if strings.Contains(idxSQL, "model_id") {
		t.Fatalf("唯一索引定义里还留着旧列名（RENAME 没重写索引）: %q", idxSQL)
	}

	// ④ 唯一性仍在：同一 (provider_id, model) 写第二行必须被库级索引拦下（索引若没跟到新列名，
	//    这一条会被放行——"唯一约束静默失效"正是本步最该防的退化）。
	if err := s.db.Exec(`INSERT INTO models (id, provider_id, model, name, note, input_modalities, revision, created_at, updated_at)
		VALUES ('mdl_dup', 'deepseek-main', 'deepseek-chat', '', '', 'text', 1, ?, ?)`, at, at).Error; err == nil {
		t.Fatal("同 (provider_id, model) 应被唯一索引拦下：索引没跟到新列名")
	}

	// ⑤ 幂等：再跑一次不改东西。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("再跑一次 Migrate 应幂等: %v", err)
	}
	var n int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM models WHERE model IN ('deepseek-chat','llama/3-8b','推理模型')`).
		Row().Scan(&n); err != nil || n != 3 {
		t.Fatalf("幂等重跑后原三行应仍在: n=%d err=%v", n, err)
	}
}
