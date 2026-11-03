package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
)

// TestMigrationV33CreatesLibraryTables v33 的判据：新库升到 v33 后三张资料库表存在、索引齐全、
// 为空表；再跑一次幂等。
func TestMigrationV33CreatesLibraryTables(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 33 {
		t.Fatalf("schema 版本应 ≥ 33，got %d (err=%v)", v, err)
	}

	wantTables := []string{"library_nodes", "library_attachments", "library_tables"}
	for _, tbl := range wantTables {
		var n int
		if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).
			Row().Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s 表应存在：n=%d err=%v", tbl, n, err)
		}
	}

	wantIdx := []string{
		"idx_library_nodes_space_parent",
		"idx_library_nodes_kind",
		"idx_library_nodes_title",
		"idx_library_nodes_drive_upload",
		"idx_library_attachments_attachment",
	}
	for _, idx := range wantIdx {
		var n int
		if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).
			Row().Scan(&n); err != nil || n != 1 {
			t.Fatalf("索引 %s 应存在：n=%d err=%v", idx, n, err)
		}
	}

	// 幂等：再跑一次不改任何东西。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复 Migrate: %v", err)
	}
	for _, tbl := range wantTables {
		if n := countRows(t, s, tbl); n != 0 {
			t.Fatalf("新库 %s 应为空表，got %d 行", tbl, n)
		}
	}
}

// TestMigrationV33FromV32PreservesData 旧库（停在 v32）升级到 v33：既有会话数据不被改写，
// 三张资料库表为新空表。
func TestMigrationV33FromV32PreservesData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v32.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	raw := New(db)
	raw.path = path
	t.Cleanup(func() { _ = raw.Close() })
	migrateUpTo(t, raw, 32)
	seedSession(t, raw, "s-legacy")

	if err := raw.Migrate(ctx); err != nil {
		t.Fatalf("v32 → v33 Migrate: %v", err)
	}
	if v, _ := raw.SchemaVersion(ctx); v < 33 {
		t.Fatalf("升级后 schema 版本应 ≥ 33，got %d", v)
	}
	var stillThere int
	if err := raw.db.Raw(`SELECT COUNT(*) FROM sessions WHERE id='s-legacy'`).Row().Scan(&stillThere); err != nil || stillThere != 1 {
		t.Fatalf("升级后旧会话应仍在：n=%d err=%v", stillThere, err)
	}
	for _, tbl := range []string{"library_nodes", "library_attachments", "library_tables"} {
		if n := countRows(t, raw, tbl); n != 0 {
			t.Fatalf("v33 后 %s 应为空表，got %d 行", tbl, n)
		}
	}
}
