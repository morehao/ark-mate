// Package officeconv 把 Office 文档（docx/pptx/xlsx…）在**服务端**转成 PDF 字节流，
// 供前端预览复用现有 PdfRenderer（docx/pptx 是 ZIP 二进制，前端直接按文本渲染会乱码）。
//
// 复用已有能力，少造轮子：转换由随包 vendor 的 `@deepseek-ai/libreoffice-kit` CLI 完成
// （`node <cli.js> convert --input X.docx --output Y.pdf` 已实测产出合法 `%PDF`）。
//
// 路径解析归装配根：**本包不 import `internal/infra/dsh`**（archtest 只许 assembly import
// runtime adapter）。node / cli.js 的绝对路径由装配根用 dsh.ResolveBundledDirs + ResolveOfficeCLI/
// ResolveOfficeNode 解析好后，经 NewForPaths 交进来；或由显式 env `MATE_OFFICE_NODE`/`MATE_OFFICE_CLI`
// 给定。
//
// 本包只做「进程调用 + 临时文件编排」，不认识 filestore / 业务状态机；字节从哪里来由上层
// （service）给绝对路径（本地驱动 blob 的只读路径）。
package officeconv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrUnavailable 表示当前形态下没有可用的转换器（随包 payload/node 缺失，或显式关闭）。
// 上层把它映射成「明确降级」的业务码（office_preview_unsupported），而不是 500。
var ErrUnavailable = errors.New("office 转 PDF 能力不可用：随包 payload（node / libreoffice-kit）缺失")

// Converter 把源文件转成 PDF 字节。
type Converter interface {
	// ConvertFile 把磁盘上 inputPath 的文档转成 PDF，返回 PDF 字节。
	// name 仅用于推导输出文件名（保留后缀语义即可，不影响渲染）。
	ConvertFile(ctx context.Context, inputPath, name string) ([]byte, error)
}

// CLI 是基于随包 libreoffice-kit CLI 的转换器实现。
type CLI struct {
	node string // node 可执行文件绝对路径（空 = 未解析到）
	cli  string // libreoffice-kit cli.js 绝对路径（空 = 未解析到）
}

var _ Converter = (*CLI)(nil)

// New 仅由 env（`MATE_OFFICE_NODE` / `MATE_OFFICE_CLI`）解析转换器；两者缺省则不可用。
//
// 需要「随包 payload 兜底」的场景由装配根解析好路径后用 NewForPaths 交进来（本包不 import dsh）。
// 任一路径为空 → Available()=false，ConvertFile 给出 ErrUnavailable（调用方据此返回明确降级码）。
func New() *CLI {
	return NewForPaths(os.Getenv("MATE_OFFICE_NODE"), os.Getenv("MATE_OFFICE_CLI"))
}

// NewForPaths 直接给定 node 与 cli 路径（测试与「路径已由其它渠道解析好」的装配用）。
func NewForPaths(node, cli string) *CLI {
	return &CLI{node: strings.TrimSpace(node), cli: strings.TrimSpace(cli)}
}

// Available 转换器是否可用（node 与 cli 都已解析到）。
func (c *CLI) Available() bool {
	return c != nil && c.node != "" && c.cli != ""
}

// ConvertFile 见接口约定。实现：
//   - 建一个临时工作目录（源文件 copy 进去，避免 CLI 往用户数据目录写）；
//   - 调 `node <cli> convert --input <in> --output <out.pdf>`；
//   - 读回 PDF 字节并校验 `%PDF` magic，非 PDF 视为转换失败（fail-closed）。
func (c *CLI) ConvertFile(ctx context.Context, inputPath, name string) ([]byte, error) {
	if !c.Available() {
		return nil, ErrUnavailable
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(inputPath))
	}
	if ext == "" {
		ext = ".bin"
	}

	workDir, err := os.MkdirTemp("", "mate-officeconv-*")
	if err != nil {
		return nil, fmt.Errorf("office 转 PDF：建临时目录失败：%w", err)
	}
	defer os.RemoveAll(workDir)

	src := filepath.Join(workDir, "input"+ext)
	if err := copyFile(inputPath, src); err != nil {
		return nil, fmt.Errorf("office 转 PDF：拷贝源文件失败：%w", err)
	}
	out := filepath.Join(workDir, "output.pdf")

	// 注意：libreoffice-kit CLI 的 convert 子命令参数形态为 --input/--output（已实测）。
	cmd := exec.CommandContext(ctx, c.node, c.cli, "convert", "--input", src, "--output", out)
	// 采集输出供诊断；失败时把尾部拼进错误信息（不含隐私：仅 CLI 自身的 JSON/日志）。
	combined, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return nil, fmt.Errorf("office 转 PDF 失败：%w（输出：%s）", runErr, tail(string(combined), 800))
	}

	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("office 转 PDF：读取输出失败：%w（输出：%s）", err, tail(string(combined), 400))
	}
	if len(data) < 5 || string(data[:5]) != "%PDF-" {
		return nil, fmt.Errorf("office 转 PDF：输出不是合法 PDF（前若干字节：%q）", tail(string(data), 32))
	}
	return data, nil
}

// copyFile 拷贝磁盘文件（只读源 → 临时副本）。
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// tail 截取字符串尾部 n 字节以内（诊断用，避免超长输出撑爆错误信息）。
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
