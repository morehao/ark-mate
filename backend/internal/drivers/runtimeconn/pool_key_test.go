package runtimeconn

import (
	"fmt"
	"testing"

	"github.com/morehao/ark-mate/internal/runtime"
)

// TestKeyForSeparatesNativeModes 原生权限模式必须**分进程**（P1-f 下半）。
//
// 模式只能以进程 env（`DSH_PERMISSION_MODE`）下发，所以两个模式不同的会话不能共用一个暖进程。
// 这条判据落在**键**上：键相同才共享进程，所以"键不同"就是"进程不同"的充分证据。
func TestKeyForSeparatesNativeModes(t *testing.T) {
	pl := NewPool(nil)
	pl.SetNativeEnvSource(stubNative{})
	spec := runtime.Spec{ID: "dsh"}

	ro := pl.KeyFor(spec, "", "read-only")
	full := pl.KeyFor(spec, "", "danger-full-access")
	none := pl.KeyFor(spec, "", "")

	if ro.Key == full.Key {
		t.Fatal("read-only 与 danger-full-access 的键相同：它们会共用一个暖进程，" +
			"后建的会话跑在先进那个进程的模式里——用户以为选了只读，实际是完全访问")
	}
	if ro.BaseKey == full.BaseKey {
		t.Fatal("两个模式的**基础键**相同：`PermChanged` 再也判不出「只是模式变了」，" +
			"代际裁决会把它当成同一条连接")
	}
	// 注入的 env 必须是**这一档**的（键与注入同源）。
	if !contains(ro.Env, "DSH_PERMISSION_MODE=read-only") {
		t.Fatalf("read-only 的注入 env 里没有对应的环境变量：%v", ro.Env)
	}
	if contains(full.Env, "DSH_PERMISSION_MODE=read-only") {
		t.Fatalf("danger-full-access 的注入 env 里混进了 read-only：%v", full.Env)
	}
	// 空模式 = 不注入（平台缺省），且它与任何显式模式都不同键。
	if contains(none.Env, "DSH_PERMISSION_MODE=") {
		t.Fatalf("空模式不该注入任何原生权限变量：%v", none.Env)
	}
	if none.Key == ro.Key {
		t.Fatal("空模式与显式模式的键相同：显式那一档会被静默忽略")
	}
	// **缺省零回归**（R11）：没有端口、没有模式时键必须与"代际机制之前"逐字相同。
	pl2 := NewPool(nil)
	if got := pl2.KeyFor(spec, "", "").Key; got != "dsh" {
		t.Fatalf("缺省路径的键应逐字等于 runtime id（R11 零回归），got %q", got)
	}
}

// TestKeyForNativeEnvSharesWithProviderEnv 三个 env 组必须**一起**进基础键（不是互相覆盖）。
func TestKeyForNativeEnvSharesWithProviderEnv(t *testing.T) {
	pl := NewPool(nil)
	pl.SetProviderEnvSource(stubProvider{})
	pl.SetNativeEnvSource(stubNative{})
	spec := runtime.Spec{ID: "dsh"}
	k := pl.KeyFor(spec, "", "workspace-write")
	if !contains(k.Env, "PROVIDER_KEY=k") || !contains(k.Env, "DSH_PERMISSION_MODE=workspace-write") {
		t.Fatalf("注入 env 应同时含供应商密钥与原生模式，got %v", k.Env)
	}
	// 去掉原生那一组 → 基础键必须变（证明它不是被供应商那组"吃掉"了）。
	pl3 := NewPool(nil)
	pl3.SetProviderEnvSource(stubProvider{})
	if pl3.KeyFor(spec, "", "workspace-write").BaseKey == k.BaseKey {
		t.Fatal("原生 env 组没有参与基础键：它被供应商那一组吃掉了")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

type stubProvider struct{}

func (stubProvider) ProviderEnv(string) []string { return []string{"PROVIDER_KEY=k"} }

type stubNative struct{}

func (stubNative) NativePermissionEnv(runtimeID, mode string) ([]string, string) {
	if mode == "" {
		return nil, "空"
	}
	if mode == "boom" {
		return nil, "boom"
	}
	return []string{fmt.Sprintf("DSH_PERMISSION_MODE=%s", mode)}, "stub"
}
