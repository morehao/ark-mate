package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/db"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
)

// writeLegacyVaultKey 在 vaultHome 下写 0600 的 keyfile（内容是 base64 的 32 字节主密钥）。
func writeLegacyVaultKey(t *testing.T, vaultHome string, key []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(vaultHome, legacyVaultKeyFile),
		[]byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatalf("写 keyfile: %v", err)
	}
}

// writeLegacyCiphertext 按**当年那一版**的格式造一个密文文件：base64(nonce‖AES-GCM(明文))，
// AAD = `kind + "\x00" + "vault:" + ref`（**带前缀的完整引用**：旧实现的 `Put` 就是这么算的，
// 按去前缀的路径算会一条都解不开——真机实测踩过），内容 base64 + 换行，落
// `<vaultHome>/vault/<ref>.enc`。
//
// 夹具**自己造密文**（不 import `internal/vault`）：那个包随 v20 删除，而这条用例要留在仓库里
// 继续证明"从旧版本升上来的密文能被搬走"。
func writeLegacyCiphertext(t *testing.T, vaultHome, ref, kind string, key []byte, plaintext string) {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	sealed := aead.Seal(nil, nonce, []byte(plaintext), []byte(kind+"\x00"+legacyVaultRefPrefix+ref))
	blob := base64.StdEncoding.EncodeToString(append(nonce, sealed...)) + "\n"
	path := filepath.Join(vaultHome, legacyVaultDir, filepath.FromSlash(ref)+legacyVaultFileExt)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("建密文目录: %v", err)
	}
	if err := os.WriteFile(path, []byte(blob), 0o600); err != nil {
		t.Fatalf("写密文: %v", err)
	}
}

