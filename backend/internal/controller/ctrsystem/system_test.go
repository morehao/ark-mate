package ctrsystem_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrsystem"
)

// newEngine 挂上系统面控制器（最小引擎：本包只关心传输形状）。
//
// `nativeDialog` = 宿主能不能抬系统目录面板（v14.5 起由装配层注入的宿主事实给出）。
func newEngine(meta ctrsystem.MetaSource, nativeDialog bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	// 控制器吃的是 provider（每次都渲染当前状态）；测试里固定返回同一份值。
	ctr := ctrsystem.NewSystemCtr(func() ctrsystem.MetaSource { return meta }, func() bool { return nativeDialog })
	eng.GET("/meta", ctr.GetMeta)
	eng.GET("/capabilities", ctr.GetCapabilities)
	return eng
}

// hostResolvedCapability 该能力的可用性由**宿主事实**决定，而不是"客户端是不是桌面壳"。
// 目前只有 native-dialog 一条（面板是核心进程抬的，本机浏览器也能用）。
func hostResolvedCapability(id string) bool { return id == "native-dialog" }

// get 发一次请求并解出信封里的 data。
func get(t *testing.T, eng *gin.Engine, path string, headers map[string]string) map[string]any {
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
		t.Fatalf("%s 信封解析失败：%v（%s）", path, err, w.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("%s 业务码 = %d", path, env.Code)
	}
	return env.Data
}

// TestMetaShape /meta 的字段契约（设计 §6.7 / 计划 §5 M1-7）。
func TestMetaShape(t *testing.T) {
	eng := newEngine(ctrsystem.MetaSource{CoreVersion: "1.2.3", APIContract: "v1", Home: "/tmp/mate-home"}, false)
	d := get(t, eng, "/meta", nil)

	for _, k := range []string{"coreVersion", "webBuildId", "apiContract", "home", "payloads"} {
		if _, ok := d[k]; !ok {
			t.Errorf("/meta 缺字段 %q（键集：%v）", k, keysOf(d))
		}
	}
	if d["coreVersion"] != "1.2.3" || d["apiContract"] != "v1" || d["home"] != "/tmp/mate-home" {
		t.Errorf("/meta 值不符：%v", d)
	}
	// webPath / webDigest 是**显式 null**（不是缺字段）：客户端按"可空字段"处理。
	if v, ok := d["webPath"]; !ok || v != nil {
		t.Errorf("webPath 应为显式 null，实际 %v（存在=%v）", v, ok)
	}
	if v, ok := d["webDigest"]; !ok || v != nil {
		t.Errorf("webDigest 应为显式 null，实际 %v（存在=%v）", v, ok)
	}
	// payloads 必须是空数组而不是 null（否则前端要为两种情况写分支）。
	ps, ok := d["payloads"].([]any)
	if !ok {
		t.Fatalf("payloads 应为数组，实际 %T", d["payloads"])
	}
	if len(ps) != 0 {
		t.Errorf("M1-7 阶段 payloads 应为空，实际 %v", ps)
	}
	// 令牌绝不进诊断面。
	if _, ok := d["restToken"]; ok {
		t.Error("/meta 里出现了 restToken")
	}
	if _, ok := d["streamToken"]; ok {
		t.Error("/meta 里出现了 streamToken")
	}
}

