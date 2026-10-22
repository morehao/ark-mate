package dsh

import (
	"reflect"
	"strings"
	"testing"
)

// TestNativePermissionEnv B5：dsh 原生权限模式推导（auto/显式/未知）。
//
// `off` 不在这里出现：它是**平台级开关**，由内核在调端口之前短路
// （见 `internal/domain/interaction/native_permission.go`）。
func TestNativePermissionEnv(t *testing.T) {
	cases := []struct {
		name  string
		mode  string
		def   string
		want  string
		empty bool
	}{
		{"default=ask 与 dsh 默认一致 → 不注入", "", "ask", "", true},
		{"空 default → 不注入", "", "", "", true},
		{"default=allow 不自动升级完全访问 → 不注入", "", "allow", "", true},
		{"default=deny → read-only", "", "deny", NativeModeReadOnly, false},
		{"default=DENY 大小写不敏感", "", " DENY ", NativeModeReadOnly, false},
		{"显式 read-only", NativeModeReadOnly, "allow", NativeModeReadOnly, false},
		{"显式 workspace-write", NativeModeWorkspaceWrite, "deny", NativeModeWorkspaceWrite, false},
		{"显式 danger-full-access", NativeModeFullAccess, "deny", NativeModeFullAccess, false},
		{"未知 mode 不注入", "yolo", "deny", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, desc := NativePermission{}.Env(c.mode, c.def, nil)
			if c.empty {
				if len(env) != 0 {
					t.Fatalf("应不注入，got %v", env)
				}
				// "为什么没注入"必须看得到——否则"我设了策略却没反应"只能靠猜。
				if strings.TrimSpace(desc) == "" {
					t.Fatal("不注入时也必须给出说明")
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

// TestNativePermissionIgnoresTools dsh 的原生面只有"模式"这一维：工具级策略走平台拦截面，
// 不得因为传了 tools 就产生任何额外注入（那会让"平台自动放行"被误解为"底座也放开了"）。
func TestNativePermissionIgnoresTools(t *testing.T) {
	env, _ := NativePermission{}.Env("", "ask", map[string]string{"bash": "allow", "edit": "deny"})
	if len(env) != 0 {
		t.Fatalf("tools 不该影响 dsh 的原生投影，got %v", env)
	}
}
