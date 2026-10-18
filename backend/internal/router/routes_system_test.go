package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/morehao/ark-mate/internal/version"
)

// TestSystemRoutes 系统面（M1-7）：`/meta` 与 `/capabilities` 挂在真实装配路径上可取。
//
// 这里着重钉两件事：
//  1. `webSource` 字段**不再存在**（2026-10-10：核心只有"调用方给的目录"一种资产来源），
//     资产面只剩 `webPath` / `webDigest` / `webBuildId`；本装配未接资产层 → webPath 为 null；
//  2. `X-Mate-Carrier` 决定能力面的 available（壳的反代带这个头，浏览器入口没有）。
func TestSystemRoutes(t *testing.T) {
	eng := newEngine(t)

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

	meta := getData("/v1/mate/meta", nil)
	// 契约代数取 `version.APIContract` 的**当前值**（v15 起是 v2）：写死字面量会让这条断言
	// 在每次提升契约时都要人肉同步，而它真正要守的是"/meta 把契约代数如实外露了"。
	if meta["apiContract"] != version.APIContract {
		t.Errorf("/meta 的 apiContract = %v，期望 %v", meta["apiContract"], version.APIContract)
	}
	if cv, _ := meta["coreVersion"].(string); cv == "" {
		t.Error("/meta 的 coreVersion 不该为空（未注入 ldflags 时至少是 dev）")
	}
	if _, ok := meta["webSource"]; ok {
		t.Errorf("/meta 不该再有 webSource 字段（资产来源只有目录一种），实际 %v", meta["webSource"])
	}
	if _, ok := meta["webPath"]; !ok {
		t.Error("/meta 必须出现 webPath（未接资产层时是显式 null）")
	}

	caps := getData("/v1/mate/capabilities", nil)
	entries, _ := caps["entries"].(map[string]any)
	if entries["desktop"] != false {
		t.Errorf("无 X-Mate-Carrier 头时应按浏览器入口：%v", entries)
	}
	capsDesk := getData("/v1/mate/capabilities", map[string]string{"X-Mate-Carrier": "desktop"})
	entriesDesk, _ := capsDesk["entries"].(map[string]any)
	if entriesDesk["desktop"] != true {
		t.Errorf("带 X-Mate-Carrier: desktop 时应按桌面入口：%v", entriesDesk)
	}
}

// TestSystemRoutesRequireCredentials 两个端点都在 rest 面凭据之后（只有 /health 免鉴权）。
//
// 交叉引用：`TestCredentialCoversAllRestRoutes` 会枚举**所有**注册路由逐条断言这一点；
// 这条则把"这两个端点确实注册了、且确实被拦"钉在同一个用例里，改动分组时更好定位。
func TestSystemRoutesRequireCredentials(t *testing.T) {
	eng := newAuthEngine(t, standaloneCreds("secret-token"))
	for _, path := range []string{"/v1/mate/meta", "/v1/mate/capabilities"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		eng.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Errorf("%s 无令牌应 401，实际 %d", path, w.Code)
		}
	}
}
