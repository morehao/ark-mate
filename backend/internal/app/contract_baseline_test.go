package app

import (
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ——— P0：控制面行为基线（v3.1 §6.1） ———
//
// 口径：排除时间戳、随机 ID、临时路径；只冻结**契约面**——HTTP 状态、业务码、`msg`
// 的稳定取值、响应信封的键集合、`data` 的形状与键集合、集合长度。这些是前端与脚本真正
// 依赖的东西；`requestID` 每次不同，只断言"键在/不在"。
//
// 为什么要有它：P1 起装配根与控制器构造被整体重排，`internal/router` 的 85 个测试覆盖的是
// 局部行为，不是逐字契约；没有这份基线，"装配重组没改业务"只能靠人读 diff。
//
// 更新方式：`go test ./internal/app -run TestControlPlaneContractBaseline -update`
// （只在**故意**改变契约时使用；diff 必须逐条评审）。

var updateContractBaseline = flag.Bool("update", false, "更新控制面契约基线")

// contractCase 一条采样。
type contractCase struct {
	Method string
	Path   string
	Body   string
}

// contractEntry 归一化后的采样结果（**不含**requestID 的值与任何随机量）。
type contractEntry struct {
	Method   string   `json:"method"`
	Path     string   `json:"path"`
	Status   int      `json:"status"`
	Code     int      `json:"code"`
	Msg      string   `json:"msg"`
	Envelope []string `json:"envelope"`
	DataKind string   `json:"data_kind"`
	DataKeys []string `json:"data_keys,omitempty"`
	DataLen  *int     `json:"data_len,omitempty"`
}

// contractCases 是基线采样面：REST 成功 + REST 错误 + 405 + 路由不存在。
func contractCases() []contractCase {
	return []contractCase{
		// 成功面（§6.1 列出的关键路径，memory 模式下可采到的部分）
		{"GET", "/v1/mate/health", ""},
		{"GET", "/v1/mate/meta", ""},
		{"GET", "/v1/mate/sessions", ""},
		{"GET", "/v1/mate/providers", ""},
		{"GET", "/v1/mate/models", ""},
		{"GET", "/v1/mate/workspaces", ""},
		{"GET", "/v1/mate/library", ""},
		{"GET", "/v1/mate/search/messages?q=x", ""},
		{"GET", "/v1/mate/capabilities", ""},
		{"GET", "/v1/mate/settings/workspace", ""},
		{"GET", "/v1/mate/usage/summary", ""},
		{"GET", "/v1/mate/runtime/diagnostics", ""},
		// 错误面：不存在 / 方法不存在 / 非法请求体
		{"GET", "/v1/mate/sessions/nope", ""},
		{"GET", "/v1/mate/providers/nope", ""},
		{"DELETE", "/v1/mate/sessions/nope", ""},
		{"POST", "/v1/mate/sessions", "{not json"},
		{"POST", "/v1/mate/sessions/nope/prompt", "{}"},
		{"PUT", "/v1/mate/settings/workspace", "{not json"},
	}
}

// TestControlPlaneContractBaseline 逐条比对控制面契约基线。
func TestControlPlaneContractBaseline(t *testing.T) {
	a, _ := newIsolationRig(t)
	eng := a.Engine()
	host := a.srv.Addr()

	got := make([]contractEntry, 0, len(contractCases()))
	for _, c := range contractCases() {
		req := httptest.NewRequest(c.Method, c.Path, strings.NewReader(c.Body))
		req.Host = host
		if c.Body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		eng.ServeHTTP(w, req)
		got = append(got, normalizeContractEntry(t, c, w))
	}

	golden := filepath.Join("testdata", "control_plane_contract.json")
	if *updateContractBaseline {
		raw, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatalf("序列化基线: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatalf("建 testdata 目录: %v", err)
		}
		if err := os.WriteFile(golden, append(raw, '\n'), 0o644); err != nil {
			t.Fatalf("写基线: %v", err)
		}
		return
	}

	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("读基线 %s 失败（用 -update 生成一次）：%v", golden, err)
	}
	var want []contractEntry
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("解析基线: %v", err)
	}
	if len(want) != len(got) {
		t.Fatalf("基线条数不符：baseline=%d 实际=%d（采样面变了？）", len(want), len(got))
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.Method != g.Method || w.Path != g.Path {
			t.Fatalf("基线顺序错位：want %s %s，got %s %s", w.Method, w.Path, g.Method, g.Path)
		}
		if w.Status != g.Status {
			t.Errorf("%s %s：HTTP 状态 %d → %d（契约变更）", g.Method, g.Path, w.Status, g.Status)
		}
		if w.Code != g.Code {
			t.Errorf("%s %s：业务码 %d → %d（契约变更）", g.Method, g.Path, w.Code, g.Code)
		}
		if w.Msg != g.Msg {
			t.Errorf("%s %s：msg %q → %q（契约变更）", g.Method, g.Path, w.Msg, g.Msg)
		}
		if strings.Join(w.Envelope, ",") != strings.Join(g.Envelope, ",") {
			t.Errorf("%s %s：信封键 %v → %v（契约变更）", g.Method, g.Path, w.Envelope, g.Envelope)
		}
		if w.DataKind != g.DataKind {
			t.Errorf("%s %s：data 形状 %q → %q（契约变更）", g.Method, g.Path, w.DataKind, g.DataKind)
		}
		if strings.Join(w.DataKeys, ",") != strings.Join(g.DataKeys, ",") {
			t.Errorf("%s %s：data 键 %v → %v（契约变更）", g.Method, g.Path, w.DataKeys, g.DataKeys)
		}
		switch {
		case w.DataLen == nil && g.DataLen != nil:
			t.Errorf("%s %s：data 长度基线为 null，实际 %d", g.Method, g.Path, *g.DataLen)
		case w.DataLen != nil && g.DataLen == nil:
			t.Errorf("%s %s：data 长度基线为 %d，实际无长度", g.Method, g.Path, *w.DataLen)
		case w.DataLen != nil && g.DataLen != nil && *w.DataLen != *g.DataLen:
			t.Errorf("%s %s：data 长度 %d → %d", g.Method, g.Path, *w.DataLen, *g.DataLen)
		}
	}
}

// normalizeContractEntry 把一次响应归一化成可比对的结构。
func normalizeContractEntry(t *testing.T, c contractCase, w *httptest.ResponseRecorder) contractEntry {
	t.Helper()
	e := contractEntry{Method: c.Method, Path: c.Path, Status: w.Code}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s %s：响应不是 JSON 对象：%v（body=%.200s）", c.Method, c.Path, err, w.Body.String())
	}
	for k := range body {
		e.Envelope = append(e.Envelope, k)
	}
	sort.Strings(e.Envelope)
	if raw, ok := body["code"]; ok {
		_ = json.Unmarshal(raw, &e.Code)
	}
	if raw, ok := body["msg"]; ok {
		_ = json.Unmarshal(raw, &e.Msg)
	}
	raw, ok := body["data"]
	if !ok {
		e.DataKind = "absent"
		return e
	}
	switch string(raw) {
	case "null":
		e.DataKind = "null"
		return e
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		e.DataKind = "array"
		n := len(arr)
		e.DataLen = &n
		return e
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		e.DataKind = "object"
		for k := range obj {
			e.DataKeys = append(e.DataKeys, k)
		}
		sort.Strings(e.DataKeys)
		return e
	}
	e.DataKind = "scalar"
	return e
}
