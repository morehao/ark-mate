package manager

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/vault"
)

// writeLegacyProviders 写一份「旧版本」的明文 providers.json。
func writeLegacyProviders(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readProvidersFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "providers.json"))
	if err != nil {
		t.Fatalf("读 providers.json: %v", err)
	}
	return string(b)
}

// TestProviderKeyMigratedToVaultOnLoad §5 启动迁移：旧明文档案 → 加密进 vault，
// 文件里明文消失、只留 key_ref；注入仍可用；再装载幂等（文件字节不变）。
func TestProviderKeyMigratedToVaultOnLoad(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "agents")
	const secret = "sk-legacy-secret-abc"
	writeLegacyProviders(t, dir,
		`{"default":{"id":"default","type":"openai","model":"gpt-4o","key":"`+secret+`"}}`)

	svc := agentTestSvc(t, dir)

	// 1) 明文从文件里消失，只留引用。
	raw := readProvidersFile(t, dir)
	if strings.Contains(raw, secret) {
		t.Fatalf("providers.json 仍有明文: %s", raw)
	}
	if !strings.Contains(raw, vault.RefPrefix+"providers/default") {
		t.Fatalf("providers.json 应只留 key_ref: %s", raw)
	}
	// 2) 密文落 <home>/vault/providers/default.enc，且不含明文。
	enc := filepath.Join(home, vault.DirName, "providers", "default.enc")
	eb, err := os.ReadFile(enc)
	if err != nil {
		t.Fatalf("密文未落盘: %v", err)
	}
	if strings.Contains(string(eb), secret) {
		t.Fatal("密文文件不应含明文")
	}
	// 3) 注入仍可用（内存里是解密后的明文），has_key 不变。
	if got := svc.providerEnvFor("opencode", "default"); len(got) != 1 || !strings.HasPrefix(got[0], "MATE_DEFAULT_") || !strings.HasSuffix(got[0], "_API_KEY="+secret) {
		t.Fatalf("迁移后注入不符: %v", got)
	}
	items, _ := svc.ListProviders(context.Background())
	if len(items) != 1 || !items[0].HasKey {
		t.Fatalf("has_key 应保持 true: %+v", items)
	}
	if err := svc.CredentialProblem(&AgentRec{ID: "a1", Provider: "default"}); err != nil {
		t.Fatalf("迁移后凭据应可用: %v", err)
	}

	// 4) 幂等：新 Service 再装载 → 文件字节不变（migrated=0 不再写回）。
	svc2 := agentTestSvc(t, dir)
	if got := readProvidersFile(t, dir); got != raw {
		t.Fatalf("二次装载应幂等: %s", got)
	}
	if got := svc2.providerEnvFor("opencode", "default"); len(got) != 1 || !strings.HasPrefix(got[0], "MATE_DEFAULT_") || !strings.HasSuffix(got[0], "_API_KEY="+secret) {
		t.Fatalf("重启后注入不符: %v", got)
	}
}

// TestProviderUpdateEncryptsAndClearDeletes 写面落盘即密文 + 引用；
// 空串清除 = 删 ref + 删密文文件（幂等）。
func TestProviderUpdateEncryptsAndClearDeletes(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "agents")
	svc := agentTestSvc(t, dir)

	key := "sk-live-secret-xyz"
	resp, err := svc.CreateProvider(context.Background(), providerReq("ds", &key))
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if !resp.HasKey {
		t.Fatal("应 has_key=true")
	}
	raw := readProvidersFile(t, dir)
	if strings.Contains(raw, key) {
		t.Fatalf("providers.json 不应含明文: %s", raw)
	}
	if !strings.Contains(raw, "providers/ds") {
		t.Fatalf("应写 key_ref: %s", raw)
	}
	enc := filepath.Join(home, vault.DirName, "providers", "ds.enc")
	if _, err := os.Stat(enc); err != nil {
		t.Fatalf("密文未落盘: %v", err)
	}
	if got := svc.providerEnvFor("opencode", "ds"); len(got) != 1 || !strings.HasPrefix(got[0], "MATE_DS_") || !strings.HasSuffix(got[0], "_API_KEY="+key) {
		t.Fatalf("注入不符: %v", got)
	}
	// 覆盖写同一档案：仍是 1 份密文、新值生效。
	key2 := "sk-live-secret-2"
	if _, err := svc.UpdateProvider(context.Background(), "ds", dtoprovider.ProviderPatchReq{APIKey: &key2}); err != nil {
		t.Fatal(err)
	}
	if got := svc.providerEnvFor("opencode", "ds"); len(got) != 1 || !strings.HasPrefix(got[0], "MATE_DS_") || !strings.HasSuffix(got[0], "_API_KEY="+key2) {
		t.Fatalf("覆盖后注入不符: %v", got)
	}

	// 清除：has_key=false、env nil、密文文件删除、文件里无 ref。
	clear := ""
	if _, err := svc.UpdateProvider(context.Background(), "ds", dtoprovider.ProviderPatchReq{APIKey: &clear}); err != nil {
		t.Fatalf("清除失败: %v", err)
	}
	if svc.providerEnvFor("opencode", "ds") != nil {
		t.Fatal("清除后不应注入")
	}
	if _, err := os.Stat(enc); !os.IsNotExist(err) {
		t.Fatalf("清除应删掉密文文件: %v", err)
	}
	items, _ := svc.ListProviders(context.Background())
	for _, it := range items {
		if it.ID == "ds" && it.HasKey {
			t.Fatalf("清除后 has_key 应为 false: %+v", it)
		}
	}
	if strings.Contains(readProvidersFile(t, dir), "providers/ds") {
		t.Fatal("清除后不应留 ref")
	}
	// 幂等：再清除一次不报错。
	if _, err := svc.UpdateProvider(context.Background(), "ds", dtoprovider.ProviderPatchReq{APIKey: &clear}); err != nil {
		t.Fatalf("重复清除应幂等: %v", err)
	}
}

