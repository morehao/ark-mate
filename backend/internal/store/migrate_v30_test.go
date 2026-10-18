package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/infra/dbclient"
)

// openV29ForV30Test 造一个**真实的 v29 库**（完整跑 v1..v29，含各步的 Go 数据迁移）。
//
// 为什么不用 `openTestDB`（它直接落到 v30）：本步要读的正是**旧形态**——`runs.status` 还是
// v1 默认值 `running`。对着归一过的库测，等于什么都没测。
func openV29ForV30Test(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v29.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path // v30 带备份，需要库文件路径
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 29)
	return s
}

// TestMigrationV30NormalizesLegacyRunningStatus v30 的判据：历史 `runs.status='running'`
// 在迁移后变成 `in_progress`，且**不会**动别的取值；再跑一次幂等。
//
// 这条判据挡的是一处已经存在的静默缺陷：写入方（`OpenRun`）从 v21 起就写 `in_progress`，
// 而查询方（`RunningRows`）与用量计数读的是 `running`——两边各自"能编译、能跑"，结果恒空。
// 只改查询不改历史行的话，那些**已经写进库的**旧行会被永远漏掉。
func TestMigrationV30NormalizesLegacyRunningStatus(t *testing.T) {
	ctx := context.Background()
	s := openV29ForV30Test(t)
	seedSession(t, s, "s1")

	// 种三行：历史 running / 已经是 in_progress / 终态 completed。
	insert := `INSERT INTO runs (id, session_id, user_text, status, first_seq, last_seq, started_at, ended_at)
		VALUES (?, 's1', '', ?, ?, -1, '2026-01-01T00:00:00Z', '')`
	for i, tc := range []struct{ id, status string }{
		{"r_legacy", "running"},
		{"r_open", "in_progress"},
		{"r_done", "completed"},
	} {
		if err := s.db.Exec(insert, tc.id, tc.status, i).Error; err != nil {
			t.Fatalf("种 run 行 %s: %v", tc.id, err)
		}
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate(v30): %v", err)
	}
	if v, err := s.SchemaVersion(ctx); err != nil || v < 30 {
		t.Fatalf("schema 版本应 ≥ 30，got %d (err=%v)", v, err)
	}

	want := map[string]string{
		"r_legacy": "in_progress",
		"r_open":   "in_progress",
		"r_done":   "completed", // 终态不许被顺手改写
	}
	for id, w := range want {
		var got string
		if err := s.db.Raw(`SELECT status FROM runs WHERE id = ?`, id).Row().Scan(&got); err != nil {
			t.Fatalf("读 %s 状态: %v", id, err)
		}
		if got != w {
			t.Fatalf("%s 的状态 = %q（want %q）：v30 只该把 running 归一成 in_progress", id, got, w)
		}
	}

	// 幂等：再跑一次不改任何东西。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复 Migrate: %v", err)
	}
	var n int
	if err := s.db.Raw(`SELECT COUNT(*) FROM runs`).Row().Scan(&n); err != nil || n != 3 {
		t.Fatalf("重复迁移后行数 = %d (err=%v)，want 3", n, err)
	}
}

// TestRunningRowsSeesOpenRun 修掉「查询恒空」之后，`RunningRuns` 必须真的看见开轮行；
// 收口之后必须看不见。
func TestRunningRowsSeesOpenRun(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	seedSession(t, s, "s1")

	if err := s.CreateRun(ctx, model.RunEntity{
		ID: "r1", SessionID: "s1", Status: "in_progress", FirstSeq: 0, LastSeq: -1,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	rows, err := s.RunningRuns(ctx, "s1")
	if err != nil {
		t.Fatalf("RunningRuns: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "r1" {
		t.Fatalf("进行中的行 = %+v（want 恰好 r1）：查询与写入方同源之后这里必须非空", rows)
	}

	if err := s.FinishRun(ctx, "r1", model.RunFinishPatch{Status: "completed", LastSeq: 0}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	rows, err = s.RunningRuns(ctx, "s1")
	if err != nil {
		t.Fatalf("RunningRuns(收口后): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("收口后仍有进行中的行 = %+v", rows)
	}
}
