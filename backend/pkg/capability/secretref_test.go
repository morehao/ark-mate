package capability

import (
	"strings"
	"testing"
)

// TestValidateSecretRefLexicon 引用词法：只认 vault:/env: 两种，明文一律拒（M1 的核心判据）。
func TestValidateSecretRefLexicon(t *testing.T) {
	valid := []struct{ ref, kind string }{
		{"vault:providers/default", "vault"},
		{"vault:mcp/fs-token", "vault"},
		{"vault:a/B", "vault"},
		{"env:FS_TOKEN", "env"},
		{"env:_x9", "env"},
		{"  vault:mcp/x  ", "vault"}, // 前后空白容忍（TrimSpace）
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
		{"vault:", "需为"},
		{"vault:onlyns", "需为"},
		{"vault:ns/", "需为"},
		{"vault:/name", "需为"},
		{"vault:../x", "非法"},
		{"vault:ns/..", "非法"},
		{"vault:ns/na me", "非法"},
		{"vault:" + strings.Repeat("a", 65) + "/x", "非法"},
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
	if err := ValidateSecretBindings("secret_env", map[string]string{"A": "vault:mcp/a"}); err != nil {
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
	m3.SecretEnv = map[string]string{"FS_TOKEN": "vault:mcp/fs-token"}
	m3.SecretHeaders = map[string]string{"Authorization": "env:FS_AUTH"}
	if err := NewRegistry().RegisterMcpServer(m3); err != nil {
		t.Fatalf("合法引用应通过: %v", err)
	}

	// 3) 同名键同时出现在明文与密钥字典 → 拒（不悄悄定优先级）。
	m4 := base()
	m4.Env = map[string]string{"TOKEN": "x"}
	m4.SecretEnv = map[string]string{"TOKEN": "vault:mcp/t"}
	if err := NewRegistry().RegisterMcpServer(m4); err == nil || !strings.Contains(err.Error(), "键不得重复") {
		t.Fatalf("同名键应被拒: %v", err)
	}
	m5 := base()
	m5.Headers = map[string]string{"X-Token": "x"}
	m5.SecretHeaders = map[string]string{"X-Token": "vault:mcp/t"}
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
