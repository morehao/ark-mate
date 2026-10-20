package capability

import (
	"strings"
	"testing"
)

// TestValidateSecretRefLexicon 引用词法：只认 store:/env: 两种，明文一律拒（M1 的核心判据）。
//
// v20 起平台托管的那个前缀是 `store:`（指向库里的 `mcp_secrets`）；文件中出现的旧前缀见文件末尾
// 的翻译点用例——**本文件是全仓唯一允许写出旧前缀的测试**（守卫白名单里的理由就是这么写的：
// 判据必须写出它才能证明"认得出、翻得对、并且告警"）。
func TestValidateSecretRefLexicon(t *testing.T) {
	valid := []struct{ ref, kind string }{
		{"store:providers/default", "store"},
		{"store:mcp/fs-token", "store"},
		{"store:a/B", "store"},
		{"env:FS_TOKEN", "env"},
		{"env:_x9", "env"},
		{"  store:mcp/x  ", "store"}, // 前后空白容忍（TrimSpace）
	}
	for _, c := range valid {
		if err := ValidateSecretRef(c.ref); err != nil {
			t.Fatalf("%q 应合法: %v", c.ref, err)
		}
		if got := SecretRefKind(c.ref); got != c.kind {
			t.Fatalf("%q kind=%q want %q", c.ref, got, c.kind)
		}
	}

	invalid := []struct{ ref, wantHas string }{
		{"", "为空"},
		{"sk-live-abcdef", "不接受明文"},
		{"Bearer abcdef", "不接受明文"},
		{"store:", "需为"},
		{"store:onlyns", "需为"},
		{"store:ns/", "需为"},
		{"store:/name", "需为"},
		{"store:../x", "非法"},
		{"store:ns/..", "非法"},
		{"store:ns/na me", "非法"},
		{"store:" + strings.Repeat("a", 65) + "/x", "非法"},
		{"env:", "非法"},
		{"env:1BAD", "非法"},
		{"env:HAS-DASH", "非法"},
	}
	for _, c := range invalid {
		err := ValidateSecretRef(c.ref)
		if err == nil {
			t.Fatalf("%q 应非法", c.ref)
		}
		if !strings.Contains(err.Error(), c.wantHas) {
			t.Fatalf("%q 错误文案应含 %q: %v", c.ref, c.wantHas, err)
		}
	}
	if SecretRefKind("sk-live-x") != "" {
		t.Fatal("明文不该被判为某种引用")
	}
}

// TestKeysOf 键列表排序且空返回 nil（读模型只回 key 时用它，顺序必须稳定）。
func TestKeysOf(t *testing.T) {
	if got := KeysOf(nil); got != nil {
		t.Fatalf("空字典应返回 nil: %v", got)
	}
	got := KeysOf(map[string]string{"b": "2", "a": "1", "C": "3"})
	want := []string{"C", "a", "b"}
	if len(got) != len(want) {
		t.Fatalf("键数不符: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("键未排序: %v", got)
		}
	}
}

// TestValidateBindings 明文字典不许空值，密钥字典只认引用。
func TestValidateBindings(t *testing.T) {
	if err := ValidatePlainBindings("env", map[string]string{"A": ""}); err == nil {
		t.Fatal("明文空值应报错（多半是写漏了）")
	}
	if err := ValidatePlainBindings("env", map[string]string{"": "x"}); err == nil {
		t.Fatal("空键名应报错")
	}
	if err := ValidatePlainBindings("env", map[string]string{"A": "1"}); err != nil {
		t.Fatalf("正常明文应通过: %v", err)
	}
	if err := ValidateSecretBindings("secret_env", map[string]string{"A": "plain-text-secret"}); err == nil {
		t.Fatal("密钥字典里的明文应被拒")
	}
	if err := ValidateSecretBindings("secret_env", map[string]string{"A": "store:mcp/a"}); err != nil {
		t.Fatalf("合法引用应通过: %v", err)
	}
	// 错误文案要能定位到具体键（不然一堆绑定里不知道哪条错）。
	err := ValidateSecretBindings("secret_env", map[string]string{"FS_TOKEN": "oops"})
	if err == nil || !strings.Contains(err.Error(), "FS_TOKEN") {
		t.Fatalf("错误应含键名: %v", err)
	}
}

