package app

import (
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// ——— P0：控制面行为基线（v3.1 §6.1） ———
//
// 口径：排除时间戳、随机 ID、临时路径；只冻结**契约面**——HTTP 状态、业务码、`msg`
// 的稳定取值、响应信封的键集合、`data` 的形状与键集合、集合长度。这些是前端与脚本真正
// 依赖的东西；`requestID` 每次不同，只断言"键在/不在"。
//
// 为什么要有它：P1 起装配根与控制器构造被整体重排，`internal/adapters/inbound/http/router` 的 85 个测试覆盖的是
// 局部行为，不是逐字契约；没有这份基线，"装配重组没改业务"只能靠人读 diff。
//
// 更新方式：`go test ./internal/bootstrap/app -run TestControlPlaneContractBaseline -update`
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

// contractCases 是基线采样面：每条已注册的 REST 路由至少一条（`TestContractBaselineCoversAllRoutes` 双向盯住）。
//
// 采样原则：
//   - 路径参数一律用 `nope`，让路由命中、业务层返回"不存在"——这样响应形状只取决于信封与错误分支，
//     不依赖任何真实数据，天然稳定；
//   - 只读路由直接取真实形状（memory 模式下多为空集合），`data_keys` 与长度都被冻住；
//   - 写路由两件套：`{not json` 走绑定错误、`{}` 走校验错误，都不产生副作用；
//   - 会摸文件系统/弹窗的路由给一个**必然不存在**的路径，把结果压成确定的错误分支。
func contractCases() []contractCase {
	return []contractCase{
		{"GET", "/v1/mate/health", ""},
		{"GET", "/v1/mate/meta", ""},

		// ——— automations ———
		{"GET", "/v1/mate/automations", ""},
		{"POST", "/v1/mate/automations", "{}"},
		{"GET", "/v1/mate/automations/nope", ""},
		{"PUT", "/v1/mate/automations/nope", "{}"},
		{"DELETE", "/v1/mate/automations/nope", ""},
		{"POST", "/v1/mate/automations/nope/run", "{}"},
		{"GET", "/v1/mate/automations/nope/runs", ""},
		{"POST", "/v1/mate/automations/preview", "{}"},

		// ——— capability / command-policy ———
		{"GET", "/v1/mate/capabilities", ""},
		{"GET", "/v1/mate/command-policy", ""},
		{"PUT", "/v1/mate/command-policy", "{}"},

		// ——— library ———
		{"GET", "/v1/mate/library", ""},

		// ——— mcp ———
		{"GET", "/v1/mate/mcps", ""},
		{"POST", "/v1/mate/mcps", "{}"},
		{"PUT", "/v1/mate/mcps/nope", "{}"},
		{"DELETE", "/v1/mate/mcps/nope", ""},
		{"POST", "/v1/mate/mcps/nope/probe", ""},
		{"GET", "/v1/mate/mcps/nope/prompts", ""},
		{"POST", "/v1/mate/mcps/nope/prompts/p/get", "{}"},
		{"GET", "/v1/mate/mcps/nope/resources", ""},
		{"POST", "/v1/mate/mcps/nope/resources/read", "{}"},
		{"POST", "/v1/mate/mcps/nope/tools/t/call", "{}"},

		// ——— models ———
		{"GET", "/v1/mate/models", ""},
		{"POST", "/v1/mate/models", "{}"},
		{"PATCH", "/v1/mate/models/nope", "{}"},
		{"DELETE", "/v1/mate/models/nope", ""},
		{"GET", "/v1/mate/models/catalog", ""},
		{"POST", "/v1/mate/models/sync", "{}"},

		// ——— providers ———
		{"GET", "/v1/mate/providers", ""},
		{"POST", "/v1/mate/providers", "{}"},
		{"PATCH", "/v1/mate/providers/nope", "{}"},
		{"DELETE", "/v1/mate/providers/nope", ""},
		{"POST", "/v1/mate/providers/nope/probe", ""},

		// ——— runtime / search / tools / usage ———
		{"GET", "/v1/mate/runtime/diagnostics", ""},
		{"GET", "/v1/mate/search/messages?q=x", ""},
		{"GET", "/v1/mate/tools", ""},
		{"GET", "/v1/mate/usage/summary", ""},

		// ——— sessions ———
		{"GET", "/v1/mate/sessions", ""},
		{"POST", "/v1/mate/sessions", "{not json"},
		{"GET", "/v1/mate/sessions/nope", ""},
		{"PATCH", "/v1/mate/sessions/nope", "{}"},
		{"GET", "/v1/mate/__no_such_route__", ""},
		{"GET", "/v1/mate/sessions/search", ""},
		{"POST", "/v1/mate/sessions/export", "{}"},
		{"GET", "/v1/mate/sessions/nope/history", ""},
		{"GET", "/v1/mate/sessions/nope/runs", ""},
		{"POST", "/v1/mate/sessions/nope/prompt", "{}"},
		{"POST", "/v1/mate/sessions/nope/cancel", ""},
		{"POST", "/v1/mate/sessions/nope/elicitation", "{}"},
		{"GET", "/v1/mate/sessions/nope/elicitations", ""},
		{"GET", "/v1/mate/sessions/nope/inputs", ""},
		{"DELETE", "/v1/mate/sessions/nope/inputs/i1", ""},
		{"POST", "/v1/mate/sessions/nope/inputs/i1/move", "{}"},
		{"POST", "/v1/mate/sessions/nope/inputs/i1/pop", "{}"},
		{"POST", "/v1/mate/sessions/nope/inputs/i1/send-now", "{}"},
		{"POST", "/v1/mate/sessions/nope/permission", "{}"},
		{"GET", "/v1/mate/sessions/nope/permissions", ""},
		{"GET", "/v1/mate/sessions/nope/permissions/always", ""},
		{"POST", "/v1/mate/sessions/nope/permissions/always/revoke", "{}"},
		{"GET", "/v1/mate/sessions/nope/permissions/memories", ""},
		{"POST", "/v1/mate/sessions/nope/permissions/memories/revoke", "{}"},

		// ——— settings ———
		{"GET", "/v1/mate/settings/workspace", ""},
		{"PUT", "/v1/mate/settings/workspace", "{not json"},

		// ——— skills ———
		{"GET", "/v1/mate/skills", ""},
		{"POST", "/v1/mate/skills", "{}"},
		{"PUT", "/v1/mate/skills/nope", "{}"},
		{"PATCH", "/v1/mate/skills/nope", "{}"},
		{"DELETE", "/v1/mate/skills/nope", ""},
		{"GET", "/v1/mate/skills/nope/materialization", ""},
		{"POST", "/v1/mate/skills/import", "{}"},

		// ——— workspaces ———
		//
		// `browse` / `files` 会真的读目录：给一个必然不存在的路径，把结果压成确定的错误分支，
		// 否则 `data_len` 会随临时目录内容漂移。
		{"GET", "/v1/mate/workspaces", ""},
		{"POST", "/v1/mate/workspaces", "{}"},
		{"PATCH", "/v1/mate/workspaces/nope", "{}"},
		{"DELETE", "/v1/mate/workspaces/nope", ""},
		{"POST", "/v1/mate/workspaces/nope/move", "{}"},
		{"GET", "/v1/mate/workspaces/browse?path=/nonexistent-ark-mate-probe", ""},
		{"GET", "/v1/mate/workspaces/recent", ""},
		{"GET", "/v1/mate/workspaces/files?path=/nonexistent-ark-mate-probe", ""},
		{"POST", "/v1/mate/workspaces/dirs", "{}"},
	}
}

// contractExcluded 已注册但不是 JSON 信封的 REST 路由：落在采样面之外，必须写明理由。
//
// 双向判据：路由新增而未加入 `contractCases`、或本表条目对应的路由已消失，`TestContractBaselineCoversAllRoutes` 都判红。
var contractExcluded = map[string]string{
	"GET /v1/mate/ws/events": "websocket 升级，响应不是 JSON 信封",
	"GET /":                  "SPA 静态入口（index.html），不是 JSON 信封",
	// 这条不看请求体，直接抬宿主原生目录选择器（core 进程 spawn），实测阻塞约 29s，
	// 且结果（选中/取消/无宿主）由宿主的桌面环境决定——它是宿主交互能力，不是数据契约。
	"POST /v1/mate/workspaces/pick-directory": "抬宿主原生目录选择器，结果依赖宿主 UI",
}

// contractNoRouteCases 刻意不命中任何注册路由的采样：它们测的是 NoRoute 兜底的信封形状。
// 键是 `方法 路径`，值写明它测什么。
var contractNoRouteCases = map[string]string{
	"GET /v1/mate/__no_such_route__": "NoRoute 兜底：未知路径必须回 JSON 信封而不是空响应",
}

// TestControlPlaneContractBaseline 逐条比对控制面契约基线。
func TestControlPlaneContractBaseline(t *testing.T) {
	a, _ := newIsolationRig(t)
	assertContractBaseline(t, a.Handler(), a.srv.Addr())
}

// assertContractBaseline 对**任意一份装配产物**逐条比对控制面契约基线。
//
// 抽成函数是为了让 C2 §3.4 的桩验收复用同一份判据（`stub_execution_test.go`：底座换成
// 不认识协议的纯内存实现，这份基线必须逐字通过）。两处各抄一份采样表，迟早在一边悄悄漂移。
func assertContractBaseline(t *testing.T, eng http.Handler, host string) {
	t.Helper()
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

// contractVolatileDataKeys 采样里**不冻结**的 data 键：它们出现与否取决于宿主事实
// （这台机器装没装底座、此刻有没有活的底座进程），不是控制面契约。
//
// 为什么必须显式列出来（不是"看着不稳就不比"）：
//   - `/runtime/diagnostics` 的 `pid` 只在**有活连接**时出现，而活连接是真实拨号的副产物
//     ——`GET /models` 会取样模型目录，取样要一条连接，于是基线跑一遍就真的起了一个底座进程。
//     把 `pid` 冻进基线，等于把"跑基线那台机器装没装 dsh"写进契约：没装的机器上基线红，
//     而契约一个字没变。
//   - `remediation` 同理：未连接/有故障时才出现（底座没装 → 安装说明）。
//
// 这两个键的**内容**另有判据（`TestRuntimeDiagnosticsRemediation` 逐条断言"怎么修"），
// 所以这里不冻结键存在性，不丢覆盖。
var contractVolatileDataKeys = map[string]string{
	"pid":         "只在有活连接时出现：取决于宿主是否装了底座、此刻有没有进程",
	"remediation": "只在未连接或有故障时出现：同样取决于宿主事实（装没装底座）",
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
			if _, volatile := contractVolatileDataKeys[k]; volatile {
				continue
			}
			e.DataKeys = append(e.DataKeys, k)
		}
		sort.Strings(e.DataKeys)
		return e
	}
	e.DataKind = "scalar"
	return e
}

