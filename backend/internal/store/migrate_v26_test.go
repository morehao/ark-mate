package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/pkg/dbclient"
)

// openV25ForV26Test 造一个**真实的 v25 库**（完整跑 v1..v25）。
//
// 本步要读的旧形态是「`sessions` 还有 `provider_id` 列、`model_id` 装的还是底座的不透明值」，
// 对着已经改过的库测等于什么都没测。
func openV25ForV26Test(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v25.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 25)
	return s
}

// TestMigrateV26DropsProviderAndMarksBackfill v26（会话模型引用改平台行 id）：
// `sessions.provider_id` 出列 + 置"待回填"标记，**`model_id` 一个字都不改**。
//
// 每一段判据对应一种静默退化：
//
//	① `provider_id` 列真的没了（它是 v19 之后恒空的列，留着只会让下一个读它的人以为这里有事实）；
//	② `model_id` **逐字未变**——回填是**应用层**的事（迁移里做不了对账：store 不得 import adapter），
//	   所以迁移只置标记。这里若"顺手"改写了 model_id，回填的输入就没了（不可逆）；
//	③ 标记 == `"26"`（回填的触发条件；它同时是"回填还没跑"的证据）；
//	④ 4 条 sessions 索引仍在且名字不变（DROP COLUMN 会原子重写表定义，索引不该受影响——但
//	   "不该"要机器判据）；
//	⑤ 幂等：再跑一次 `Migrate` 不改任何东西。
func TestMigrateV26DropsProviderAndMarksBackfill(t *testing.T) {
	ctx := context.Background()
	s := openV25ForV26Test(t)

	at := "2026-05-06T10:00:00Z"
	// `model_id` 装的是 v25 的形态：**底座的不透明值**（dsh 下就是那个 JSON 数组）。
	// 回填的判据在 manager 的测试里（要 stub 注册表），这里只钉"迁移没碰它"。
	type sessFix struct {
		id, provider, model string
	}
	fixtures := []sessFix{
		{"ses_01J9Z6Q0M7X2V8K4B3N5TRW2Q1", "deepseek-main", `["mate-deepseek-main","deepseek-chat"]`},
		{"ses_01J9Z6Q0M7X2V8K4B3N5TRW2Q2", "qwen-gw", `["mate-qwen-gw","qwen-max"]`},
		{"ses_01J9Z6Q0M7X2V8K4B3N5TRW2Q3", "", ``},
	}
	for _, f := range fixtures {
		if err := s.db.Exec(`INSERT INTO sessions
			(id, cwd, workspace_id, workspace_temporary, provider_id, model_id, runtime_id, runtime_session_id,
			 title, pinned, archived, status, origin, capability_json, capability_fingerprint, last_seq, created_at, updated_at)
			VALUES (?, '', '', 0, ?, ?, 'rtx', '', '', 0, 0, 'completed', '', '', '', 0, ?, ?)`,
			f.id, f.provider, f.model, at, at).Error; err != nil {
			t.Fatalf("写会话夹具 %s: %v", f.id, err)
		}
	}

	// 钉住"我们确实从 v25 出发"：那时还有 provider_id 列，且没有回填标记。
	var n int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='provider_id'`).
		Row().Scan(&n); err != nil || n != 1 {
		t.Fatalf("v25 的 sessions 应有 provider_id 列（夹具造错版本了？）: n=%d err=%v", n, err)
	}
	if _, ok, err := s.GetMeta(ctx, "sessions_model_ref_backfill"); err != nil || ok {
		t.Fatalf("v25 不该有回填标记: ok=%v err=%v", ok, err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("迁移到 v26: %v", err)
	}

	// ① 列没了。
	if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='provider_id'`).
		Row().Scan(&n); err != nil || n != 0 {
		t.Fatalf("v26 之后 sessions.provider_id 必须已出列: n=%d err=%v", n, err)
	}
	// `model_id` **必须还在**（它是回填的输入与落点）。
	if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='model_id'`).
		Row().Scan(&n); err != nil || n != 1 {
		t.Fatalf("v26 之后 sessions.model_id 必须仍在: n=%d err=%v", n, err)
	}

	// ② `model_id` 逐字未变。
	for _, f := range fixtures {
		got, ok, err := s.GetSession(ctx, f.id)
		if err != nil || !ok {
			t.Fatalf("读回会话 %s: ok=%v err=%v", f.id, ok, err)
		}
		if got.ModelID != f.model {
			t.Fatalf("会话 %s 的 model_id 被迁移改写了：got %q want %q"+
				"（回填是应用层的事，迁移只置标记）", f.id, got.ModelID, f.model)
		}
	}

	// ③ 标记 == "26"。
	ver, ok, err := s.GetMeta(ctx, "sessions_model_ref_backfill")
	if err != nil || !ok || ver != "26" {
		t.Fatalf("回填标记应为 \"26\": ok=%v ver=%q err=%v", ok, ver, err)
	}

	// ④ 4 条 sessions 索引仍在、名字不变。
	for _, idx := range []string{"idx_sessions_list", "idx_sessions_origin", "idx_sessions_fingerprint", "idx_sessions_workspace"} {
		if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).
			Row().Scan(&n); err != nil || n != 1 {
			t.Fatalf("DROP COLUMN 之后索引 %s 不见了: n=%d err=%v", idx, n, err)
		}
	}

	// ⑤ 幂等。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("再跑一次 Migrate 应幂等: %v", err)
	}
	if ver, _, _ := s.GetMeta(ctx, "sessions_model_ref_backfill"); ver != "26" {
		t.Fatalf("幂等重跑后标记应仍是 \"26\"，实际 %q", ver)
	}
}
