package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSystemRoutes 系统面（M1-7）：`/meta` 与 `/capabilities` 挂在真实装配路径上可取。
//
// 这里着重钉两件事：
//  1. `webSource` 随**构建形态**变化——release 构建内嵌了前端资产（builtin），默认构建没有；
//     期望值由 build tag 决定（见 meta_web_source_*_test.go），因此 `make test` 与
//     `make test/release` 各自验证自己那一支；
//  2. `X-Forge-Carrier` 决定能力面的 available（壳的反代带这个头，浏览器入口没有）。
func TestSystemRoutes(t *testing.T) {
	eng := newEngine()

	getData := func(path string, headers map[string]string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		eng.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s HTTP %d：%s", path, w.Code, w.Body.String())
		}
		var env struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s 信封解析失败：%v", path, err)
		}
		if env.Code != 0 {
			t.Fatalf("%s 业务码 = %d", path, env.Code)
		}
		return env.Data
	}

	meta := getData("/v1/forge/meta", nil)
	if meta["apiContract"] != "v1" {
		t.Errorf("/meta 的 apiContract = %v，期望 v1", meta["apiContract"])
	}
	if cv, _ := meta["coreVersion"].(string); cv == "" {
		t.Error("/meta 的 coreVersion 不该为空（未注入 ldflags 时至少是 dev）")
	}
	if got := meta["webSource"]; got != nil && got != wantWebSource {
		t.Errorf("/meta 的 webSource = %v，本构建形态下期望 %q", got, wantWebSource)
	}
	if wantWebSource != "" && meta["webSource"] != wantWebSource {
		t.Errorf("本构建形态应报 webSource=%q，实际 %v", wantWebSource, meta["webSource"])
	}

	caps := getData("/v1/forge/capabilities", nil)
	entries, _ := caps["entries"].(map[string]any)
	if entries["desktop"] != false {
		t.Errorf("无 X-Forge-Carrier 头时应按浏览器入口：%v", entries)
	}
	capsDesk := getData("/v1/forge/capabilities", map[string]string{"X-Forge-Carrier": "desktop"})
	entriesDesk, _ := capsDesk["entries"].(map[string]any)
	if entriesDesk["desktop"] != true {
		t.Errorf("带 X-Forge-Carrier: desktop 时应按桌面入口：%v", entriesDesk)
	}
}

// TestSystemRoutesRequireCredentials 两个端点都在 rest 面凭据之后（只有 /health 免鉴权）。
//
// 交叉引用：`TestCredentialCoversAllRestRoutes` 会枚举**所有**注册路由逐条断言这一点；
// 这条则把"这两个端点确实注册了、且确实被拦"钉在同一个用例里，改动分组时更好定位。
func TestSystemRoutesRequireCredentials(t *testing.T) {
	eng := newAuthEngine(standaloneCreds("secret-token"))
	for _, path := range []string{"/v1/forge/meta", "/v1/forge/capabilities"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		eng.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Errorf("%s 无令牌应 401，实际 %d", path, w.Code)
		}
	}
}
