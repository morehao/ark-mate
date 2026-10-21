package router_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/adapters/inbound/http/router"
)

// writeDistLayout 造一份最小的 dist 目录（index.html + assets/<hash>.js）。
func writeDistLayout(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string]string{
		"index.html":          "<!doctype html><html><body>probe-app</body></html>",
		"assets/index-abc.js": "console.log('probe')",
		"favicon.svg":         "<svg/>",
		// 前端构建写出的构建 id（M2-4）：真实产物里一定有它，测试夹具也照做。
		"build-id.txt": "probe-build-id\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// TestResolveAssetsRejectsInvalidDir `--web-dir` 的失败语义（M2-1 的冻结表）。
//
// 四种非法形态各自给出**能照着修**的错误文案；调用方（app.Init）据此以退出码 3 收尾。
func TestResolveAssetsRejectsInvalidDir(t *testing.T) {
	tmp := t.TempDir()
	dirNoIndex := filepath.Join(tmp, "no-index")
	if err := os.MkdirAll(dirNoIndex, 0o755); err != nil {
		t.Fatal(err)
	}
	fileNotDir := filepath.Join(tmp, "a-file")
	if err := os.WriteFile(fileNotDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		dir     string
		wantSub string
	}{
		{"相对路径", "frontend/apps/web/dist", "绝对路径"},
		{"目录不存在", filepath.Join(tmp, "nope"), "不可用"},
		{"不是目录", fileNotDir, "不是目录"},
		{"缺 index.html", dirNoIndex, "index.html"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := router.ResolveAssets(tc.dir); err == nil {
				t.Fatalf("非法 --web-dir %q 应报错", tc.dir)
			} else if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("错误信息应含 %q（便于照着修）：%v", tc.wantSub, err)
			}
		})
	}
}

// TestResolveAssetsDirSource 合法目录 → `dir` 来源 + 绝对路径 + 可读的 FS。
func TestResolveAssetsDirSource(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dist")
	writeDistLayout(t, dir)

	info, err := router.ResolveAssets(dir)
	if err != nil {
		t.Fatalf("ResolveAssets: %v", err)
	}
	if !filepath.IsAbs(info.Path) || info.Path != dir {
		t.Errorf("AssetInfo.Path 应为绝对路径 %q，实际 %q", dir, info.Path)
	}
	if !info.Has() {
		t.Fatal("Has() 应为 true")
	}
	body, err := fs.ReadFile(info.FS, "index.html")
	if err != nil || !strings.Contains(string(body), "probe-app") {
		t.Fatalf("FS 读 index.html 失败：%v（body=%q）", err, string(body))
	}

	// 显式目录是**唯一**来源（设计 §6.1 规则）：用一个只存在于目录里的文件来证明
	// "命中的是这份目录"，而不是任何别处。
	if _, err := fs.Stat(info.FS, "assets/index-abc.js"); err != nil {
		t.Fatalf("目录来源应能读到它自己的 assets：%v", err)
	}
}

// TestResolveAssetsNoDir 未给 `--web-dir`：**拒绝启动**并指出唯一出路。
//
// 本仓从 2026-10-10 起没有第二来源可回落（`//go:embed` 已删），所以这条在任何构建形态下都一样：
// "没有资产"是错误（部署形态装错），这是 M2-1 的核心失败语义之一。
func TestResolveAssetsNoDir(t *testing.T) {
	info, err := router.ResolveAssets("")
	if err == nil {
		t.Fatalf("未给 --web-dir 时必须报错，实际拿到 %+v", info)
	}
	for _, want := range []string{"--web-dir", "MATE_WEB_DIR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息应指出出路（含 %q）：%v", want, err)
		}
	}
	if info.Has() {
		t.Error("报错时不该同时给出可用资产")
	}
}

// TestMountWebServesDirAssets 目录资产的挂载行为（M2-1；缓存头与 404 的完整矩阵在 M2-2）。
func TestMountWebServesDirAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := filepath.Join(t.TempDir(), "dist")
	writeDistLayout(t, dir)
	info, err := router.ResolveAssets(dir)
	if err != nil {
		t.Fatalf("ResolveAssets: %v", err)
	}

	eng := gin.New()
	router.MountWeb(eng, info)
	do := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		eng.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	if w := do("/"); w.Code != 200 || !strings.Contains(w.Body.String(), "probe-app") {
		t.Errorf("GET / 应回 index.html：%d %q", w.Code, w.Body.String()[:min(60, len(w.Body.String()))])
	}
	if w := do("/index.html"); w.Code != 200 || !strings.Contains(w.Body.String(), "probe-app") {
		t.Errorf("GET /index.html 应回 index.html：%d", w.Code)
	}
	if w := do("/assets/index-abc.js"); w.Code != 200 || !strings.Contains(w.Body.String(), "probe") {
		t.Errorf("GET /assets/* 应回原文件：%d", w.Code)
	}
	if w := do("/favicon.svg"); w.Code != 200 {
		t.Errorf("GET /favicon.svg 应回原文件：%d", w.Code)
	}
	// SPA 深链回落（M2-2 会补完整断言；这里先钉"目录形态也有回退"）。
	if w := do("/sessions/abc"); w.Code != 200 || !strings.Contains(w.Body.String(), "probe-app") {
		t.Errorf("非 API 未知路径应回落 index.html：%d", w.Code)
	}
	// API 未注册路径**不**回落（把 API 404 变成 HTML 会让前端把 HTML 当 JSON 解析）。
	if w := do("/v1/mate/nope"); w.Code != 404 || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("API 未知路径应回 JSON 信封 404：%d %s", w.Code, w.Header().Get("Content-Type"))
	}
}

// TestResolveAssetsReadsBuildID 资产层读**这份**资产里的 build-id.txt（M2-4，设计 §6.7）。
//
// 读不到不是错误（手工 dist / 旧构建都可能没有），报 `unknown` 并记警告——
// 版本闸门据此判"无法校验"（放行），而不是把用户挡在状态页外面。
func TestResolveAssetsReadsBuildID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dist")
	writeDistLayout(t, dir)
	info, err := router.ResolveAssets(dir)
	if err != nil {
		t.Fatalf("ResolveAssets: %v", err)
	}
	if info.BuildID != "probe-build-id" {
		t.Fatalf("应从产物里读到构建 id，实际 %q", info.BuildID)
	}

	// 换一份没有 build-id.txt 的产物：不报错，报 unknown。
	bare := filepath.Join(t.TempDir(), "bare")
	if err := os.MkdirAll(filepath.Join(bare, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bare, "index.html"), []byte("<html>bare</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	bareInfo, err := router.ResolveAssets(bare)
	if err != nil {
		t.Fatalf("缺 build-id.txt 不该导致解析失败：%v", err)
	}
	if bareInfo.BuildID != router.BuildIDUnknown {
		t.Fatalf("缺 build-id.txt 应报 %q，实际 %q", router.BuildIDUnknown, bareInfo.BuildID)
	}

	// 空文件同样按 unknown（不能让一个空行冒充"一致"）。
	empty := filepath.Join(t.TempDir(), "empty-id")
	writeDistLayout(t, empty)
	if err := os.WriteFile(filepath.Join(empty, "build-id.txt"), []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyInfo, err := router.ResolveAssets(empty)
	if err != nil {
		t.Fatal(err)
	}
	if emptyInfo.BuildID != router.BuildIDUnknown {
		t.Fatalf("空白 build-id.txt 应报 %q，实际 %q", router.BuildIDUnknown, emptyInfo.BuildID)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// copyTree 递归复制目录（测试用：验证"同一份产物换路径后摘要不变"）。
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
