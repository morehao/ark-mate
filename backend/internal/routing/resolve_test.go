package routing

import (
	"os"
	"testing"

	"github.com/morehao/ark-forge/internal/runtime"
)

// TestFirstAvailable 首可用路由：候选顺序 + 未注册跳过 + 已注册但命令不可执行跳过；
// 未采用分类正确记录；无候选/全部未采用 → 空串。
func TestFirstAvailable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "installed", Launch: runtime.LaunchSpec{Command: exe}},
		runtime.Spec{ID: "ghost", Launch: runtime.LaunchSpec{Command: "ghost-cmd-not-exist-zzz-9f3a"}},
	)

	id, av := FirstAvailable(reg, []string{"ghost", "installed"})
	if id != "installed" {
		t.Fatalf("应取 installed，got %q", id)
	}
	if len(av.NotInstalled) != 1 || av.NotInstalled[0] != "ghost" || len(av.NotFound) != 0 {
		t.Fatalf("未采用分类不符: %+v", av)
	}

	if id, av := FirstAvailable(reg, []string{"not-registered"}); id != "" ||
		len(av.NotFound) != 1 || av.NotFound[0] != "not-registered" {
		t.Fatalf("未注册候选应落 NotFound，id=%q av=%+v", id, av)
	}
	if id, av := FirstAvailable(reg, []string{"ghost"}); id != "" ||
		len(av.NotInstalled) != 1 || av.NotInstalled[0] != "ghost" {
		t.Fatalf("命令不可执行候选应落 NotInstalled，id=%q av=%+v", id, av)
	}
	if id, _ := FirstAvailable(reg, nil); id != "" {
		t.Fatalf("空候选应空串，got %q", id)
	}
}

// TestInstalled 命令可执行判定：PATH 查找与路径 stat 两分支 + 空命令。
func TestInstalled(t *testing.T) {
	exe, _ := os.Executable()
	if !Installed(runtime.Spec{Launch: runtime.LaunchSpec{Command: exe}}) {
		t.Fatal("绝对路径可执行文件应判 installed")
	}
	if Installed(runtime.Spec{Launch: runtime.LaunchSpec{Command: "ghost-cmd-not-exist-zzz-9f3a"}}) {
		t.Fatal("PATH 找不到应判未安装")
	}
	if Installed(runtime.Spec{}) {
		t.Fatal("空命令应判未安装")
	}
}
