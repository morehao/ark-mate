package manager

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/vault"
	"github.com/morehao/ark-mate/pkg/runtime"
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
	if got := svc.providerEnvFor("default"); len(got) != 1 || got[0] != "OPENAI_API_KEY="+secret {
		t.Fatalf("迁移后注入不符: %v", got)
	}
	items, _ := svc.ListProviders()
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
	if got := svc2.providerEnvFor("default"); len(got) != 1 || got[0] != "OPENAI_API_KEY="+secret {
		t.Fatalf("重启后注入不符: %v", got)
	}
}

// TestProviderUpdateEncryptsAndClearDeletes UpdateProvider 落盘即密文 + 引用；
// 空串清除 = 删 ref + 删密文文件（幂等）。
func TestProviderUpdateEncryptsAndClearDeletes(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "agents")
	svc := agentTestSvc(t, dir)

	key := "sk-live-secret-xyz"
	typ := "deepseek"
	resp, err := svc.UpdateProvider(context.Background(), "ds", dtoprovider.ProviderPatchReq{Type: &typ, Key: &key})
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
	if got := svc.providerEnvFor("ds"); len(got) != 1 || got[0] != "DEEPSEEK_API_KEY="+key {
		t.Fatalf("注入不符: %v", got)
	}
	// 覆盖写同一档案：仍是 1 份密文、新值生效。
	key2 := "sk-live-secret-2"
	if _, err := svc.UpdateProvider(context.Background(), "ds", dtoprovider.ProviderPatchReq{Key: &key2}); err != nil {
		t.Fatal(err)
	}
	if got := svc.providerEnvFor("ds"); len(got) != 1 || got[0] != "DEEPSEEK_API_KEY="+key2 {
		t.Fatalf("覆盖后注入不符: %v", got)
	}

	// 清除：has_key=false、env nil、密文文件删除、文件里无 ref。
	clear := ""
	if _, err := svc.UpdateProvider(context.Background(), "ds", dtoprovider.ProviderPatchReq{Key: &clear}); err != nil {
		t.Fatalf("清除失败: %v", err)
	}
	if svc.providerEnvFor("ds") != nil {
		t.Fatal("清除后不应注入")
	}
	if _, err := os.Stat(enc); !os.IsNotExist(err) {
		t.Fatalf("清除应删掉密文文件: %v", err)
	}
	items, _ := svc.ListProviders()
	for _, it := range items {
		if it.ID == "ds" && it.HasKey {
			t.Fatalf("清除后 has_key 应为 false: %+v", it)
		}
	}
	if strings.Contains(readProvidersFile(t, dir), "providers/ds") {
		t.Fatal("清除后不应留 ref")
	}
	// 幂等：再清除一次不报错。
	if _, err := svc.UpdateProvider(context.Background(), "ds", dtoprovider.ProviderPatchReq{Key: &clear}); err != nil {
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
	if _, err := svc.UpdateProvider(context.Background(), "default",
		dtoprovider.ProviderPatchReq{Type: strPtr("openai"), Key: &key}); err != nil {
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
	if got := svc2.providerEnvFor("default"); got != nil {
		t.Fatalf("解不开时不应注入 env: %v", got)
	}
	// has_key 仍为 true（档案确实配了密钥），但诊断要说明原因。
	items, _ := svc2.ListProviders()
	if len(items) != 1 || !items[0].HasKey {
		t.Fatalf("has_key 应仍为 true: %+v", items)
	}
	err := svc2.CredentialProblem(&AgentRec{ID: "a1", Provider: "default"})
	if err == nil {
		t.Fatal("密文不可解应报错")
	}
	if !strings.Contains(err.Error(), "无法解密") || !strings.Contains(err.Error(), "vault.key") {
		t.Fatalf("错误应说明原因与出路: %v", err)
	}
}

// TestProviderInvalidIDRejected provider id 会进 vault 文件名 → 路径穿越必须被拒。
func TestProviderInvalidIDRejected(t *testing.T) {
	svc := agentTestSvc(t, filepath.Join(t.TempDir(), "agents"))
	key := "sk-x"
	for _, id := range []string{"../escape", "a/b", "a b", ".."} {
		if _, err := svc.UpdateProvider(context.Background(), id,
			dtoprovider.ProviderPatchReq{Key: &key}); err == nil {
			t.Fatalf("非法 provider id 应被拒绝: %q", id)
		}
	}
}

// TestProviderPlaintextSurvivesWhenVaultUnavailable vault 不可用（agentDir 为空 = 无 mate home）
// 时：不清空密钥、不写盘（纯内存），且不 pretend 加密成功。
func TestProviderPlaintextSurvivesWhenVaultUnavailable(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), nil) // 未 LoadAgentDefinitions → agentDir == ""
	key := "sk-memory-only"
	resp, err := svc.UpdateProvider(context.Background(), "default",
		dtoprovider.ProviderPatchReq{Type: strPtr("openai"), Key: &key})
	if err != nil {
		t.Fatalf("无 vault 时仍应可用（仅内存）: %v", err)
	}
	if !resp.HasKey {
		t.Fatal("应 has_key=true")
	}
	if got := svc.providerEnvFor("default"); len(got) != 1 || got[0] != "OPENAI_API_KEY="+key {
		t.Fatalf("内存注入不符: %v", got)
	}
	// 重启 = 新 Service → 内存档案不复存在（没有任何落盘）。
	svc2 := NewService(runtime.NewRegistry(), nil)
	if got := svc2.providerEnvFor("default"); got != nil {
		t.Fatalf("无 vault 时不应落盘: %v", got)
	}
}

func strPtr(s string) *string { return &s }