// routePatternMatches 判断一条已注册路由模式是否被某条具体路径命中（`:id` 视为通配）。
func routePatternMatches(pattern, path string) bool {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	pp := strings.Split(strings.Trim(pattern, "/"), "/")
	cp := strings.Split(strings.Trim(path, "/"), "/")
	if len(pp) != len(cp) {
		return false
	}
	for i := range pp {
		if strings.HasPrefix(pp[i], ":") || strings.HasPrefix(pp[i], "*") {
			continue
		}
		if pp[i] != cp[i] {
			return false
		}
	}
	return true
}

// TestContractBaselineCoversAllRoutes 双向盯住采样面。
//
// `TestControlPlaneContractBaseline` 只比对"采样到的那些"：新增或改名路由时它照样绿，
// 采样面会**静默缩小**。这条守卫让"每条注册路由都有基线"成为可验证的事实：
//   - 每条已注册路由，要么被 `contractCases` 命中，要么在 `contractExcluded` 里写明理由；
//   - 每条采样必须命中一条真实路由（抓路由改名/删除后残留的采样）；
//   - 排除表条目对应的路由必须仍然存在（双向判据）。
func TestContractBaselineCoversAllRoutes(t *testing.T) {
	a, _ := newIsolationRig(t)
	routes := a.Handler().(*gin.Engine).Routes()
	if len(routes) == 0 {
		t.Fatalf("路由表为空：无法校验契约采样面")
	}
	cases := contractCases()

	for _, c := range cases {
		if _, ok := contractNoRouteCases[c.Method+" "+strings.SplitN(c.Path, "?", 2)[0]]; ok {
			continue // 显式 NoRoute 探针，见 contractNoRouteCases
		}
		hit := false
		for _, r := range routes {
			if r.Method == c.Method && routePatternMatches(r.Path, c.Path) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("采样 %s %s 没有命中任何已注册路由（路由改名或删除后残留？）", c.Method, c.Path)
		}
	}

	seen := make(map[string]bool, len(contractExcluded))
	for k := range contractExcluded {
		seen[k] = false
	}
	for _, r := range routes {
		key := r.Method + " " + r.Path
		if _, ok := contractExcluded[key]; ok {
			seen[key] = true
			continue
		}
		covered := false
		for _, c := range cases {
			if c.Method == r.Method && routePatternMatches(r.Path, c.Path) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("路由 %s 既不在 contractCases 也不在 contractExcluded（采样面缩小了）", key)
		}
	}
	for k, hit := range seen {
		if !hit {
			t.Errorf("排除表条目 %s 对应的路由已不存在，请删除该条", k)
		}
	}
}