// TestProviderCiphertextMismatchSurfacesKeyErr 换掉 vault.key（密文解不开）→
// 装载不 panic、不静默；providerEnvFor 不注入、CredentialProblem 报出可执行出路。
func TestProviderCiphertextMismatchSurfacesKeyErr(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "agents")
	svc := agentTestSvc(t, dir)
	key := "sk-will-be-orphaned"
	if _, err := svc.CreateProvider(context.Background(),
		providerReq("default", &key)); err != nil {
		t.Fatal(err)
	}

	// 换成另一把主密钥（模拟 vault.key 丢失/重建）= 已有密文永久不可解。
	other := make([]byte, 32)
	for i := range other {
		other[i] = byte(255 - i)
	}
	keyPath := filepath.Join(home, vault.KeyFileName)
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(other)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	svc2 := agentTestSvc(t, dir)
	if got := svc2.providerEnvFor("opencode", "default"); got != nil {
		t.Fatalf("解不开时不应注入 env: %v", got)
	}
	// `has_key` 的语义是**内存里解出了明文**（ADR-7）：密文解不开时它是 false，
	// 但原因必须走 problems（"没有密钥"与"密钥拿不到"是两件事，处置也不同）。
	items, _ := svc2.ListProviders(context.Background())
	if len(items) != 1 || items[0].HasKey {
		t.Fatalf("密文解不开时 has_key 应为 false: %+v", items)
	}
	if len(items[0].Problems) == 0 || !strings.Contains(strings.Join(items[0].Problems, " "), "密钥不可用") {
		t.Fatalf("problems 应说清原因: %+v", items[0].Problems)
	}
	err := svc2.CredentialProblem(&AgentRec{ID: "a1", Provider: "default"})
	if err == nil {
		t.Fatal("密文不可解应报错")
	}
	if !strings.Contains(err.Error(), "密钥不可用") || !strings.Contains(err.Error(), "vault.key") {
		t.Fatalf("错误应说明原因与出路: %v", err)
	}
}

// TestProviderInvalidIDRejected provider id 会进 vault 文件名 → 路径穿越必须被拒。
func TestProviderInvalidIDRejected(t *testing.T) {
	svc := agentTestSvc(t, filepath.Join(t.TempDir(), "agents"))
	key := "sk-x"
	for _, id := range []string{"../escape", "a/b", "a b", ".."} {
		if _, err := svc.CreateProvider(context.Background(),
			providerReq(id, &key)); err == nil {
			t.Fatalf("非法 provider id 应被拒绝: %q", id)
		}
		// PATCH 走同一条词法校验（id 会进 vault 文件名）。
		if _, err := svc.UpdateProvider(context.Background(), id,
			dtoprovider.ProviderPatchReq{APIKey: &key}); err == nil {
			t.Fatalf("非法 provider id 的 PATCH 应被拒绝: %q", id)
		}
	}
}

