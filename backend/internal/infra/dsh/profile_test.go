package dsh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestProvisionProfileWritesFiles 幂等落盘四份 profile 文件。
func TestProvisionProfileWritesFiles(t *testing.T) {
	home := t.TempDir()
	n, err := ProvisionProfile(home)
	if err != nil {
		t.Fatalf("ProvisionProfile: %v", err)
	}
	if n != 4 {
		t.Fatalf("首次应写 4 个文件，实际 %d", n)
	}
	dir := ProfileDir(home)
	for _, f := range []string{"package.json", "cordis.yml", "cordis.patch.yml", "pnpm-workspace.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("缺文件 %s: %v", f, err)
		}
	}
	// 覆写层必须含 office 三件套 + 官方 disabled 门控原文。
	patch, _ := os.ReadFile(filepath.Join(dir, "cordis.patch.yml"))
	s := string(patch)
	for _, want := range []string{
		"id: workspace-dependencies",
		"id: skill-office",
		"id: office-to-pdf",
		"@deepseek-ai/dsh-tool-workspace-dependencies",
		"@deepseek-ai/dsh-skill-office",
		"@deepseek-ai/dsh-office-to-pdf",
		"DSH_PRIMARY_RUNTIME",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("cordis.patch.yml 缺 %q", want)
		}
	}
	// 幂等：再跑一次不写任何文件。
	n2, err := ProvisionProfile(home)
	if err != nil || n2 != 0 {
		t.Fatalf("幂等重放应写 0 个，得到 %d / err=%v", n2, err)
	}
}

// TestProvisionProfileEmptyHome 空 homeDir 报错。
func TestProvisionProfileEmptyHome(t *testing.T) {
	if _, err := ProvisionProfile("  "); err == nil {
		t.Fatal("空 homeDir 应报错")
	}
}

// TestProvisionedProfileHasOfficeInDsh 端到端：provision 出来的 mate-acp 交给真实 dsh --dump-config，
// 断言 office 三件套进组合且 enabled、且 ACP 传输仍在。dsh 不在 PATH 时跳过。
func TestProvisionedProfileHasOfficeInDsh(t *testing.T) {
	bin, err := exec.LookPath("dsh")
	if err != nil {
		t.Skip("PATH 中没有 dsh，跳过端到端校验")
	}
	home := t.TempDir()
	if _, err := ProvisionProfile(home); err != nil {
		t.Fatalf("provision: %v", err)
	}
	// DSH_HOME 与 profile 落点一致：<home>/runtime/dsh。
	dshHome := filepath.Join(home, "runtime", "dsh")
	cmd := exec.Command(bin, "--profile", ProfileName, "--dump-config")
	cmd.Env = append(os.Environ(), "DSH_HOME="+dshHome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dsh --dump-config 失败（profile 可能让底座拒启）：%v\n%s", err, out)
	}
	s := string(out)
	// ACP 传输要在。
	if !strings.Contains(s, "id: acp-app-startup") || !strings.Contains(s, "id: acp\n") {
		t.Errorf("ACP 传输未进组合（被换了？）")
	}
	// office 三件套要在且 enabled（条目往下 220 字符内不应有 disabled: true）。
	for _, id := range []string{"id: workspace-dependencies", "id: skill-office", "id: office-to-pdf"} {
		idx := strings.Index(s, id)
		if idx < 0 {
			t.Fatalf("dump 里没有 %q（office 未进组合）", id)
		}
		tail := s[idx:min(idx+220, len(s))]
		if strings.Contains(tail, "disabled: true") {
			t.Errorf("%q 被 disabled 关掉了：\n%s", id, tail)
		}
	}
}
