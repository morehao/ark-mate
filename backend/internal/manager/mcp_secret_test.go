package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/vault"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// TestSecretRefLexiconMatchesVault §13.3 M1 的一致性守卫：能力数据面（pkg/capability）与
// vault（internal/vault）对"什么算合法引用"必须同判——同一个引用不能出现"数据面放行、vault 拒绝"。
// 放在 manager 里测是因为只有这里同时 import 得动两边（pkg/ 不得 import internal/）。
func TestSecretRefLexiconMatchesVault(t *testing.T) {
	samples := []string{
		"vault:providers/default", "vault:mcp/fs-token", "vault:a/B", "vault:a.b-c_d/e",
		"vault:", "vault:onlyns", "vault:ns/", "vault:/name", "vault:../x", "vault:ns/..",
		"vault:ns/na me", "vault:" + strings.Repeat("a", 65) + "/x",
	}
	for _, ref := range samples {
		_, vaultErr := vault.ValidateRef(ref)
		capErr := capability.ValidateSecretRef(ref)
		if (vaultErr == nil) != (capErr == nil) {
			t.Fatalf("引用 %q 判定不一致：vault=%v capability=%v", ref, vaultErr, capErr)
		}
	}
}

// TestMcpSecretResolution §13.3 M1/M6：定义文件里只有引用，注入时才解析成明文；
// vault 密文缺失/环境变量未设 → **不注入该 server**（不静默塞空密钥）。
func TestMcpSecretResolution(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "agents")
	svc := agentTestSvc(t, dir) // 经 LoadAgentDefinitions 设定 agentDir → vault 落在 <home>

	st, err := vault.Open(home)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	const token = "sk-mcp-secret-xyz"
	if err := st.Put("vault:mcp/fs-token", vault.KindMcpSecret, []byte(token)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FS_AUTH_HEADER", "Bearer from-env")

	if err := svc.caps.RegisterMcpServer(&capability.McpServer{
		ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
		Env:           map[string]string{"FS_ROOT": "/tmp/root"},
		SecretEnv:     map[string]string{"FS_TOKEN": "vault:mcp/fs-token"},
		Headers:       map[string]string{"X-Client": "forge"},
		SecretHeaders: map[string]string{"Authorization": "env:FS_AUTH_HEADER"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{
		ID: "broken", Name: "broken", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
		SecretEnv: map[string]string{"FS_TOKEN": "vault:mcp/not-there"},
	}); err != nil {
		t.Fatal(err)
	}

	// 注入：引用解析成明文，且明文/密钥两路合并到同一字典。
	got := svc.McpServersFor(&AgentRec{ID: "a"}, "stub", nil)
	if len(got) != 1 || got[0].Name != "fs" {
		t.Fatalf("解析失败的 server 不该注入，应只剩 fs: %+v", got)
	}
	if got[0].Env["FS_TOKEN"] != token || got[0].Env["FS_ROOT"] != "/tmp/root" {
		t.Fatalf("env 注入不符: %+v", got[0].Env)
	}
	if got[0].Headers["Authorization"] != "Bearer from-env" || got[0].Headers["X-Client"] != "forge" {
		t.Fatalf("header 注入不符: %+v", got[0].Headers)
	}

	// 读模型：明文键给键名，密钥侧**只给键名**（连引用都不回），并标出解析不了的键。
	list, err := svc.ListMcpServers()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]struct {
		env, secEnv, secHdr, unresolved []string
	}{}
	for _, m := range list {
		byID[m.ID] = struct {
			env, secEnv, secHdr, unresolved []string
		}{m.EnvKeys, m.SecretEnvKeys, m.SecretHeaderKeys, m.UnresolvedSecretKeys}
	}
	fs := byID["fs"]
	if len(fs.env) != 1 || fs.env[0] != "FS_ROOT" {
		t.Fatalf("env_keys 不符: %+v", fs.env)
	}
	if len(fs.secEnv) != 1 || fs.secEnv[0] != "FS_TOKEN" {
		t.Fatalf("secret_env_keys 不符: %+v", fs.secEnv)
	}
	if len(fs.secHdr) != 1 || fs.secHdr[0] != "Authorization" {
		t.Fatalf("secret_header_keys 不符: %+v", fs.secHdr)
	}
	if len(fs.unresolved) != 0 {
		t.Fatalf("fs 的引用都能解析，不该有 unresolved: %+v", fs.unresolved)
	}
	if broken := byID["broken"]; len(broken.unresolved) != 1 || broken.unresolved[0] != "FS_TOKEN" {
		t.Fatalf("缺密文的引用应报 unresolved: %+v", broken.unresolved)
	}

	// 读模型里绝不能出现密钥值/引用体（守住"只回 key"）：序列化整个响应体来查。
	raw := dumpMcpServers(t, svc)
	for _, leak := range []string{token, "vault:mcp/fs-token", "Bearer from-env", "from-env"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("读模型泄漏 %q：%s", leak, raw)
		}
	}
}

