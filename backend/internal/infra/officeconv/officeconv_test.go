package officeconv

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dsh"
)

// TestNewUnavailableWhenNoPayload 未给任何路径时必须可用性为 false，且 ConvertFile 明确返回
// ErrUnavailable（而不是 panic / 裸错）——上层据此返回降级码 office_preview_unsupported。
func TestNewUnavailableWhenNoPayload(t *testing.T) {
	// 显式把两个 env 清空 + 临时把 MATE_DSH_ENTRY 清掉，制造"无 payload"环境。
	t.Setenv("MATE_OFFICE_NODE", "")
	t.Setenv("MATE_OFFICE_CLI", "")
	t.Setenv("MATE_DSH_ENTRY", "")
	t.Setenv("MATE_DSH_BUNDLED_RUNTIME", "")
	t.Setenv("MATE_DSH_BUNDLED_OFFICE_SKILLS", "")

	c := NewForPaths("", "")
	if c.Available() {
		t.Fatal("空路径时 Available() 应为 false")
	}
	if _, err := c.ConvertFile(context.Background(), "/tmp/nope.docx", "nope.docx"); err != ErrUnavailable {
		t.Fatalf("无 payload 时 ConvertFile 应返回 ErrUnavailable，got %v", err)
	}
}

// TestNewForPathsAvailable 给定存在的文件路径 → Available()=true。
func TestNewForPathsAvailable(t *testing.T) {
	dir := t.TempDir()
	node := filepath.Join(dir, "node")
	cli := filepath.Join(dir, "cli.js")
	for _, p := range []string{node, cli} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := NewForPaths(node, cli)
	if !c.Available() {
		t.Fatal("node 与 cli 都在场时 Available() 应为 true")
	}
}

// TestConvertRealDocx 真实端到端：用随包 primary-runtime 里 python-docx 的 default.docx 模板
// 做源文件，真实调用 libreoffice-kit 转 PDF，断言产出是合法 %PDF。
//
// 找不到随包 payload（CI 或裁剪环境）时 Skip——本用例的意义是**在能力就位的机器上反证真能转**，
// 而不是把 payload 变成测试硬依赖。
func TestConvertRealDocx(t *testing.T) {
	primaryRuntime, _ := dsh.ResolveBundledDirs()
	if primaryRuntime == "" {
		t.Skip("随包 primary-runtime 缺失，跳过真实转换用例")
	}
	src := filepath.Join(primaryRuntime, "dependencies", "python", "lib",
		"python3.12", "site-packages", "docx", "templates", "default.docx")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("未找到 docx 模板源文件，跳过：%v", err)
	}

	c := New()
	if !c.Available() {
		t.Skip("officeconv 未解析到 node/cli（payload 缺 libreoffice-kit），跳过真实转换用例")
	}
	pdf, err := c.ConvertFile(context.Background(), src, "default.docx")
	if err != nil {
		t.Fatalf("真实转换应成功：%v", err)
	}
	if len(pdf) < 5 || string(pdf[:5]) != "%PDF-" {
		t.Fatalf("产出不是合法 PDF：前若干字节 %q", string(pdf[:min(16, len(pdf))]))
	}
	t.Logf("真实 docx→pdf 成功，产物大小 %d 字节，magic=%q", len(pdf), string(pdf[:5]))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