// TestCapabilitiesPerEntry 两个入口**同构**：id 集合与顺序完全一致，只有 available 不同。
//
// 这是 V3/V18 的落点：声明面是两端的机器判据，因此"顺序"也在契约里（前端可以按下标渲染）。
func TestCapabilitiesPerEntry(t *testing.T) {
	eng := newEngine(ctrsystem.MetaSource{CoreVersion: "dev", APIContract: "v1"}, true)
	browser := get(t, eng, "/capabilities", nil)
	desktop := get(t, eng, "/capabilities", map[string]string{"X-Mate-Carrier": "desktop"})

	// 冻结顺序（设计 §7.3 的表序）。
	wantIDs := []string{"tray", "native-dialog", "open-path", "diagnostics-export", "deep-link", "update"}
	wantFallbacks := []string{
		"none", "server:directory-browse", "ui:show-path-copy",
		"http:download", "url:direct", "deployer:replace-binary",
	}

	// wantAvailable 是"非宿主判定能力"的期望；native-dialog 单独按宿主事实断言。
	check := func(name string, d map[string]any, wantAvailable bool, wantSource bool, nativeDialog bool) {
		entries, _ := d["entries"].(map[string]any)
		if entries["browser"] != true {
			t.Errorf("%s：entries.browser 应为 true（核心始终提供同一套 REST 契约）", name)
		}
		if entries["desktop"] != wantSource {
			t.Errorf("%s：entries.desktop 应为 %v，实际 %v", name, wantSource, entries["desktop"])
		}
		list, _ := d["capabilities"].([]any)
		if len(list) != len(wantIDs) {
			t.Fatalf("%s：能力条数 %d，期望 %d", name, len(list), len(wantIDs))
		}
		for i, raw := range list {
			e, _ := raw.(map[string]any)
			if e["id"] != wantIDs[i] {
				t.Errorf("%s：第 %d 条 id = %v，期望 %q（顺序也是契约）", name, i, e["id"], wantIDs[i])
			}
			want := wantAvailable
			if hostResolvedCapability(wantIDs[i]) {
				want = nativeDialog
			}
			if e["available"] != want {
				t.Errorf("%s：%s 的 available 应为 %v，实际 %v", name, wantIDs[i], want, e["available"])
			}
			if e["browserFallback"] != wantFallbacks[i] {
				t.Errorf("%s：%s 的 browserFallback = %v，期望 %q", name, wantIDs[i], e["browserFallback"], wantFallbacks[i])
			}
			// V3 的机器判据：只有纯增强的 tray 允许 none。
			if e["browserFallback"] == "none" && wantIDs[i] != "tray" {
				t.Errorf("%s：%s 的 browserFallback 不得为 none（V3：可用能力必须有浏览器等价路径）", name, wantIDs[i])
			}
		}
	}

	// 宿主**能**抬面板：native-dialog 两个入口都 true——这正是"本机浏览器也能用系统面板"，
	// 也是与参照实现对齐的那一点（判定在宿主侧，不在客户端形态）。
	check("浏览器入口（宿主可抬面板）", browser, false, false, true)
	check("桌面入口（宿主可抬面板）", desktop, true, true, true)

	// 宿主**抬不起来**（远程 / SSH / 无显示会话）：两个入口都退化成 browse，
	// 其余能力仍按入口形态判——两类判据互不干扰。
	engNo := newEngine(ctrsystem.MetaSource{CoreVersion: "test"}, false)
	check("浏览器入口（宿主抬不起来）", get(t, engNo, "/capabilities", nil), false, false, false)
	check("桌面入口（宿主抬不起来）", get(t, engNo, "/capabilities", map[string]string{"X-Mate-Carrier": "desktop"}), true, true, false)

	// 同构断言：两次响应的 id 序列必须逐字相同。
	idsOf := func(d map[string]any) []string {
		list, _ := d["capabilities"].([]any)
		out := make([]string, 0, len(list))
		for _, raw := range list {
			e, _ := raw.(map[string]any)
			out = append(out, e["id"].(string))
		}
		return out
	}
	a, b := idsOf(browser), idsOf(desktop)
	if len(a) != len(b) {
		t.Fatalf("两个入口的 id 数量不同：%v vs %v", a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("两个入口的 id 序列不同：%v vs %v", a, b)
		}
	}
}

// TestCarrierHeaderCaseInsensitive 入口标识大小写不敏感（HTTP 头值的大小写不该决定能力面）。
func TestCarrierHeaderCaseInsensitive(t *testing.T) {
	eng := newEngine(ctrsystem.MetaSource{CoreVersion: "dev", APIContract: "v1"}, false)
	d := get(t, eng, "/capabilities", map[string]string{"X-Mate-Carrier": "Desktop"})
	entries, _ := d["entries"].(map[string]any)
	if entries["desktop"] != true {
		t.Fatalf("Desktop（首字母大写）应被识别为桌面入口：%v", entries)
	}
	// 其它值一律按浏览器入口。
	d2 := get(t, eng, "/capabilities", map[string]string{"X-Mate-Carrier": "web"})
	e2, _ := d2["entries"].(map[string]any)
	if e2["desktop"] != false {
		t.Fatalf("未知入口标识应为浏览器入口：%v", e2)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