// dumpMcpServers 把 MCP 列表响应序列化成 JSON（用于"读模型不得泄漏值/引用"的字符串断言）。
func dumpMcpServers(t *testing.T, svc *Service) string {
	t.Helper()
	list, err := svc.ListMcpServers()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestMcpSecretResolutionFailures 三类解析失败都给得出原因（env 未设 / vault 不可用 / 引用非法），
// 且都不注入——不静默塞空密钥让 server 带着空凭证跑。
func TestMcpSecretResolutionFailures(t *testing.T) {
	// 1) env 引用未设。
	svc := agentTestSvc(t, filepath.Join(t.TempDir(), "agents"))
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{
		ID: "s1", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
		SecretEnv: map[string]string{"TOKEN": "env:FORGE_TEST_UNSET_VAR"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := svc.McpServersFor(&AgentRec{ID: "a"}, "stub", nil); len(got) != 0 {
		t.Fatalf("env 未设时不该注入: %+v", got)
	}
	if keys := svc.unresolvedSecretKeys(capability.McpServer{SecretEnv: map[string]string{"TOKEN": "env:FORGE_TEST_UNSET_VAR"}}); len(keys) != 1 {
		t.Fatalf("应报 unresolved: %+v", keys)
	}

	// 2) vault 不可用（agentDir 为空 = 没有 ~/.forge）：明确指出"无 vault"，而不是当成"密钥为空"。
	noVault := NewService(runtime.NewRegistry(runtime.Spec{ID: "stub"}), nil)
	if err := noVault.caps.RegisterMcpServer(&capability.McpServer{
		ID: "s2", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
		SecretEnv: map[string]string{"TOKEN": "vault:mcp/x"},
	}); err != nil {
		t.Fatal(err)
	}
	_, _, problems := noVault.resolveMcpSecrets(capability.McpServer{
		SecretEnv: map[string]string{"TOKEN": "vault:mcp/x"},
	})
	if len(problems) != 1 || !strings.Contains(problems[0], "vault 不可用") {
		t.Fatalf("应报 vault 不可用: %+v", problems)
	}
	if len(noVault.unresolvedSecretKeys(capability.McpServer{SecretEnv: map[string]string{"TOKEN": "vault:mcp/x"}})) != 1 {
		t.Fatal("vault 不可用时该键应报 unresolved")
	}

	// 3) 失败原因里不得含密钥值（日志不该成为泄漏面）。
	home := t.TempDir()
	dir := filepath.Join(home, "agents")
	svc3 := agentTestSvc(t, dir)
	const leaked = "sk-should-never-appear"
	if err := os.Setenv("FORGE_TEST_WRONG_KIND", leaked); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("FORGE_TEST_WRONG_KIND") })
	st, err := vault.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	// 用错 kind 存密文 → 解密失败（AAD 带 kind）。
	if err := st.Put("vault:mcp/wrong", vault.KindProviderKey, []byte(leaked)); err != nil {
		t.Fatal(err)
	}
	_, _, problems = svc3.resolveMcpSecrets(capability.McpServer{
		SecretEnv: map[string]string{"TOKEN": "vault:mcp/wrong"},
	})
	if len(problems) != 1 {
		t.Fatalf("kind 不符应解析失败: %+v", problems)
	}
	if strings.Contains(problems[0], leaked) {
		t.Fatalf("失败原因泄漏了密钥值: %v", problems[0])
	}
	if !strings.Contains(problems[0], "TOKEN") || !strings.Contains(problems[0], "vault:mcp/wrong") {
		t.Fatalf("失败原因应含键名与引用: %v", problems[0])
	}
}