// TestProviderKeyRejectedWhenVaultUnavailable A4 的第二条入口（v13 的 fail-closed，ADR-8）：
// 没有可用 vault 时，**带密钥的写入被拒绝**，而不是"只留内存"或"明文落库"。
//
// v12 的行为是"密钥只在内存、载体里写不出 key_ref"（`providerBodyJSON` 的降级路径）——
// 而 v13 的两张表**没有能放明文的列**，那条降级路径在结构上不存在了。
// 所以这条用例的断言从"仍应可用"改成"拒绝写入 + 可执行出路"，**不是放宽实现**。
func TestProviderKeyRejectedWhenVaultUnavailable(t *testing.T) {
	ctx := context.Background()
	svc := NewService(v12ProviderRegistry(), nil) // 未 LoadAgentDefinitions → agentDir == ""（无 mate home）
	key := "sk-memory-only"
	_, err := svc.CreateProvider(ctx, providerReq("default", &key))
	if errCode(err) != "vault_unavailable" {
		t.Fatalf("无 vault 时带密钥的写入应 vault_unavailable，got %q（%v）", errCode(err), err)
	}
	// 出路必须可执行（用户要知道怎么让它能用）。
	if !strings.Contains(err.Error(), "MATE_VAULT_KEY") || !strings.Contains(err.Error(), "vault.key") {
		t.Fatalf("出路文案应指向 MATE_VAULT_KEY 与 vault.key: %v", err)
	}
	// 拒绝 = **一行内存都不留**（不是"半条档案"）。
	if items, _ := svc.ListProviders(ctx); len(items) != 0 {
		t.Fatalf("被拒的写入不该留下任何档案: %+v", items)
	}
	// 不带密钥的档案照常可建（"先建后补密钥"是合法用法）。
	if _, err := svc.CreateProvider(ctx, providerReq("default", nil)); err != nil {
		t.Fatalf("不带密钥时应该可以建: %v", err)
	}
	// PATCH 补密钥同样被拒（两条入口同一判据）。
	if _, err := svc.UpdateProvider(ctx, "default", dtoprovider.ProviderPatchReq{APIKey: &key}); errCode(err) != "vault_unavailable" {
		t.Fatalf("PATCH 补密钥也应 vault_unavailable，got %q（%v）", errCode(err), err)
	}
	// 重启 = 新 Service → 内存档案不复存在（没有任何落盘），且**没有任何明文残留**。
	svc2 := NewService(v12ProviderRegistry(), nil)
	if got := svc2.providerEnvFor("opencode", "default"); got != nil {
		t.Fatalf("无 vault 时不应有任何落盘: %v", got)
	}
}

func strPtr(s string) *string { return &s }

// TestProviderVaultEarlyCallDoesNotPoisonAnchor 2026-10-11 实测回归。
//
// 用户侧报的是「已存的密钥显示未设置、点保存也没有反应」。后端这一半的根因是**顺序**：
// 装配期的 runtime 同步（`EnableStore` → `SyncRuntime` → `providerForSync`）发生在 definitions
// 目录登记之前，那一次调用就会问一次 vault；旧实现把这次"锚点还不知道"缓存成
// `vaultTried = true` + `(nil, nil)`，于是**整个进程**此后都按"本机没有 vault"处理：
//   - 已存密文读不出来（v13 的 `has_key` 口径 = 内存里有明文 → 界面说「尚未设置密钥」，
//     而密文好好的：本机实测 `mate doctor` 报 ciphertext 在册、`vault:providers/yygu` 解得开）；
//   - 写面一律 `vault_unavailable`（密钥再也存不进去）。
//
// 判据分两半：① 锚点未知的调用**不许**落下"已试过"的缓存；② 登记锚点后必须真拿得到句柄
// 并指向 `<home>/vault`（旧实现在这里永远返回 nil）。
func TestProviderVaultEarlyCallDoesNotPoisonAnchor(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "agents")

	early := NewService(testutil.StubRegistry(), nil) // 装配早期：还没登记 definitions 目录
	st, err := early.providerVault()
	if st != nil || err != nil {
		t.Fatalf("锚点未知时应只回答「现在还不知道」= (nil,nil)，得到 st=%v err=%v", st, err)
	}
	if early.vaultTried {
		t.Fatal("锚点未知的调用不该落下 vaultTried（那会把「暂时不知道」变成整个进程的结论）")
	}

	early.SetAgentDir(dir) // = 真实装配里的 a.svc.SetAgentDir(cfg.AgentDir)
	st, err = early.providerVault()
	if err != nil || st == nil {
		t.Fatalf("登记锚点后必须拿得到 vault 句柄（旧实现会永远返回 nil）: st=%v err=%v", st, err)
	}
	if want := filepath.Join(home, vault.DirName); st.Dir() != want {
		t.Fatalf("vault 密文目录应为 %s，得到 %s", want, st.Dir())
	}
	// 顺带钉住幂等：再问一次拿到的是同一份（缓存仍然生效，不是每次重开）。
	if again, _ := early.providerVault(); again != st {
		t.Fatal("已装好的 vault 句柄应被缓存复用")
	}
}
