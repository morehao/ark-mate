package manager

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/pkg/runtime"
)

// TestNativePermissionDSH B5：dsh 原生权限模式推导（auto/显式/off/未知）。
func TestNativePermissionDSH(t *testing.T) {
	cases := []struct {
		name  string
		pol   *PermissionPolicy
		want  string
		empty bool
	}{
		{"nil 策略不注入", nil, "", true},
		{"default=ask 与 dsh 默认一致 → 不注入", &PermissionPolicy{Default: "ask"}, "", true},
		{"空 default → 不注入", &PermissionPolicy{}, "", true},
		{"default=allow 不自动升级完全访问 → 不注入", &PermissionPolicy{Default: "allow"}, "", true},
		{"default=deny → read-only", &PermissionPolicy{Default: "deny"}, "read-only", false},
		{"default=DENY 大小写不敏感", &PermissionPolicy{Default: " DENY "}, "read-only", false},
		{"显式 read-only", &PermissionPolicy{Default: "allow", NativeMode: "read-only"}, "read-only", false},
		{"显式 workspace-write", &PermissionPolicy{Default: "deny", NativeMode: "workspace-write"}, "workspace-write", false},
		{"显式 danger-full-access", &PermissionPolicy{Default: "deny", NativeMode: "danger-full-access"}, "danger-full-access", false},
		{"native_mode=off 不注入", &PermissionPolicy{Default: "deny", NativeMode: "off"}, "", true},
		{"native_mode=OFF 大小写不敏感", &PermissionPolicy{NativeMode: "OFF"}, "", true},
		{"未知 mode 不注入", &PermissionPolicy{NativeMode: "yolo"}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, desc := nativePermissionEnv("deepseekharness", c.pol)
			if c.empty {
				if len(env) != 0 {
					t.Fatalf("应不注入，got %v", env)
				}
				return
			}
			want := []string{"DSH_PERMISSION_MODE=" + c.want}
			if !reflect.DeepEqual(env, want) {
				t.Fatalf("env=%v want=%v", env, want)
			}
			if !strings.Contains(desc, c.want) {
				t.Fatalf("说明应含模式：%q", desc)
			}
		})
	}
}

