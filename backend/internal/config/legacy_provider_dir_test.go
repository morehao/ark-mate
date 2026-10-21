package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v19 的目录改名（agents/ → providers/）只搬 providers.json 一次，且**不删旧文件**。
// 失效方式都是静默的：不搬 → 用户的供应商在界面上凭空消失；重复搬/覆盖 → 用历史覆盖现在。
func TestImportLegacyProviderDirImportsOnce(t *testing.T) {
	home := t.TempDir()
	legacyDir := filepath.Join(home, "agents")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const body = `{"providers":[{"id":"pv_1"}]}`
	legacy := filepath.Join(legacyDir, "providers.json")
	if err := os.WriteFile(legacy, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	imported, warn, err := ImportLegacyProviderDir(filepath.Join(home, "providers"), home)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if !imported {
		t.Fatal("旧文件存在、新文件不存在时必须导入")
	}
	if !strings.Contains(warn, "providers/") || !strings.Contains(warn, "旧文件保留") {
		t.Fatalf("WARN 必须给出新路径与“旧文件保留”这条事实: %q", warn)
	}
	got, err := os.ReadFile(filepath.Join(home, "providers", "providers.json"))
	if err != nil || string(got) != body {
		t.Fatalf("导入内容不一致: %q, %v", got, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("旧文件必须保留（它是退路）: %v", err)
	}
	// 幂等：第二次不再导入（新文件已存在）。
	again, warn2, err := ImportLegacyProviderDir(filepath.Join(home, "providers"), home)
	if err != nil || again || warn2 != "" {
		t.Fatalf("新文件已存在时不该再导入: imported=%v warn=%q err=%v", again, warn2, err)
	}
}

func TestImportLegacyProviderDirKeepsNewFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "providers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "agents", "providers.json"), []byte(`{"old":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	const newBody = `{"new":true}`
	if err := os.WriteFile(filepath.Join(home, "providers", "providers.json"), []byte(newBody), 0o600); err != nil {
		t.Fatal(err)
	}
	imported, _, err := ImportLegacyProviderDir(filepath.Join(home, "providers"), home)
	if err != nil || imported {
		t.Fatalf("新目录里的那份是事实源，不许被旧文件覆盖: imported=%v err=%v", imported, err)
	}
	got, _ := os.ReadFile(filepath.Join(home, "providers", "providers.json"))
	if string(got) != newBody {
		t.Fatalf("新文件被改写了: %q", got)
	}
}

func TestImportLegacyProviderDirSkipsCustomAndMissing(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "agents", "providers.json"), []byte(`{"p":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 自定义目录（MATE_PROVIDERS 指到别处）：用户显式选了另一个位置，不替他做决定。
	custom := filepath.Join(t.TempDir(), "elsewhere")
	imported, warn, err := ImportLegacyProviderDir(custom, home)
	if err != nil || imported || warn != "" {
		t.Fatalf("自定义目录不该被写入: imported=%v warn=%q err=%v", imported, warn, err)
	}
	if _, err := os.Stat(filepath.Join(custom, "providers.json")); !os.IsNotExist(err) {
		t.Fatal("自定义目录里不该出现文件")
	}
	// 老布局不存在：静默无操作（新装场景）。
	empty := t.TempDir()
	imported, warn, err = ImportLegacyProviderDir(filepath.Join(empty, "providers"), empty)
	if err != nil || imported || warn != "" {
		t.Fatalf("老布局不存在时必须静默: imported=%v warn=%q err=%v", imported, warn, err)
	}
}
