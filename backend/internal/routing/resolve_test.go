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

// TestSupportsTransports 传输承载判定：白名单含 required；空传输列表保守按 stdio。
func TestSupportsTransports(t *testing.T) {
	spec := runtime.Spec{McpTransports: []string{"stdio", "http"}}
	if !SupportsTransports(spec, []string{"stdio", "http"}) {
		t.Fatal("stdio+http 应承载 stdio+http")
	}
	if SupportsTransports(spec, []string{"sse"}) {
		t.Fatal("不应承载 sse")
	}
	if !SupportsTransports(runtime.Spec{}, []string{"stdio"}) {
		t.Fatal("空 McpTransports 应保守承载 stdio")
	}
	if SupportsTransports(runtime.Spec{}, []string{"http"}) {
		t.Fatal("空 McpTransports 不应承载 http")
	}
}

// TestCapabilityRouted 能力匹配路由：候选须能承载 requiredTransports；无法承载落
// CapabilityMismatch；required 为空 = 等价 FirstAvailable。
func TestCapabilityRouted(t *testing.T) {
	exe, _ := os.Executable()
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "http-rt", Launch: runtime.LaunchSpec{Command: exe}, McpTransports: []string{"http"}},
		runtime.Spec{ID: "stdio-rt", Launch: runtime.LaunchSpec{Command: exe}, McpTransports: []string{"stdio"}},
		runtime.Spec{ID: "sse-rt", Launch: runtime.LaunchSpec{Command: exe}, McpTransports: []string{"sse"}},
	)
	if id, _ := CapabilityRouted(reg, []string{"http-rt", "stdio-rt"}, []string{"stdio"}); id != "stdio-rt" {
		t.Fatalf("应跳过 http-rt 取 stdio-rt，got %q", id)
	}
	if id, av := CapabilityRouted(reg, []string{"http-rt", "stdio-rt"}, []string{"sse"}); id != "" ||
		len(av.CapabilityMismatch) != 2 {
		t.Fatalf("全部能力不符应空串且记录 mismatch，id=%q av=%+v", id, av)
	}
	if id, _ := CapabilityRouted(reg, []string{"stdio-rt"}, nil); id != "stdio-rt" {
		t.Fatalf("required 为空应等价 FirstAvailable，got %q", id)
	}
	// 未注册 + 能力不符并存时分类正确。
	if id, av := CapabilityRouted(reg, []string{"ghost", "stdio-rt"}, []string{"sse"}); id != "" ||
		len(av.NotFound) != 1 || len(av.CapabilityMismatch) != 1 {
		t.Fatalf("分类不符：id=%q av=%+v", id, av)
	}
}

// TestParseStrategy 策略解析：已知两值 + 未知/空回退默认。
func TestParseStrategy(t *testing.T) {
	if s, ok := ParseStrategy("capability_routed"); !ok || s != StrategyCapabilityRouted {
		t.Fatalf("capability_routed 解析失败: %q %v", s, ok)
	}
	if s, ok := ParseStrategy(""); ok || s != StrategyFirstAvailable {
		t.Fatalf("空应回退默认: %q %v", s, ok)
	}
	if s, ok := ParseStrategy("sticky"); ok || s != StrategyFirstAvailable {
		t.Fatalf("未知应回退默认: %q %v", s, ok)
	}
}
