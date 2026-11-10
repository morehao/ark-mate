package router_test

// 二期 C · WP-C7：运行期应用沙箱页静态端点 `/_app/<token>/` 的端到端判据。
//
// 覆盖点（对齐计划 §10 WP-C7 验收）：
//  1. 合法 token → 取到包内 page/ 下的文件（含默认 index.html）。
//  2. 响应带**隔离 CSP**（connect-src 'none'）——页面零出站。
//  3. 伪造/篡改 token → 404（不可猜 + 常量时间校验）。
//  4. 路径穿越（`..`）→ 404（只 serve page/ 之内）。
//  5. page/ 之外的文件不可达。
//  6. Deps.AppPages=nil → 端点不挂（走 SPA 回落，不误暴露）。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/router"
)

// writeAppPage 在临时 apps 根下造一个 `<id>/page/<rel>` 文件，返回文件内容。
func writeAppPage(t *testing.T, appsDir, appID, rel, content string) {
	t.Helper()
	full := filepath.Join(appsDir, appID, "page", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

func getPath(t *testing.T, eng http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
	return w
}

func TestAppPageServesUnderTokenWithIsolationCSP(t *testing.T) {
	appsDir := t.TempDir()
	writeAppPage(t, appsDir, "demo-app", "index.html", "<!doctype html><h1>demo</h1>")
	writeAppPage(t, appsDir, "demo-app", "assets/app.js", "console.log('app')")

	assets := router.NewAppPageAssets(appsDir)
	eng := router.New(testRouterDeps(t, testRouterOpts{appPages: assets}))

	tok := assets.Token("demo-app")

	// 1. 默认 index.html。
	w := getPath(t, eng, "/_app/"+tok+"/index.html")
	if w.Code != http.StatusOK {
		t.Fatalf("合法 token 取 index.html 期望 200，得 %d（body=%q）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "demo") {
		t.Errorf("index.html 内容不符：%q", w.Body.String())
	}
	// 2. 隔离 CSP：connect-src 'none'。
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'none'") {
		t.Errorf("沙箱页 CSP 缺 connect-src 'none'（页面不得自带网络出站）：%q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("沙箱页 CSP 缺 frame-ancestors 'self'：%q", csp)
	}
	// 尾斜杠 → index.html。
	if w2 := getPath(t, eng, "/_app/"+tok+"/"); w2.Code != http.StatusOK {
		t.Errorf("尾斜杠形态期望 200，得 %d", w2.Code)
	}
	// 子资源可读。
	if w3 := getPath(t, eng, "/_app/"+tok+"/assets/app.js"); w3.Code != http.StatusOK {
		t.Errorf("子资源期望 200，得 %d", w3.Code)
	}
}

func TestAppPageRejectsBadTokenAndTraversal(t *testing.T) {
	appsDir := t.TempDir()
	writeAppPage(t, appsDir, "demo-app", "index.html", "ok")
	// page/ 之外的文件（manifest.json 同级 / secret.txt）绝不能被 serve。
	if err := os.WriteFile(filepath.Join(appsDir, "demo-app", "secret.txt"), []byte("TOPSECRET"), 0o644); err != nil {
		t.Fatalf("写 secret: %v", err)
	}

	assets := router.NewAppPageAssets(appsDir)
	eng := router.New(testRouterDeps(t, testRouterOpts{appPages: assets}))
	good := assets.Token("demo-app")

	// 伪造 token（改签名）→ 404。
	bad := good[:len(good)-1] + "0"
	if bad == good {
		bad = good[:len(good)-1] + "1"
	}
	if w := getPath(t, eng, "/_app/"+bad+"/index.html"); w.Code != http.StatusNotFound {
		t.Errorf("篡改 token 期望 404，得 %d", w.Code)
	}
	// 无签名 / 乱码 token → 404。
	if w := getPath(t, eng, "/_app/not-a-token/index.html"); w.Code != http.StatusNotFound {
		t.Errorf("乱码 token 期望 404，得 %d", w.Code)
	}

	// 路径穿越：`..` 跳出 page/ 读 secret.txt → 404（gin 会把 {filepath} 收成含 .. 的段）。
	for _, p := range []string{
		"/_app/" + good + "/../secret.txt",
		"/_app/" + good + "/%2e%2e/secret.txt",
		"/_app/" + good + "/..%2fsecret.txt",
	} {
		if w := getPath(t, eng, p); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "TOPSECRET") {
			t.Errorf("路径穿越 %q 泄露了 page/ 之外的 secret.txt（得 200）", p)
		}
	}
	// 直接读同级 secret.txt（用合法 token 但指向 page/ 外）也应 404。
	if w := getPath(t, eng, "/_app/"+good+"/../../secret.txt"); w.Code != http.StatusNotFound {
		t.Errorf("跨目录读取期望 404，得 %d", w.Code)
	}
}

func TestAppPageNotMountedWhenNil(t *testing.T) {
	// AppPages=nil（默认）：`/_app/...` 走 SPA 回落，不应命中我们的端点。
	eng := router.New(testRouterDeps(t, testRouterOpts{}))
	w := getPath(t, eng, "/_app/whatever/index.html")
	// 无资产时 NoRoute 给信封 404；关键是**不会是**我们端点的隔离 CSP 响应。
	if strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'none'") {
		t.Errorf("AppPages=nil 时端点仍被挂上（不该下发沙箱页 CSP）")
	}
}
