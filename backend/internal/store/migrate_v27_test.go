package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
)

// TestMigrationV27AddsWorkspaceDefaults v27 的判据。
//
// 为什么单独立一条而不是搭在"迁移能跑通"上：那一条断言的是**版本号前进**，而这一批真正要保证的是
// 三件**语义**——列存在、**不写就有值**（`NOT NULL DEFAULT ”`，于是存量行天然落成"没有默认值"，
// 不需要一次全表 UPDATE）、空串是合法取值而非"必须填"。
//
// 「本步一个字都不改 `sessions`」那一条**不在这里**：它是"这一批有没有顺手改快照"的判据，
// 落在 archtest 的 `TestWorkspaceDefaultsDoNotRewriteSessions` 上（扫迁移与写面的语句）。
func TestMigrationV27AddsWorkspaceDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := dbclient.Open(filepath.Join(t.TempDir(), "v27.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	st := New(db)
	defer func() { _ = st.Close() }()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// ① 两列存在即可（**不再断言"最新版本是 27"**：迁移是累积的，v28 之后这条断言会年年假红，
	//    而它想钉的从来不是版本号本身）。
	if v, err := st.SchemaVersion(ctx); err != nil || v < 27 {
		t.Fatalf("schema 版本应 ≥ 27，got %d (err=%v)", v, err)
	}

	// ② 两列存在、且**不写就有值**（`NOT NULL DEFAULT ''`）——新建一行只给旧列。
	if err := st.DB().Exec(`INSERT INTO workspaces (id, path, title, is_default, list_order, created_at, updated_at)
		VALUES ('wsp_t', '/tmp/t', '', 0, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`).Error; err != nil {
		t.Fatalf("插工作空间: %v", err)
	}
	var capabilityDefault, modeDefault string
	if err := st.DB().Raw(`SELECT default_capability_json, default_permission_mode FROM workspaces WHERE id = 'wsp_t'`).
		Row().Scan(&capabilityDefault, &modeDefault); err != nil {
		t.Fatalf("读新列（列不存在即在这里失败）: %v", err)
	}
	if capabilityDefault != "" || modeDefault != "" {
		t.Fatalf("两个默认值列应默认为空串（= 没有默认值），got (%q,%q)", capabilityDefault, modeDefault)
	}

	// ③ 空串是**合法取值**（不是"必须填"）：能写回、能读回。
	if err := st.DB().Exec(`UPDATE workspaces SET default_capability_json = ?, default_permission_mode = ? WHERE id = 'wsp_t'`,
		`{"skills":["sk_1"]}`, `workspace-write`).Error; err != nil {
		t.Fatalf("写默认值: %v", err)
	}
	if err := st.DB().Raw(`SELECT default_capability_json, default_permission_mode FROM workspaces WHERE id = 'wsp_t'`).
		Row().Scan(&capabilityDefault, &modeDefault); err != nil {
		t.Fatalf("读回默认值: %v", err)
	}
	if capabilityDefault != `{"skills":["sk_1"]}` || modeDefault != `workspace-write` {
		t.Fatalf("默认值读回不符：(%q,%q)", capabilityDefault, modeDefault)
	}
}