// newLegacyVaultStore 起一个已迁到最新版的库（搬运要用 v20 的列与表）。
func newLegacyVaultStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	db, err := dbclient.Open(filepath.Join(t.TempDir(), "v20.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	return s, ctx
}

// TestImportLegacyVaultMovesCiphertextsIntoDB 是搬运的主路径：provider 密钥进 `providers.key`、
// MCP 密钥进 `mcp_secrets`（引用换成 `store:` 前缀），**密文一个字节都不删**，且重跑幂等。
//
// 三段判据对应的失效方式都是静默的：不搬 → 用户升级后所有密钥"消失"（`has_key` 变 false、
// 底座拨号 401）；搬了却删密文 → 一步不可逆的丢失（库还没写成功就没了回退依据）；
// 不幂等 → 每次启动都覆盖用户后来在面板上改过的值。
func TestImportLegacyVaultMovesCiphertextsIntoDB(t *testing.T) {
	s, ctx := newLegacyVaultStore(t)
	home := t.TempDir()
	key := make([]byte, legacyVaultKeyLen)
	for i := range key {
		key[i] = byte(i + 1)
	}
	writeLegacyVaultKey(t, home, key)
	writeLegacyCiphertext(t, home, "providers/p1", legacyKindProvider, key, "sk-provider-legacy")
	writeLegacyCiphertext(t, home, "default/github", legacyKindMcpSecret, key, "ghp-legacy")

	// 供应商行要存在（v19 起它是列化的表；搬运只负责写 key）。
	now := model.NowTS()
	if err := s.db.Exec(`INSERT INTO providers (id, name, protocol, base_url, note, revision, created_at, updated_at)
		VALUES (?, ?, 'openai', 'https://api.example.com/v1', '', 1, ?, ?)`, "p1", "供应商一", now, now).Error; err != nil {
		t.Fatalf("写供应商行: %v", err)
	}

	move, err := s.ImportLegacyVault(ctx, home)
	if err != nil {
		t.Fatalf("搬运: %v", err)
	}
	if move.Ciphertexts != 2 || move.ProviderKeys != 1 || move.McpSecrets != 1 || len(move.Problems) != 0 {
		t.Fatalf("搬运结果不对：%+v", move)
	}
	if got, ok, err := s.ProviderKey(ctx, "p1"); err != nil || !ok || got != "sk-provider-legacy" {
		t.Fatalf("providers.key 没搬对：%q ok=%v err=%v", got, ok, err)
	}
	if got, ok, err := s.McpSecret(ctx, "store:default/github"); err != nil || !ok || got != "ghp-legacy" {
		t.Fatalf("mcp_secrets 没搬对：%q ok=%v err=%v", got, ok, err)
	}
	// 密文必须还在（删除是用户的显式动作，见 legacy_vault.go 的头注释）。
	for _, ref := range []string{"providers/p1", "default/github"} {
		if _, err := os.Stat(filepath.Join(home, legacyVaultDir, filepath.FromSlash(ref)+legacyVaultFileExt)); err != nil {
			t.Fatalf("密文 %s 不见了（搬运不该删任何东西）: %v", ref, err)
		}
	}

	// 重跑：两条都应幂等跳过，且**不覆盖**用户后来改过的值。
	if _, err := s.SetProviderKey(ctx, "p1", "sk-user-edited"); err != nil {
		t.Fatalf("用户改写: %v", err)
	}
	again, err := s.ImportLegacyVault(ctx, home)
	if err != nil {
		t.Fatalf("重跑: %v", err)
	}
	if again.ProviderKeys != 0 || again.McpSecrets != 0 || again.AlreadyMoved != 2 || len(again.Problems) != 0 {
		t.Fatalf("重跑不幂等：%+v", again)
	}
	if got, _, _ := s.ProviderKey(ctx, "p1"); got != "sk-user-edited" {
		t.Fatalf("重跑覆盖了用户改过的值：%q", got)
	}
}

// TestImportLegacyVaultReportsProblemsWithoutLosingTheRest 钉"单条失败不拖垮整批、也不静默丢"：
// 一个库里的行不存在、一个密文解不开（主密钥不对），同批里那条好的一定要搬进去。
//
// 失败原因**只记引用不记值**——诊断面泄露密钥就等于把 v20 的一条不变量拆了。
func TestImportLegacyVaultReportsProblemsWithoutLosingTheRest(t *testing.T) {
	s, ctx := newLegacyVaultStore(t)
	home := t.TempDir()
	key := make([]byte, legacyVaultKeyLen)
	for i := range key {
		key[i] = byte(0xA0 + i)
	}
	writeLegacyVaultKey(t, home, key)
	writeLegacyCiphertext(t, home, "providers/missing-row", legacyKindProvider, key, "sk-orphan")
	writeLegacyCiphertext(t, home, "default/ok", legacyKindMcpSecret, key, "ok-value")
	writeLegacyCiphertext(t, home, "default/other-key", legacyKindMcpSecret, bytesRepeat(0x11, legacyVaultKeyLen), "never-read")

	move, err := s.ImportLegacyVault(ctx, home)
	if err != nil {
		t.Fatalf("搬运: %v", err)
	}
	if move.Ciphertexts != 3 || move.McpSecrets != 1 || move.ProviderKeys != 0 {
		t.Fatalf("可搬的那条没搬：%+v", move)
	}
	if len(move.Problems) != 2 {
		t.Fatalf("应有 2 条问题（行不存在 + 解不开），实际 %d：%+v", len(move.Problems), move.Problems)
	}
	refs := map[string]string{}
	for _, p := range move.Problems {
		refs[p.Ref] = p.Reason
	}
	if _, ok := refs["providers/missing-row"]; !ok {
		t.Fatalf("缺 providers 行的那条没报出来：%+v", move.Problems)
	}
	if _, ok := refs["default/other-key"]; !ok {
		t.Fatalf("解不开的那条没报出来：%+v", move.Problems)
	}
	for _, p := range move.Problems {
		if strings.Contains(p.Reason, "never-read") || strings.Contains(p.Reason, "ok-value") {
			t.Fatalf("问题原因里带了明文值（诊断面泄露密钥）：%+v", p)
		}
	}
	if got, ok, _ := s.McpSecret(ctx, "store:default/ok"); !ok || got != "ok-value" {
		t.Fatalf("同批里那条好的没搬进去：%q ok=%v", got, ok)
	}
}

// TestImportLegacyVaultWithoutKeyReportsInsteadOfFailing 钉"主密钥不可用 = 待处理，不是启动失败"：
// 一条密钥都不搬、但**必须报出来**（否则用户升级后只会看到"我的 key 都没了"）。
func TestImportLegacyVaultWithoutKeyReportsInsteadOfFailing(t *testing.T) {
	s, ctx := newLegacyVaultStore(t)
	home := t.TempDir()
	writeLegacyCiphertext(t, home, "default/x", legacyKindMcpSecret, bytesRepeat(0x22, legacyVaultKeyLen), "v")

	move, err := s.ImportLegacyVault(ctx, home)
	if err != nil {
		t.Fatalf("缺主密钥不该报错（它是待处理项）：%v", err)
	}
	if move.Ciphertexts != 1 || move.McpSecrets != 0 || len(move.Problems) != 1 || move.Problems[0].Ref != "" {
		t.Fatalf("缺主密钥的结果不对：%+v", move)
	}
	if !strings.Contains(move.Problems[0].Reason, legacyVaultKeyFile) {
		t.Fatalf("报的原因要指出缺的是哪个文件：%+v", move.Problems[0])
	}

	// 没有旧 vault 的机器：空结果、零问题（装配期每次启动都会调它）。
	if empty, err := s.ImportLegacyVault(ctx, t.TempDir()); err != nil || !empty.Empty() {
		t.Fatalf("没有旧 vault 时应是空结果：%+v err=%v", empty, err)
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// TestImportLegacyVaultDecryptsFrozenOldArtifact 用一份**由旧实现亲自产出**的密文当判据。
//
// 为什么必须有这条：上面那些用例是"夹具自己用 AES-GCM 造密文"，一旦夹具与实现共享同一个误解
// （2026-10-14 真机实测就发生过：AAD 少算了 `vault:` 前缀，三条用例一起绿），它们就一起失去
// 判据力。这条把密文与主密钥**冻结成字面量**（由 v19 的 `internal/vault.Encrypt` 在删除前产出，
// 明文是假值 `sk-frozen-fixture`），于是任何对 AAD / kind / 编码 / 前缀的改动都会当场变红。
func TestImportLegacyVaultDecryptsFrozenOldArtifact(t *testing.T) {
	const (
		frozenKeyHex  = "5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a"
		frozenBlob    = "DwAlkZ1XRrU6YXQz2GHzKfcNRnzs4Qf3of/h2gX5wrIrG6xJwAIxvNZNYF/c"
		frozenRef     = "vault:providers/frozen"
		frozenSecret  = "sk-frozen-fixture"
		frozenKeyFile = "vault.key"
	)
	_ = frozenRef
	s, ctx := newLegacyVaultStore(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, frozenKeyFile), []byte(frozenKeyHex), 0o600); err != nil {
		t.Fatalf("写 keyfile: %v", err)
	}
	dir := filepath.Join(home, legacyVaultDir, legacyProviderNS)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frozen"+legacyVaultFileExt), []byte(frozenBlob+"\n"), 0o600); err != nil {
		t.Fatalf("写冻结密文: %v", err)
	}
	now := model.NowTS()
	if err := s.db.Exec(`INSERT INTO providers (id, name, protocol, base_url, note, revision, created_at, updated_at)
		VALUES ('frozen', '冻结夹具', 'openai', '', '', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("写供应商行: %v", err)
	}

	move, err := s.ImportLegacyVault(ctx, home)
	if err != nil {
		t.Fatalf("搬运: %v", err)
	}
	if len(move.Problems) != 0 || move.ProviderKeys != 1 {
		t.Fatalf("旧实现的文物没被搬走（格式漂了？）：%+v", move)
	}
	if got, ok, _ := s.ProviderKey(ctx, "frozen"); !ok || got != frozenSecret {
		t.Fatalf("解出来的值与旧实现写入的不一致：%q ok=%v", got, ok)
	}
}
