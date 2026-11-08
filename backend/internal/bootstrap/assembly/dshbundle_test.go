package assembly

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/config"
)

// TestDshBundledRuntimeRootEnvOverride 显式 MATE_DSH_BUNDLED_RUNTIME 优先。
func TestDshBundledRuntimeRootEnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "runtime")
	t.Setenv("MATE_DSH_BUNDLED_RUNTIME", want)
	if got := dshBundledRuntimeRoot(config.Config{}); got != want {
		t.Fatalf("显式 env 应原样返回 %q，得到 %q", want, got)
	}
}

// TestDshBundledRuntimeRootFallbackWhenUnresolvable 无 env、PATH 里 dsh 又反推不出 runtime 根时：
// 返回空，**或**（本机装了 DeepSeek Harness 时）返回其随包 runtime 根——两态都合法，且返回的若是路径，
// 必须真的含 primary-runtime/runtime.json（不能是瞎猜的路径）。
func TestDshBundledRuntimeRootFallbackWhenUnresolvable(t *testing.T) {
	decoy := t.TempDir()
	t.Setenv("PATH", decoy) // 空目录：LookPath("dsh") 必失败
	t.Setenv("MATE_DSH_BUNDLED_RUNTIME", "")
	got := dshBundledRuntimeRoot(config.Config{})
	if got == "" {
		return // 合法：本机没装、反推不出 → 跳过注入
	}
	// 非空则必须是真实存在的随包 runtime（含 runtime.json）。
	if fi, err := os.Stat(filepath.Join(got, "primary-runtime", "runtime.json")); err != nil || fi.IsDir() {
		t.Fatalf("返回了 %q，但它不含 primary-runtime/runtime.json（不该瞎猜路径）", got)
	}
}

// TestDshBundledRuntimeRootFromPath 造一个 <root>/runtime/cli/bin/dsh 布局，反推出 <root>/runtime。
func TestDshBundledRuntimeRootFromPath(t *testing.T) {
	root := t.TempDir() // 用作 <root>
	binDir := filepath.Join(root, "runtime", "cli", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(binDir, "dsh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MATE_DSH_BUNDLED_RUNTIME", "")
	want := filepath.Join(root, "runtime")
	// EvalSymlinks 会把 /var → /private/var 规整，比较时同样规整。
	if real, err := filepath.EvalSymlinks(want); err == nil {
		want = real
	}
	if got := dshBundledRuntimeRoot(config.Config{}); got != want {
		t.Fatalf("反推应得 %q，得到 %q", want, got)
	}
}
