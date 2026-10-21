package ctrsystem_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/morehao/ark-mate/internal/service/svcsystem"
)

// rawMeta 取 `/meta` 的原始 JSON（要断言"字段在不在"，不能用 map 解完再查零值）。
func rawMeta(t *testing.T, src svcsystem.MetaSource) map[string]json.RawMessage {
	t.Helper()
	eng := newEngine(src, false)
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/meta", nil))
	if w.Code != 200 {
		t.Fatalf("HTTP %d：%s", w.Code, w.Body.String())
	}
	var env struct {
		Code int                        `json:"code"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("信封解析失败：%v（%s）", err, w.Body.String())
	}
	return env.Data
}

// TestMetaSnapshotShape `/meta` 的两个形状约定（M2-3）：可空字段恒出现、数组恒为数组。
//
// 为什么值得单独钉：客户端会写 `data.webPath === null` 或 `'webPath' in data` 两种判断，
// 而"缺字段"与"显式 null"在 JS 里行为不同（`in` 为 false vs true）。契约里 `webPath`/`webDigest`
// 写的是 `…|null`，因此必须**恒出现**。
func TestMetaSnapshotShape(t *testing.T) {
	// ① 无资产（只服务 API 的装配）：webPath/webDigest 是显式 null，payloads 是 []。
	d := rawMeta(t, svcsystem.MetaSource{CoreVersion: "dev", APIContract: "v1", Home: "/tmp/home"})
	if _, ok := d["webSource"]; ok {
		t.Errorf("2026-10-10 起不该再有 webSource 字段，实际存在：%s", d["webSource"])
	}
	for _, k := range []string{"webPath", "webDigest"} {
		raw, ok := d[k]
		if !ok {
			t.Errorf("%s 必须出现（显式 null），缺字段会让客户端两种判断各说各话", k)
			continue
		}
		if string(raw) != "null" {
			t.Errorf("%s 应为 null，实际 %s", k, raw)
		}
	}
	if string(d["payloads"]) != "[]" {
		t.Errorf("payloads 应为空数组而不是 null，实际 %s", d["payloads"])
	}

	// ② 有资产：webPath 是那份目录的绝对路径、webDigest 是 sha256:…、webBuildId 现场读。
	d2 := rawMeta(t, svcsystem.MetaSource{
		CoreVersion: "dev", APIContract: "v1", Home: "/tmp/home",
		Assets: svcsystem.MetaAssets{Path: "/opt/mate/web", Digest: "sha256:abc", BuildID: "bid-1"},
	})
	var path, digest, buildID string
	_ = json.Unmarshal(d2["webPath"], &path)
	_ = json.Unmarshal(d2["webDigest"], &digest)
	_ = json.Unmarshal(d2["webBuildId"], &buildID)
	if path != "/opt/mate/web" || digest != "sha256:abc" || buildID != "bid-1" {
		t.Errorf("资产字段投影不符：path=%q digest=%q buildId=%q", path, digest, buildID)
	}

	// ③ 摘要而没有路径（理论上不会出现，客户端仍要能解析）：webPath 保持 null、摘要照发。
	d3 := rawMeta(t, svcsystem.MetaSource{
		CoreVersion: "dev", APIContract: "v1",
		Assets: svcsystem.MetaAssets{Digest: "sha256:def"},
	})
	if string(d3["webPath"]) != "null" {
		t.Errorf("没有路径时 webPath 应为 null，实际 %s", d3["webPath"])
	}
	if string(d3["webDigest"]) != `"sha256:def"` {
		t.Errorf("摘要照发，实际 %s", d3["webDigest"])
	}
}
