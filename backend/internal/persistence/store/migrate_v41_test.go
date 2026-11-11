package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
)

// TestMigrationV41AddsAppSource v41 的判据：新库升到 v41 后 apps 有 source 列（TEXT NOT NULL、
// 默认 'self'），再跑一次迁移幂等。
func TestMigrationV41AddsAppSource(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 41 {
		t.Fatalf("schema 版本应 ≥ 41，got %d (err=%v)", v, err)
	}
	if !hasColumn(t, s, "apps", "source") {
		t.Fatalf("apps 应有 source 列（v41）")
	}
	// 幂等：再跑一次不改任何东西。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复 Migrate: %v", err)
	}
}

// TestMigrationV41FromV40DefaultsSelf 旧库（停在 v40）升级到 v41：既有 apps 行保留，source 取默认 'self'
// （历史应用都是自建）。
func TestMigrationV41FromV40DefaultsSelf(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v40.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	raw := New(db)
	raw.path = path
	t.Cleanup(func() { _ = raw.Close() })
	migrateUpTo(t, raw, 40)

	// 插一条 v40 时期的 apps 行（无 source 列）。
	if err := raw.db.Exec(`INSERT INTO apps
		(id, name, description, avatar, tags_json, entities_json, tool_domains_json, slots_json,
		 page_json, settings_schema_json, api_version, enabled, created_at, updated_at)
		VALUES ('app_legacy','老应用','','','[]','[]','[]','[]','','','^1',1,1000,1000)`).Error; err != nil {
		t.Fatalf("插 v40 app 行: %v", err)
	}

	if err := raw.Migrate(ctx); err != nil {
		t.Fatalf("v40 → v41 Migrate: %v", err)
	}
	if v, _ := raw.SchemaVersion(ctx); v < 41 {
		t.Fatalf("升级后 schema 版本应 ≥ 41，got %d", v)
	}
	// 旧行仍在，source 默认 'self'。
	var source, name string
	if err := raw.db.Raw(`SELECT source, name FROM apps WHERE id='app_legacy'`).
		Row().Scan(&source, &name); err != nil {
		t.Fatalf("读旧 app 行: %v", err)
	}
	if source != "self" {
		t.Fatalf("旧 app 行 source 默认应为 'self'，got %q", source)
	}
	if name != "老应用" {
		t.Fatalf("旧 app 行 name 应保留，got %q", name)
	}
}
