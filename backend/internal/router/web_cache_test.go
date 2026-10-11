package router_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/router"
)

// TestStaticCacheMatrix 三档缓存语义 + 内容 ETag + 条件请求（设计 §6.2，M2-2 的核心侧）。
//
// 规则（两侧一致，M2-5 会拿壳反代再打一遍同一个矩阵逐条比对）：
//
//	/index.html（与 `/`、SPA 回落） → no-store（发版后必须拿新的）
//	/assets/*                       → public, immutable, max-age=31536000（文件名含内容 hash）
//	其余静态文件                     → no-cache（可缓存但每次回源校验）
//
// ETag 用**内容摘要**而不是 mtime：内嵌资产的 mtime 是零值、目录来源的 mtime 因机器而异，
// 只有内容摘要能让两个来源给出同一个值（V7 比的就是这个）。
func TestStaticCacheMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := filepath.Join(t.TempDir(), "dist")
	writeDistLayout(t, dir)
	info, err := router.ResolveAssets(dir)
	if err != nil {
		t.Fatalf("ResolveAssets: %v", err)
	}
	if info.Digest == "" || len(info.Digests) < 3 {
		t.Fatalf("解析期应算出逐文件摘要与合成摘要：digest=%q files=%d", info.Digest, len(info.Digests))
	}
	eng := gin.New()
	router.MountWeb(eng, info)

	do := func(path string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		eng.ServeHTTP(w, req)
		return w
	}

	cases := []struct {
		path      string
		wantCache string
		wantETag  bool
	}{
		{"/", "no-store", false},
		{"/index.html", "no-store", false},
		{"/sessions/deep-link", "no-store", false}, // SPA 回落出来的也是文档
		{"/assets/index-abc.js", "public, immutable, max-age=31536000", true},
		{"/favicon.svg", "no-cache", true},
	}
	for _, tc := range cases {
		w := do(tc.path, nil)
		if w.Code != 200 {
			t.Errorf("%s 期望 200，得到 %d", tc.path, w.Code)
			continue
		}
		if got := w.Header().Get("Cache-Control"); got != tc.wantCache {
			t.Errorf("%s 的 Cache-Control = %q，期望 %q", tc.path, got, tc.wantCache)
		}
		etag := w.Header().Get("ETag")
		if tc.wantETag && etag == "" {
			t.Errorf("%s 应带内容 ETag", tc.path)
		}
		if !tc.wantETag && etag != "" {
			t.Errorf("%s 不该带 ETag（no-store 下没有意义），得到 %q", tc.path, etag)
		}
		// Last-Modified 刻意不设（内容 ETag 已足够；零值/来源相关的 mtime 只会误导缓存）。
		if lm := w.Header().Get("Last-Modified"); lm != "" {
			t.Errorf("%s 不该有 Last-Modified（两个资产来源的 mtime 不一致），得到 %q", tc.path, lm)
		}
	}

	// 条件请求：命中 ETag → 304 且无 body。
	first := do("/assets/index-abc.js", nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("assets 没拿到 ETag，条件请求无从验证")
	}
	w304 := do("/assets/index-abc.js", map[string]string{"If-None-Match": etag})
	if w304.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match 命中应回 304，得到 %d", w304.Code)
	}
	if w304.Body.Len() != 0 {
		t.Fatalf("304 不应带 body，得到 %d 字节", w304.Body.Len())
	}
	// 未命中 → 200 全量。
	if w := do("/assets/index-abc.js", map[string]string{"If-None-Match": `"nope"`}); w.Code != 200 {
		t.Fatalf("ETag 未命中应回 200，得到 %d", w.Code)
	}
	// 弱校验前缀与多值列表都要认（HTTP 允许 `W/"x", "y"`）。
	if w := do("/assets/index-abc.js", map[string]string{"If-None-Match": `W/"other", ` + etag}); w.Code != http.StatusNotModified {
		t.Fatalf("多值/弱校验前缀应命中 304，得到 %d", w.Code)
	}
	// `*` 表示"任何版本都行"。
	if w := do("/assets/index-abc.js", map[string]string{"If-None-Match": "*"}); w.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: * 应回 304，得到 %d", w.Code)
	}
}

// TestAssetDigestIsSourceIndependent 同一份产物换到另一个路径后，合成摘要必须不变。
//
// 这是 V7「两个入口同一份产物」与 M2-5 一致性断言的基石：摘要只由内容决定，
// 与部署位置、与绝对路径、与 mtime 都无关。
// （`//go:embed` 那条来源已于 2026-10-10 删除，测试改为"复制到另一个目录"来证明位置无关性。）
func TestAssetDigestIsSourceIndependent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dist")
	writeDistLayout(t, dir)

	fromDir, err := router.ResolveAssets(dir)
	if err != nil {
		t.Fatalf("ResolveAssets(dir): %v", err)
	}
	// 复制到另一个位置（模拟"打包期把同一份 dist 放到运行时树"）：摘要必须不变。
	dir2 := filepath.Join(t.TempDir(), "another", "dist")
	if err := copyTree(dir, dir2); err != nil {
		t.Fatalf("复制产物失败: %v", err)
	}
	fromDir2, err := router.ResolveAssets(dir2)
	if err != nil {
		t.Fatalf("ResolveAssets(dir2): %v", err)
	}
	if fromDir.Digest != fromDir2.Digest {
		t.Fatalf("同一份产物换了路径后摘要变了：%s vs %s", fromDir.Digest, fromDir2.Digest)
	}
}
