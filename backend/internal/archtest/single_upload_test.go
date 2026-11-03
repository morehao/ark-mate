package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ——— 产物溯源 P0′：上传收口守卫（A-5 / A-6，2026-11-03）———
//
// 设计 .dsh/docs/specs/2026-11-03-artifact-provenance-design.md §3.4：
// **全局只有一个收字节的 HTTP 面 = golib `ginupload.Register` 的 `/files`**；业务模块只建索引、
// 不再各自收 multipart 流。这两条守卫把该拍板变成机器判据。

// TestSingleUploadEndpoint（A-5）：全仓 `internal/` 生产源码里，接收 multipart 的**写端点**只允许：
//   - `ginupload`（golib 外部包，不在本仓——本仓不手写收字节的 handler）；
//   - `ctrcapability`（`POST /skills/import`，解包导入**定义**、字节不进 filestore，是「导入定义」
//     不是「存文件」，明确白名单例外）。
//
// 出现第二个 `c.FormFile` / `ParseMultipartForm` 的写端点即红——防「顺手又给某模块开一个 upload」。
func TestSingleUploadEndpoint(t *testing.T) {
	root := filepath.Join(backendRoot(t), "internal")
	// 逐文件允许的相对路径（相对 internal/），用 `/` 分隔。
	allowed := map[string]bool{
		// skills/import：收 zip 解包成定义（非存文件），白名单例外。
		"controller/ctrcapability/capability.go": true,
	}

	scanned := 0
	var offenders []string
	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := stripLineComments(string(raw))
		if !strings.Contains(src, "c.FormFile(") && !strings.Contains(src, "ParseMultipartForm(") {
			return nil
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if !allowed[rel] {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 internal 失败: %v", err)
	}
	// fail-closed：一个收 multipart 的点都没扫到，要么是白名单文件改名、要么扫描逻辑坏了——
	// 两种情况都该红（避免「目录改了守卫空转」）。
	if scanned == 0 {
		t.Fatalf("一个收 multipart 的写端点都没扫到——白名单文件可能改名，或守卫失效")
	}
	if len(offenders) > 0 {
		t.Errorf("收 multipart 的写端点只允许 ctrcapability（skills/import 白名单），出现越界：\n  %s\n"+
			"上传统一走 golib ginupload 的 POST /files，业务模块只建索引（设计 §3.4 A-5）",
			strings.Join(offenders, "\n  "))
	}
}

// TestBytesWrittenOnlyViaFilestore（A-6）：上传字节的唯一落点是 golib filestore
// （经 `infra/attachmentstore` / `ginupload`）。除这些之外，生产源码不得出现
// 「自己 `os.Create` / `os.WriteFile` 落上传字节」的第二条路。
//
// 判据刻意宽松：只禁「上传字节」语境下的直接写盘——`pagePublisher`（page 落盘）、
// 迁移、资产写入等**不是上传字节**的写盘不算。所以本守卫以「同一文件里同时出现
// `FormFile`/`ParseMultipartForm` 与 `os.Create`/`os.WriteFile`」为越界信号（上传 handler 自己落盘）。
func TestBytesWrittenOnlyViaFilestore(t *testing.T) {
	root := filepath.Join(backendRoot(t), "internal")
	var offenders []string
	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := stripLineComments(string(raw))
		recvMultipart := strings.Contains(src, "c.FormFile(") || strings.Contains(src, "ParseMultipartForm(")
		writesDisk := strings.Contains(src, "os.Create(") || strings.Contains(src, "os.WriteFile(")
		if recvMultipart && writesDisk {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 internal 失败: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("上传 handler 不得自己落盘（字节唯一落点是 filestore，经 attachmentstore/ginupload）：\n  %s\n"+
			"见设计 §3.4 A-6", strings.Join(offenders, "\n  "))
	}
}