// TestRegisterMcpServerSecretRules 注册闸门：密钥只收引用、明文/密钥不许同名键。
func TestRegisterMcpServerSecretRules(t *testing.T) {
	base := func() *McpServer {
		return &McpServer{ID: "fs", Transport: TransportStdio, Command: "/usr/local/bin/npx"}
	}

	// 1) 密钥字典里的明文直接被拒（这是 M1 存在的理由）。
	//
	// 注意：明文 `headers` 里的 `Bearer …` **不被拒**——类型分离管的是"秘密该放哪个字段"，
	// 不是"哪个值看起来像秘密"（后者是 §7 的凭据卫生，猜错会挡掉正常配置）。命中像凭据的
	// 键名时注册期只告警提醒改放 secret_headers；读模型则**任何 header 值都不回**。
	m := base()
	m.Headers = map[string]string{"Authorization": "Bearer sk-live-123"}
	if err := NewRegistry().RegisterMcpServer(m); err != nil {
		t.Fatalf("明文 headers 仍应可注册（只告警提示）: %v", err)
	}
	m2 := base()
	m2.SecretEnv = map[string]string{"FS_TOKEN": "sk-live-123"}
	if err := NewRegistry().RegisterMcpServer(m2); err == nil {
		t.Fatal("明文 secret_env 应被拒")
	}

	// 2) 合法引用通过。
	m3 := base()
	m3.Env = map[string]string{"FS_ROOT": "/tmp"}
	m3.SecretEnv = map[string]string{"FS_TOKEN": "store:mcp/fs-token"}
	m3.SecretHeaders = map[string]string{"Authorization": "env:FS_AUTH"}
	if err := NewRegistry().RegisterMcpServer(m3); err != nil {
		t.Fatalf("合法引用应通过: %v", err)
	}

	// 3) 同名键同时出现在明文与密钥字典 → 拒（不悄悄定优先级）。
	m4 := base()
	m4.Env = map[string]string{"TOKEN": "x"}
	m4.SecretEnv = map[string]string{"TOKEN": "store:mcp/t"}
	if err := NewRegistry().RegisterMcpServer(m4); err == nil || !strings.Contains(err.Error(), "键不得重复") {
		t.Fatalf("同名键应被拒: %v", err)
	}
	m5 := base()
	m5.Headers = map[string]string{"X-Token": "x"}
	m5.SecretHeaders = map[string]string{"X-Token": "store:mcp/t"}
	if err := NewRegistry().RegisterMcpServer(m5); err == nil || !strings.Contains(err.Error(), "键不得重复") {
		t.Fatalf("同名 header 应被拒: %v", err)
	}

	// 4) 别的校验不受影响（transport / 绝对路径）。
	m6 := base()
	m6.Transport = "carrier-pigeon"
	if err := NewRegistry().RegisterMcpServer(m6); err == nil {
		t.Fatal("非法 transport 仍应被拒")
	}
	m7 := base()
	m7.Command = "npx"
	if err := NewRegistry().RegisterMcpServer(m7); err == nil {
		t.Fatal("相对 command 仍应被拒")
	}
}

// TestTranslateLegacySecretRef 旧前缀的**唯一翻译点**：认得出、翻得对、并且告警。
//
// 为什么这条必须写旧前缀：档案文件（`providers.json`、能力目录里的 `mcp/*.json`）不在库里、
// v20 迁移改不到它们，所以导入时必须认出旧串并就地翻成 `store:`——不写旧串就测不到"认得出来"。
// 反过来，**运行时**读到旧串必须判失败（它是坏数据，翻译会掩盖它）：那半在 `internal/manager`。
func TestTranslateLegacySecretRef(t *testing.T) {
	// 认得出 + 翻得对（整串剥前缀后拼 `store:`）。
	for _, tc := range []struct{ in, want string }{
		{"vault:mcp/fs-token", "store:mcp/fs-token"},
		{"vault:providers/default", "store:providers/default"},
		{"  vault:a/B  ", "store:a/B"},
	} {
		got, ok := TranslateLegacySecretRef(tc.in)
		if !ok || got != tc.want {
			t.Fatalf("TranslateLegacySecretRef(%q) = %q, %v；want %q, true", tc.in, got, ok, tc.want)
		}
	}
	// 新前缀与 env 引用原样返回（不误翻）。
	for _, ref := range []string{"store:mcp/a", "env:FS_TOKEN", "sk-plaintext"} {
		if got, ok := TranslateLegacySecretRef(ref); ok || got != ref {
			t.Fatalf("%q 不该被翻译：got=%q ok=%v", ref, got, ok)
		}
	}
	// 告警要说清三件事：旧机制已删除、档案文件不在库里、只在这条导入路径上翻。
	warn := LegacySecretRefWarning("vault:mcp/x")
	for _, want := range []string{"vault:mcp/x", SecretRefLegacyVaultPrefix, SecretRefStorePrefix, "档案文件不在库里", "删除"} {
		if !strings.Contains(warn, want) {
			t.Fatalf("告警文案应含 %q: %s", want, warn)
		}
	}
}
