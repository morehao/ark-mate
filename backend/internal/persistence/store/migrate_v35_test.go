package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
)

// TestMigrationV35AddsArtifactPath v35 的判据：新库升到 v35 后 artifacts 有 path 列（TEXT、默认 ”），
// 再跑一次迁移幂等。
func TestMigrationV35AddsArtifactPath(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 35 {
		t.Fatalf("schema 版本应 ≥ 35，got %d (err=%v)", v, err)
	}
	if !hasColumn(t, s, "artifacts", "path") {
		t.Fatalf("artifacts 应有 path 列（v35）")
	}
	// 幂等：再跑一次不改任何东西。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复 Migrate: %v", err)
	}
}

// TestMigrationV35FromV34PreservesArtifacts 旧库（停在 v34）升级到 v35：既有 artifacts 行保留，
// path 取默认空串（表示「无工作区原文件」）。
func TestMigrationV35FromV34PreservesArtifacts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v34.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	raw := New(db)
	raw.path = path
	t.Cleanup(func() { _ = raw.Close() })
	migrateUpTo(t, raw, 34)

	// 插一条 v34 时期的 artifacts 行（无 path 列）。
	if err := raw.db.Exec(`INSERT INTO artifacts
		(id, session_id, run_id, tool_call_id, origin, kind, title, mime_type, size_bytes, upload_id, content_text, content_sha256, dirty, created_at, updated_at)
		VALUES ('art_legacy','ses1','','','tool_write','file','old.txt','',5,'upl_1','','abc',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`).Error; err != nil {
		t.Fatalf("插 v34 产物行: %v", err)
	}

	if err := raw.Migrate(ctx); err != nil {
		t.Fatalf("v34 → v35 Migrate: %v", err)
	}
	if v, _ := raw.SchemaVersion(ctx); v < 35 {
		t.Fatalf("升级后 schema 版本应 ≥ 35，got %d", v)
	}
	// 旧行仍在，path 默认空。
	var pathCol, title string
	if err := raw.db.Raw(`SELECT path, title FROM artifacts WHERE id='art_legacy'`).
		Row().Scan(&pathCol, &title); err != nil {
		t.Fatalf("读旧产物行: %v", err)
	}
	if pathCol != "" {
		t.Fatalf("旧产物行 path 默认应为空，got %q", pathCol)
	}
	if title != "old.txt" {
		t.Fatalf("旧产物行 title 应保留，got %q", title)
	}
}

// hasColumn 判断某表是否有某列（PRAGMA table_info）。
func hasColumn(t *testing.T, s *Store, table, column string) bool {
	t.Helper()
	rows, err := s.db.Raw(`PRAGMA table_info(` + table + `)`).Rows()
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}
