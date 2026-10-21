package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// TestMigrationV20AddsProviderKeyAndMcpSecretsAndRewritesVaultRefs v19 → v20：加列、建表、
// 以及把既有 MCP 定义体里的 `vault:` 引用前缀**就地**改写成 `store:`（只改前缀）。
//
// 三段判据都要钉，因为三种失效都是静默的：少一列 → "有没有密钥"没有判据；少一张表 → 密钥只能
// 塞进 body（于是审计与导入导出载体里躺着明文）；不重写前缀 → 定义照常读得出来，只是运行时
// 再也解析不到那把钥匙（用户看到的是"这个 MCP 连不上"而不是"前缀失效了"）。
func TestMigrationV20AddsProviderKeyAndMcpSecretsAndRewritesVaultRefs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v19.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	defer func() { _ = s.Close() }()

	// 造一个真实的 v19 库：一个供应商（v13 的列集合，**没有** key 列）+ 一个 MCP 定义体，
	// 里面两个 `vault:` 引用（env 一个、header 一个）与一个无关的 `env:` 引用（不该被动到）。
	//
	// 这里必须走 `migrateUpTo` 而不是 `applySchemaUpTo`：v13 的"列化重建"是 **Go apply 步**
	// （不是 DDL），只重放建表语句会造出一个"供应商表还是载体形态"的假 v19 底座——那正是
	// `migrateUpTo` 注释里说的"线上不会出现的中间态"。
	migrateUpTo(t, s, 19)
	now := model.NowTS()
	if err := s.db.Exec(`INSERT INTO providers (id, name, protocol, base_url, note, revision, created_at, updated_at)
		VALUES (?, ?, ?, ?, '', 1, ?, ?)`, "p1", "供应商一", "openai", "https://api.example.com/v1", now, now).Error; err != nil {
		t.Fatalf("写 v19 供应商行: %v", err)
	}
	legacyBody := `{"transport":{"kind":"stdio"},"env":{"GITHUB_TOKEN":"vault:default/github"},` +
		`"headers":{"X-Api-Key":"vault:default/x"},"cwd_env":{"K":"env:K"}}`
	if err := s.db.Exec(`INSERT INTO mcp_servers (id, body, revision, updated_at) VALUES (?, ?, 1, ?)`,
		"mcp-1", legacyBody, now).Error; err != nil {
		t.Fatalf("写 v19 MCP 定义: %v", err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("v19 → v20 升级失败: %v", err)
	}

	// ① `providers.key` 列在，且存量行是空串（= 没有密钥，不是 NULL）。
	var n int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_table_info('providers') WHERE name = 'key'`).
		Row().Scan(&n); err != nil || n != 1 {
		t.Fatalf("v20 应给 providers 加上 key 列: n=%d err=%v", n, err)
	}
	keys, ok, err := s.ProviderKey(ctx, "p1")
	if err != nil || !ok || keys != "" {
		t.Fatalf("存量供应商的密钥应为空串: key=%q ok=%v err=%v", keys, ok, err)
	}

	// ② `mcp_secrets` 表在（空的）。
	if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='mcp_secrets'`).
		Row().Scan(&n); err != nil || n != 1 {
		t.Fatalf("v20 应建出 mcp_secrets 表: n=%d err=%v", n, err)
	}

	// ③ 前缀改写：两个 `vault:` 变 `store:`，`env:` 引用与其余正文一字不动。
	row, found, err := s.GetMcpServerDefinition(ctx, "mcp-1")
	if err != nil || !found {
		t.Fatalf("读回 MCP 定义: found=%v err=%v", found, err)
	}
	if strings.Contains(row.Body, "vault:") {
		t.Fatalf("定义体里不该再有 vault: 前缀: %s", row.Body)
	}
	if !strings.Contains(row.Body, `"GITHUB_TOKEN":"store:default/github"`) ||
		!strings.Contains(row.Body, `"X-Api-Key":"store:default/x"`) {
		t.Fatalf("两个引用都应改成 store: 前缀: %s", row.Body)
	}
	if !strings.Contains(row.Body, `"K":"env:K"`) {
		t.Fatalf("`env:` 引用不该被这次改写动到: %s", row.Body)
	}
}

