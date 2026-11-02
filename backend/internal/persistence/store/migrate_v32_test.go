package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/attachmentstore"
	"github.com/morehao/ark-mate/internal/infra/dbclient"
)

// TestMigrationV32CreatesAttachmentsTable v32 的判据：新库升到 v32 后 `attachments` 表存在、
// 四条索引齐全；旧库（停在 v31）升级后既有数据不被改写、`attachments` 为空表；再跑一次幂等。
func TestMigrationV32CreatesAttachmentsTable(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 32 {
		t.Fatalf("schema 版本应 ≥ 32，got %d (err=%v)", v, err)
	}

	// 表在，且四条索引齐全（G1 的"列集合"由 archtest 静态钉；这里钉真库里的对象存在）。
	var tbl int
	if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='attachments'`).
		Row().Scan(&tbl); err != nil || tbl != 1 {
		t.Fatalf("attachments 表应存在：n=%d err=%v", tbl, err)
	}
	wantIdx := []string{
		"idx_attachments_session",
		"idx_attachments_upload",
		"idx_attachments_sha256",
		"idx_attachments_state_created",
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
	if n := countRows(t, s, "attachments"); n != 0 {
		t.Fatalf("新库 attachments 应为空表，got %d 行", n)
	}
}

// TestMigrationV32FromV31PreservesData 旧库（停在 v31）升级到 v32：既有会话数据不被改写，
// attachments 为新空表。
//
// 为 v31 造库用 `migrateUpTo(t, s, 31)`（跑完整的 statements + apply）；随后写一行会话，
// 再 `Migrate` 到最新，断言会话仍在、attachments 表建出来且为空。
func TestMigrationV32FromV31PreservesData(t *testing.T) {
	ctx := context.Background()
	// 造一个"停在 v31"的库（`dbclient.Open` 只建句柄，不迁移；migrateUpTo 走完整 statements+apply 到 v31）。
	path := filepath.Join(t.TempDir(), "v31.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	raw := New(db)
	raw.path = path
	t.Cleanup(func() { _ = raw.Close() })
	migrateUpTo(t, raw, 31)
	seedSession(t, raw, "s-legacy")

	if err := raw.Migrate(ctx); err != nil {
		t.Fatalf("v31 → v32 Migrate: %v", err)
	}
	if v, _ := raw.SchemaVersion(ctx); v != 32 {
		t.Fatalf("升级后 schema 版本应为 32，got %d", v)
	}
	var stillThere int
	if err := raw.db.Raw(`SELECT COUNT(*) FROM sessions WHERE id='s-legacy'`).Row().Scan(&stillThere); err != nil || stillThere != 1 {
		t.Fatalf("升级后旧会话应仍在：n=%d err=%v", stillThere, err)
	}
	if n := countRows(t, raw, "attachments"); n != 0 {
		t.Fatalf("v32 后 attachments 应为空表，got %d 行", n)
	}
}

// TestFilestoreMigrateIdempotent `filestore.Migrate`（建 core_file*）幂等：连开两次不报错，
// 两表都在（本仓对 golib AutoMigrate 的显式例外的判据）。
func TestFilestoreMigrateIdempotent(t *testing.T) {
	s := openTestDB(t)
	if err := attachmentstore.Migrate(s.DB()); err != nil {
		t.Fatalf("第一次 filestore.Migrate: %v", err)
	}
	if err := attachmentstore.Migrate(s.DB()); err != nil {
		t.Fatalf("第二次 filestore.Migrate（应幂等）: %v", err)
	}
	for _, tbl := range []string{"core_file", "core_file_upload"} {
		var n int
		if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).
			Row().Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s 表应存在：n=%d err=%v", tbl, n, err)
		}
	}
}
