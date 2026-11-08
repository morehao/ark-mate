package router_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/morehao/ark-mate/internal/infra/dsh"
)

// TestConvertPDFEndpoint 端到端：真引擎（真 filestore + 真附件字节存储）里
//   ① POST /files 上传一份真实 docx → file_id；
//   ② POST /files/:id/convert-pdf → 直接回 `application/pdf` 且字节以 `%PDF-` 开头。
//
// 依赖随包 libreoffice-kit payload（node + cli）。payload 缺失时 Skip——本用例的意义是在能力就位的
// 机器上**实跑反证整条链**（handler → service → 真 CLI），不把 payload 变成测试硬依赖。
func TestConvertPDFEndpoint(t *testing.T) {
	// 仓库内随包 payload（desktop 的资源目录）——用它把 MATE_OFFICE_NODE/CLI 解析出来，让装配根
	// 的 newOfficeConverter 也走同一套路径。找不到就 Skip（不在无 payload 的机器上当硬依赖）。
	payloads := payloadsRoot()
	if payloads == "" {
		t.Skip("仓库内未找到 desktop 随包 payload，跳过 convert-pdf 端到端用例")
	}
	t.Setenv("MATE_DSH_BUNDLED_RUNTIME", filepath.Join(payloads, "primary-runtime"))
	t.Setenv("MATE_DSH_BUNDLED_OFFICE_SKILLS",
		filepath.Join(payloads, "dsh", "0.2.2-rc.1", "node_modules", "@deepseek-ai", "dsh-skill-office", "assets"))

	primaryRuntime, officeSkills := dsh.ResolveBundledDirs()
	node := os.Getenv("MATE_OFFICE_NODE")
	if node == "" {
		node = dsh.ResolveOfficeNode(primaryRuntime)
	}
	cli := os.Getenv("MATE_OFFICE_CLI")
	if cli == "" {
		cli = dsh.ResolveOfficeCLI(officeSkills)
	}
	if node == "" || cli == "" {
		t.Skip("随包 office payload（node/libreoffice-kit）缺失，跳过 convert-pdf 端到端用例")
	}
	// 让装配根（newOfficeConverter）解析到同一套路径。
	t.Setenv("MATE_OFFICE_NODE", node)
	t.Setenv("MATE_OFFICE_CLI", cli)

	e, _ := materialApp(t)

	cases := []struct {
		name    string
		content []byte
	}{
		{"preview.docx", templateBytes(t, "docx", "docx")},
		{"preview.pptx", templateBytes(t, "pptx", "pptx")},
	}
	ran := 0
	for _, c := range cases {
		if c.content == nil {
			t.Logf("跳过 %s：找不到源模板", c.name)
			continue
		}
		convertAndAssertPDF(t, e, c.name, c.content)
		ran++
	}
	if ran == 0 {
		t.Skip("docx/pptx 源模板都缺失，跳过")
	}
}

// convertAndAssertPDF 上传 name/content 到 POST /files 拿 file_id，再 POST convert-pdf，
// 断言响应是 `application/pdf` 且字节以 `%PDF-` 开头。
func convertAndAssertPDF(t *testing.T, e *gin.Engine, name string, content []byte) {
	t.Helper()
	sum := sha256.Sum256(content)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("content_hash", hex.EncodeToString(sum[:]))
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write(content)
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/mate/files", &buf)
	req.Header.Set("content-type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("上传 %s HTTP %d: %s", name, w.Code, w.Body.String())
	}
	var fileEnv struct {
		Code int `json:"code"`
		Data struct {
			FileID string `json:"file_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &fileEnv)
	if fileEnv.Code != 0 || fileEnv.Data.FileID == "" {
		t.Fatalf("上传 %s code=%d body=%s", name, fileEnv.Code, w.Body.String())
	}

	req2 := httptest.NewRequest("POST", "/v1/mate/files/"+fileEnv.Data.FileID+"/convert-pdf?name="+name, nil)
	w2 := httptest.NewRecorder()
	e.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("convert-pdf(%s) HTTP %d: %s", name, w2.Code, w2.Body.String())
	}
	if ct := w2.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("Content-Type 应为 application/pdf，得到 %q", ct)
	}
	body := w2.Body.Bytes()
	if len(body) < 5 || string(body[:5]) != "%PDF-" {
		t.Fatalf("%s 响应不是合法 PDF（前若干字节 %q）", name, string(body[:min(16, len(body))]))
	}
	t.Logf("convert-pdf 端到端成功：%s file_id=%s，PDF %d 字节", name, fileEnv.Data.FileID, len(body))
}

// templateBytes 返回随包 python-docx / python-pptx 的默认模板字节（kind = "docx"/"pptx"）。
// 拿不到返回 nil。
func templateBytes(t *testing.T, kind, ext string) []byte {
	t.Helper()
	payloads := payloadsRoot()
	if payloads == "" {
		return nil
	}
	p := filepath.Join(payloads, "primary-runtime", "dependencies", "python", "lib", "python3.12",
		"site-packages", kind, "templates", "default."+ext)
	if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
		return b
	}
	return nil
}

// payloadsRoot 从测试文件所在目录向上找到仓库内的 desktop 随包 payload 根（<repo>/frontend/apps/
// desktop/resources/payloads）。找不到返回空串。
func payloadsRoot() string {
	// 当前工作目录 = backend/internal/router；上溯到仓库根再拼 desktop payload 路径。
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := wd
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, "frontend", "apps", "desktop", "resources", "payloads")
		if fi, err := os.Stat(filepath.Join(cand, "primary-runtime", "runtime.json")); err == nil && !fi.IsDir() {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
