//go:build !release

package router_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/pkg/code"
)

// TestRouterUnknownNonAPIPath 非 release 构建下非 API 未知路径也回标准信封 404。
//
// 历史上 dev 形态不注册 NoRoute，请求落到 gin 默认的纯文本 "404 page not found"：
// 与 release 形态不一致，也让「所有对外错误都走信封、都带注册表里的 code」在开发态漏了
// 一角——而开发态恰恰最常被 curl。dev 没有内嵌前端，因此没有可回落的页面，回信封是唯一
// 自洽的答案（release 那边的同一测试断言回落 index.html）。
func TestRouterUnknownNonAPIPath(t *testing.T) {
	e := newEngine()
	req := httptest.NewRequest("GET", "/nope", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("应回 HTTP 404，得到 %d（body=%s）", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("dev 下未知非 API 路径也必须是 JSON 信封，Content-Type=%q body=%s", ct, w.Body.String())
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