// TestCredentialWritePaths 密钥写路径：DB 是唯一权威，"有没有密钥"只有一个判据。
//
// 两条不变量：① 密钥写入 **bump revision**（否则"这个供应商改过没有"在设置页会看错），
// 且 `providers.key` 不参与档案 digest；② MCP 密钥覆盖时 revision 单调、删除后读回 false。
func TestCredentialWritePaths(t *testing.T) {
	ctx := context.Background()
	s := openTestDB(t)

	if ok, err := s.UpsertProviderDefinition(ctx, ProviderColumns{
		ID: "p1", Protocol: "openai", BaseURL: "https://api.example.com/v1",
	}, 0, "rest"); err != nil || !ok {
		t.Fatalf("写供应商: ok=%v err=%v", ok, err)
	}
	before, found, err := s.GetProviderDefinition(ctx, "p1")
	if err != nil || !found {
		t.Fatalf("读回供应商: found=%v err=%v", found, err)
	}

	if ok, err := s.SetProviderKey(ctx, "p1", "sk-plain-1"); err != nil || !ok {
		t.Fatalf("写密钥: ok=%v err=%v", ok, err)
	}
	if key, ok, err := s.ProviderKey(ctx, "p1"); err != nil || !ok || key != "sk-plain-1" {
		t.Fatalf("读回密钥: key=%q ok=%v err=%v", key, ok, err)
	}
	after, _, err := s.GetProviderDefinition(ctx, "p1")
	if err != nil {
		t.Fatalf("再读供应商: %v", err)
	}
	if after.Revision != before.Revision+1 {
		t.Fatalf("密钥写入必须 bump revision: before=%d after=%d", before.Revision, after.Revision)
	}
	// 密钥**不是**档案的一部分：行里的档案列一个字没动，而 digest 的输入是 `ProviderColumns`
	// （它刻意没有 Key 字段——所以"密钥不进 digest"这条不变量在这里是可执行的：档案列等价 ⇒
	// digest 等价）。
	beforeCols := ProviderColumns{ID: before.ID, Name: before.Name, Protocol: before.Protocol, BaseURL: before.BaseURL, Note: before.Note}
	afterCols := ProviderColumns{ID: after.ID, Name: after.Name, Protocol: after.Protocol, BaseURL: after.BaseURL, Note: after.Note}
	if providerColumnsDigest(beforeCols) != providerColumnsDigest(afterCols) {
		t.Fatalf("密钥写入不该动到档案列: before=%s after=%s", providerColumnsDigest(beforeCols), providerColumnsDigest(afterCols))
	}

	// 清除也是"一次写"：值变空、revision 继续涨，行仍在。
	if ok, err := s.SetProviderKey(ctx, "p1", ""); err != nil || !ok {
		t.Fatalf("清密钥: ok=%v err=%v", ok, err)
	}
	if key, ok, err := s.ProviderKey(ctx, "p1"); err != nil || !ok || key != "" {
		t.Fatalf("清除后应为空串而**不是**行不存在: key=%q ok=%v err=%v", key, ok, err)
	}
	if ok, err := s.SetProviderKey(ctx, "nope", "x"); err != nil || ok {
		t.Fatalf("给不存在的供应商写密钥应报「没命中」: ok=%v err=%v", ok, err)
	}

	// MCP：写入 → 覆盖（revision 单调）→ 列表 → 删除。
	if err := s.SetMcpSecret(ctx, "store:default/github", "tok-1"); err != nil {
		t.Fatalf("写 MCP 密钥: %v", err)
	}
	if v, ok, err := s.McpSecret(ctx, "store:default/github"); err != nil || !ok || v != "tok-1" {
		t.Fatalf("读回 MCP 密钥: v=%q ok=%v err=%v", v, ok, err)
	}
	if err := s.SetMcpSecret(ctx, "store:default/github", "tok-2"); err != nil {
		t.Fatalf("覆盖 MCP 密钥: %v", err)
	}
	rows, err := s.ListMcpSecrets(ctx)
	var rev int64
	if err != nil || len(rows) != 1 {
		t.Fatalf("列表应只有一条: n=%d err=%v", len(rows), err)
	}
	rev = rows[0].Revision
	if rows[0].Value != "tok-2" || rev != 2 {
		t.Fatalf("覆盖后值/revision 不符: %+v", rows[0])
	}
	if n, err := s.CountMcpSecrets(ctx); err != nil || n != 1 {
		t.Fatalf("计数应为 1: n=%d err=%v", n, err)
	}
	if ok, err := s.DeleteMcpSecret(ctx, "store:default/github"); err != nil || !ok {
		t.Fatalf("删除: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.McpSecret(ctx, "store:default/github"); err != nil || ok {
		t.Fatalf("删除后应读不到: ok=%v err=%v", ok, err)
	}
	if ok, err := s.DeleteMcpSecret(ctx, "store:default/github"); err != nil || ok {
		t.Fatalf("重复删除应报没删到: ok=%v err=%v", ok, err)
	}
}
