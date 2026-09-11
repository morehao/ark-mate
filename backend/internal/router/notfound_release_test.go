//go:build release

package router_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/pkg/code"
)

// TestRouterUnknownNonAPIPath release 构建下非 API 未知路径回落 SPA index.html（不是信封 404）。
//
// 与 notfound_dev_test.go 里的同名测试配对：两者断言的都是"非 API 未知路径"，期望值因构建
// 形态而异——release 内嵌了前端产物，用户刷新前端路由（如 /settings）必须拿到页面而不是
// 404；dev 没有内嵌形态，只能回信封。把差异显式写进两条 tag 测试，比让一条测试在两处
// 各自动跳过要诚实。
//
// 需要先 make web（pkg/web/dist 有产物），故由 `make test/release` 驱动。
func TestRouterUnknownNonAPIPath(t *testing.T) {
	e := newEngine()
	req := httptest.NewRequest("GET", "/settings", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("release 下未知非 API 路径应回落 index.html(200)，得到 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("应回落 HTML 页面，Content-Type=%q", ct)
	}
	if !strings.Contains(w.Body.String(), "<!doctype html") && !strings.Contains(w.Body.String(), "<!DOCTYPE html") {
		t.Fatalf("回落内容不像 index.html: %.120s", w.Body.String())
	}
}

// TestRouterUnknownAPINotSPARelease release 构建下 API 未知路径仍回信封 404，不回落 SPA。
//
// 这条守住「不要把 API 404 变成 index.html」——那会让前端把 HTML 当 JSON 解析，
// 报出与真实原因无关的解析错误。
func TestRouterUnknownAPINotSPARelease(t *testing.T) {
	e := newEngine()
	req := httptest.NewRequest("GET", "/v1/forge/nope", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("release 下未知 API 路径应 404，得到 %d", w.Code)
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("应为 JSON 信封而非 SPA，Content-Type=%q body=%.120s",
			w.Header().Get("Content-Type"), w.Body.String())
	}
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应体不是信封 JSON: %v（body=%s）", err, w.Body.String())
	}
	if env.Code != code.NotFound {
		t.Errorf("信封 code 应为 code.NotFound(%d)，得到 %d", code.NotFound, env.Code)
	}
}
