package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
)

// TestMigrationV34CreatesArtifactsTable v34 的判据：新库升到 v34 后 artifacts 表存在、索引齐全、
// 为空表；library_nodes 有 category/provenance_id 两列且默认值正确；再跑一次幂等。
func TestMigrationV34CreatesArtifactsTable(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 34 {
		t.Fatalf("schema 版本应 ≥ 34，got %d (err=%v)", v, err)
	}

	// artifacts 表存在且空。
	var n int
	if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='artifacts'`).
		Row().Scan(&n); err != nil || n != 1 {
		t.Fatalf("artifacts 表应存在：n=%d err=%v", n, err)
	}
	wantIdx := []string{
		"idx_artifacts_session", "idx_artifacts_run", "idx_artifacts_upload",
		"idx_library_nodes_category", "idx_library_nodes_provenance",
	}
	for _, idx := range wantIdx {
		if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).
			Row().Scan(&n); err != nil || n != 1 {
			t.Fatalf("索引 %s 应存在：n=%d err=%v", idx, n, err)
		}
	}
	if got := countRows(t, s, "artifacts"); got != 0 {
		t.Fatalf("新库 artifacts 应为空表，got %d 行", got)
	}

	// 幂等：再跑一次不改任何东西。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复 Migrate: %v", err)
	}
}

// TestMigrationV34FromV33PreservesData 旧库（停在 v33）升级到 v34：既有资料库节点不被改写，
// 新列取默认值（category='material'、provenance_id=”），artifacts 为新空表。
func TestMigrationV34FromV33PreservesData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v33.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	raw := New(db)
	raw.path = path
	t.Cleanup(func() { _ = raw.Close() })
	migrateUpTo(t, raw, 33)

	// 插一条 v33 时期的 library_nodes 行（无 category/provenance 列）。
	if err := raw.db.Exec(`INSERT INTO library_nodes
		(id, space_id, parent_id, node_kind, title, body, table_id, drive_upload_id, source_url, size_bytes, created_at, updated_at)
		VALUES ('libnd_legacy','default','','folder','旧目录','','','','',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`).Error; err != nil {
		t.Fatalf("插 v33 节点: %v", err)
	}

	if err := raw.Migrate(ctx); err != nil {
		t.Fatalf("v33 → v34 Migrate: %v", err)
	}
	if v, _ := raw.SchemaVersion(ctx); v < 34 {
		t.Fatalf("升级后 schema 版本应 ≥ 34，got %d", v)
	}
	// 旧节点仍在，且新列取默认值。
	var category, provenance string
	if err := raw.db.Raw(`SELECT category, provenance_id FROM library_nodes WHERE id='libnd_legacy'`).
		Row().Scan(&category, &provenance); err != nil {
		t.Fatalf("读旧节点: %v", err)
	}
	if category != "material" {
		t.Fatalf("旧节点 category 默认应为 material，got %q", category)
	}
	if provenance != "" {
		t.Fatalf("旧节点 provenance_id 默认应为空，got %q", provenance)
	}
	if got := countRows(t, raw, "artifacts"); got != 0 {
		t.Fatalf("v34 后 artifacts 应为空表，got %d 行", got)
	}
}