// TestNativePermissionOpencode B5：opencode permission 表生成（默认项/别名/更严者胜/未映射键）。
func TestNativePermissionOpencode(t *testing.T) {
	// 默认 ask + 别名映射。
	env, desc := nativePermissionEnv("opencode", &PermissionPolicy{
		Default: "ask",
		Tools: map[string]string{
			"bash":    "allow",
			"execute": "deny",  // 与 bash 同映射 → deny 更严者胜
			"write":   "deny",  // → edit
			"fetch":   "allow", // → webfetch
			"mystery": "allow", // 未映射 → 跳过
		},
	})
	if len(env) != 1 || !strings.HasPrefix(env[0], "OPENCODE_CONFIG_CONTENT=") {
		t.Fatalf("应注入 OPENCODE_CONFIG_CONTENT，got %v", env)
	}
	var cfg struct {
		Permission map[string]string `json:"permission"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(env[0], "OPENCODE_CONFIG_CONTENT=")), &cfg); err != nil {
		t.Fatalf("注入内容应为合法 JSON: %v", err)
	}
	want := map[string]string{"*": "ask", "bash": "deny", "edit": "deny", "webfetch": "allow"}
	if !reflect.DeepEqual(cfg.Permission, want) {
		t.Fatalf("permission=%v want=%v", cfg.Permission, want)
	}
	if !strings.Contains(desc, "未映射键 mystery") {
		t.Fatalf("说明应提示未映射键：%q", desc)
	}

	// 空策略 → 不注入。
	if env, _ := nativePermissionEnv("opencode", &PermissionPolicy{}); len(env) != 0 {
		t.Fatalf("空策略应不注入，got %v", env)
	}
	// native_mode=off → 不注入。
	if env, _ := nativePermissionEnv("opencode", &PermissionPolicy{Default: "allow", NativeMode: "off"}); len(env) != 0 {
		t.Fatalf("off 应不注入，got %v", env)
	}
	// 仅全 ask：与 opencode 默认等价 → 不注入。
	if env, desc := nativePermissionEnv("opencode", &PermissionPolicy{Default: "ask"}); len(env) != 0 {
		t.Fatalf("全 ask 应不注入，got %v", env)
	} else if !strings.Contains(desc, "等价 opencode 默认") {
		t.Fatalf("应说明不注入原因：%q", desc)
	}
	// 只有未映射键 → 不注入（不生成空配置）。
	if env, _ := nativePermissionEnv("opencode", &PermissionPolicy{Tools: map[string]string{"weird": "deny"}}); len(env) != 0 {
		t.Fatalf("仅未映射键应不注入，got %v", env)
	}
}

// TestNativePermissionEcho B5：无原生开关的 runtime 不注入。
func TestNativePermissionEcho(t *testing.T) {
	env, desc := nativePermissionEnv("stub", &PermissionPolicy{Default: "deny"})
	if len(env) != 0 {
		t.Fatalf("echo 应不注入，got %v", env)
	}
	if !strings.Contains(desc, "无原生权限开关") {
		t.Fatalf("说明应写明降级原因：%q", desc)
	}
}

// TestNativeModeValidate B5：native_mode 非法值在定义加载期被拒（不静默生效）。
func TestNativeModeValidate(t *testing.T) {
	if err := (&PermissionPolicy{NativeMode: "yolo"}).Validate(); err == nil {
		t.Fatal("非法 native_mode 应报错")
	}
	for _, ok := range ValidNativeModes {
		if err := (&PermissionPolicy{NativeMode: ok}).Validate(); err != nil {
			t.Fatalf("%q 应合法: %v", ok, err)
		}
	}
	if err := (&PermissionPolicy{NativeMode: "DANGER-FULL-ACCESS"}).Validate(); err != nil {
		t.Fatalf("大小写不敏感应合法: %v", err)
	}
}

// TestNativePermissionEnabledSwitch B5：Service 开关（默认开 / 可关）。
func TestNativePermissionEnabledSwitch(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), nil)
	if !svc.NativePermissionEnabled() {
		t.Fatal("默认应开启")
	}
	svc.SetNativePermission(false)
	if svc.NativePermissionEnabled() {
		t.Fatal("关闭后应为 false")
	}
}

// TestOpencodePermissionConfigStable B5：同一策略生成稳定结果（map 迭代序不影响输出）。
func TestOpencodePermissionConfigStable(t *testing.T) {
	pol := &PermissionPolicy{Default: "ask", Tools: map[string]string{
		"bash": "allow", "edit": "deny", "read": "allow", "webfetch": "ask", "glob": "deny", "list": "allow",
	}}
	var first string
	for i := 0; i < 20; i++ {
		cfg, _ := opencodePermissionConfig(pol)
		keys := make([]string, 0, len(cfg))
		for k := range cfg {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		cur := compactJSON(cfg)
		if i == 0 {
			first = cur
			continue
		}
		if cur != first {
			t.Fatalf("输出不稳定: %q vs %q", cur, first)
		}
		// read/grep/glob/list 都映射到 read：deny 更严者胜。
		if cfg["read"] != "deny" {
			t.Fatalf("read 应取更严者 deny: %v", cfg)
		}
	}
}

// TestConnKeyIsolatesNativePermission B5：连接缓存键含原生权限签名——配置不同不得共享子进程。
func TestConnKeyIsolatesNativePermission(t *testing.T) {
	ro := []string{"DSH_PERMISSION_MODE=read-only"}
	full := []string{"DSH_PERMISSION_MODE=danger-full-access"}
	if connKey("deepseekharness", ro) == connKey("deepseekharness", full) {
		t.Fatal("不同原生权限配置应产生不同连接键")
	}
	if connKey("deepseekharness", ro) != connKey("deepseekharness", ro) {
		t.Fatal("相同配置应产生相同连接键（可共享暖进程）")
	}
	if connKey("stub", nil) != "stub" {
		t.Fatalf("无原生配置时应退化为 runtime ID: %q", connKey("stub", nil))
	}
	// 同一策略两次计算结果一致（env 顺序稳定）。
	pol := &PermissionPolicy{Default: "deny"}
	if connKey("deepseekharness", mustEnv(t, pol)) != connKey("deepseekharness", mustEnv(t, pol)) {
		t.Fatal("同一策略的连接键应稳定")
	}
}

func mustEnv(t *testing.T, p *PermissionPolicy) []string {
	t.Helper()
	env, _ := nativePermissionEnv("deepseekharness", p)
	return env
}

// TestNativePermissionEnvForSwitch B5：Service 开关关闭后不注入。
func TestNativePermissionEnvForSwitch(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), nil)
	if env := svc.nativePermissionEnvFor("deepseekharness", &PermissionPolicy{Default: "deny"}); len(env) != 1 {
		t.Fatalf("默认应注入，got %v", env)
	}
	svc.SetNativePermission(false)
	if env := svc.nativePermissionEnvFor("deepseekharness", &PermissionPolicy{Default: "deny"}); len(env) != 0 {
		t.Fatalf("关闭后应不注入，got %v", env)
	}
}
