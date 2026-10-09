package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/router"
	"github.com/morehao/ark-forge/pkg/code"
)

// 未命中路由的三种去向（M2-2 把历史上按 build tag 分叉的两条测试合并成一组）：
//
//	有资产  + 非 API → 回落 index.html（SPA 深链）
//	无资产  + 非 API → 标准信封 404
//	有资产  + API   → 仍然信封 404（**永不**回落：把 API 404 变成 HTML 会让前端把 HTML 当 JSON 解析）
//
// 为什么不再按 tag 分叉：资产来源从 M2-1 起是**运行时解析**的（内置 / --web-dir / 没有），
// 而"能不能回落"取决于"有没有资产"，与构建形态只是间接相关。用显式装配出两种形态，
// 一条 tag 下就把三件事都钉死——比两条各跳过一半的 tag 测试更能说明问题。

// noAssetsEngine 只挂 API、没有任何前端资产的引擎。
func noAssetsEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	router.MountWeb(eng, router.AssetInfo{}) // 零值 = 无资产
	return eng
}

// dirAssetsEngine 用一份临时 dist 目录挂载资产的引擎。
func dirAssetsEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := filepath.Join(t.TempDir(), "dist")
	writeDistLayout(t, dir)
	info, err := router.ResolveAssets(dir)
	if err != nil {
		t.Fatalf("ResolveAssets: %v", err)
	}
	eng := gin.New()
	router.MountWeb(eng, info)
	return eng
}

// TestUnknownNonAPIPathFallsBackWhenAssetsPresent 有资产 → 非 API 未知路径回落 index.html。
func TestUnknownNonAPIPathFallsBackWhenAssetsPresent(t *testing.T) {
	e := dirAssetsEngine(t)
	for _, p := range []string{"/settings", "/conversations/abc", "/index.html"} {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != 200 {
			t.Fatalf("%s 应回落 index.html(200)，得到 %d", p, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("%s 应回落 HTML 页面，Content-Type=%q", p, ct)
		}
		if !strings.Contains(w.Body.String(), "probe-app") {
			t.Fatalf("%s 回落内容不像 index.html: %.80s", p, w.Body.String())
		}
		// 回落出来的仍是"文档"：不缓存（否则发版后页面还引用旧 chunk）。
		if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("%s 的回落响应应为 no-store，得到 %q", p, cc)
		}
	}
}

// TestUnknownNonAPIPathEnvelopeWhenNoAssets 无资产 → 非 API 未知路径也回标准信封 404。
//
// 历史上 dev 形态不注册 NoRoute，请求落到 gin 默认的纯文本 "404 page not found"；
// 现在无论有没有资产，非 API 未知路径至少都是**带注册表 code 的信封**（与 release 形态一致）。
func TestUnknownNonAPIPathEnvelopeWhenNoAssets(t *testing.T) {
	e := noAssetsEngine()
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("应回 HTTP 404，得到 %d（body=%s）", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("无资产时未知非 API 路径必须是 JSON 信封，Content-Type=%q body=%s", ct, w.Body.String())
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应体不是信封 JSON: %v（body=%s）", err, w.Body.String())
	}
	if env.Code != code.NotFound {
		t.Errorf("信封 code 应为 code.NotFound(%d)，得到 %d", code.NotFound, env.Code)
	}
}

// TestUnknownAPIPathNeverFallsBack 有资产时 `/v1/*` 未注册路径**仍然**回信封 404。
func TestUnknownAPIPathNeverFallsBack(t *testing.T) {
	e := dirAssetsEngine(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/forge/nope", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("未知 API 路径应 404，得到 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("API 404 必须是 JSON 信封，Content-Type=%q", ct)
	}
	if strings.Contains(w.Body.String(), "probe-app") {
		t.Fatal("API 404 回落成了 index.html —— 前端会把 HTML 当 JSON 解析")
	}
}
