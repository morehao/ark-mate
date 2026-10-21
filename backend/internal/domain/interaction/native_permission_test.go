package interaction

import (
	"github.com/morehao/ark-mate/internal/infra/dsh"
	"reflect"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/runtime"
)

// portRegistry 一张**带可执行半场**的注册表（含各内置 adapter 的端口）。
//
// 为什么不用 `runtime.NewRegistry()`（空表）：原生权限的实现挂在注册表的第三张表上
// （ADR-2：`Spec` 只装纯数据、行为进执行表），空表上"没有端口"与"这个底座没有原生机制"
// 是同一种表现——用它当夹具会把"内核只做转发"这件事掩盖掉。
func portRegistry() *runtime.Registry { return dsh.Registry() }

// TestNativePermissionDispatch 内核只做三件事：nil 短路、`off` 短路、按 runtime 取端口转发。
// 具体映射的词表与算法在 adapter 侧（每个 adapter 自己的包与它的测试）。
func TestNativePermissionDispatch(t *testing.T) {
	reg := portRegistry()

	// ① nil 策略：行为与历史一致（不注入、无说明）。
	if env, desc := NativePermissionEnv(reg, "dsh", nil); len(env) != 0 || desc != "" {
		t.Fatalf("nil 策略应静默不注入，got env=%v desc=%q", env, desc)
	}

	// ② native_mode=off 是**平台级开关**：任何底座、任何其它取值都不注入口。
	for _, id := range []string{"dsh", "rtx", "no-such-runtime"} {
		env, desc := NativePermissionEnv(reg, id, &PermissionPolicy{Default: "deny", NativeMode: "off"})
		if len(env) != 0 {
			t.Fatalf("%s: off 应不注入，got %v", id, env)
		}
		if !strings.Contains(desc, "native_mode=off") {
			t.Fatalf("%s: off 应说明原因，got %q", id, desc)
		}
	}
	// 大小写不敏感（wire 上允许大写）。
	if env, _ := NativePermissionEnv(reg, "dsh", &PermissionPolicy{NativeMode: "OFF"}); len(env) != 0 {
		t.Fatalf("OFF 应不注入，got %v", env)
	}

	// ③ 端口转发：内核不解析词表，拿到什么就是什么。
	env, desc := NativePermissionEnv(reg, "dsh", &PermissionPolicy{Default: "deny"})
	if want := []string{"DSH_PERMISSION_MODE=read-only"}; !reflect.DeepEqual(env, want) {
		t.Fatalf("dsh 应注入 %v，got %v（desc=%q）", want, env, desc)
	}
	// 模式在**调用端口之前**被归一（小写+去空白）——adapter 拿到的是规范取值。
	if env, _ := NativePermissionEnv(reg, "dsh", &PermissionPolicy{Default: "deny", NativeMode: " DANGER-FULL-ACCESS "}); !reflect.DeepEqual(env, []string{"DSH_PERMISSION_MODE=danger-full-access"}) {
		t.Fatalf("显式模式应被归一后转发，got %v", env)
	}
}

// TestNativePermissionNoMechanism 未登记原生机制的 runtime：如实报"无原生权限开关（仅平台拦截面）"，
// 而不是静默什么都不做——静默会让操作员以为策略已经投影到底座了。
func TestNativePermissionNoMechanism(t *testing.T) {
	env, desc := NativePermissionEnv(portRegistry(), "stub", &PermissionPolicy{Default: "deny"})
	if len(env) != 0 {
		t.Fatalf("无机制的 runtime 应不注入，got %v", env)
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

func mustEnv(t *testing.T, p *PermissionPolicy) []string {
	t.Helper()
	env, _ := NativePermissionEnv(portRegistry(), "dsh", p)
	return env
}
