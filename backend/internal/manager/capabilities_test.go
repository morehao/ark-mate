package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/mcp"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/gerror"
)

func writeCapFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 本文件里的装载用例走 **memory 载体的目录装载**（`LoadCapabilityDefinitions`）；sqlite 载体的
// 整体替换式装载（含"库里删掉的行不会以幽灵定义留在内存里"）在 `capability_store_test.go`。
//
// TestLoadCapabilityDefinitionsAndList Service 集成：目录装载 + REST 三表列表（排序、headers 不读回）。
func TestLoadCapabilityDefinitionsAndList(t *testing.T) {
	dir := t.TempDir()
	writeCapFile(t, filepath.Join(dir, "skills", "plan.json"), `{"id":"plan","name":"规划","description":"先规划"}`)
	writeCapFile(t, filepath.Join(dir, "mcp", "fs.json"),
		`{"id":"fs","name":"Files","transport":"stdio","command":"/usr/local/bin/npx","headers":{"Authorization":"Bearer x"}}`)
	writeCapFile(t, filepath.Join(dir, "tools", "web.json"), `{"name":"web_search","source":"builtin"}`)

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	if n := svc.LoadCapabilityDefinitions(dir); n != 3 {
		t.Fatalf("期望加载 3 条，got %d", n)
	}

	skills, _ := svc.ListSkills()
	if len(skills) != 1 || skills[0].ID != "plan" || skills[0].Name != "规划" {
		t.Fatalf("skills 不符: %+v", skills)
	}

	mcps, _ := svc.ListMcpServers()
	if len(mcps) != 1 || mcps[0].ID != "fs" || mcps[0].Transport != "stdio" {
		t.Fatalf("mcps 不符: %+v", mcps)
	}

	tools, _ := svc.ListTools()
	if len(tools) != 1 || tools[0].Name != "web_search" || tools[0].Source != capability.ToolSourceBuiltin {
		t.Fatalf("tools 不符: %+v", tools)
	}
}

// TestRegisterToolAndSort 代码注入工具 + 列表排序（工具表按 name）。
func TestRegisterToolAndSort(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	if err := svc.RegisterTool(capability.Tool{Name: "z_tool", Source: capability.ToolSourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RegisterTool(capability.Tool{Name: "a_tool", Source: capability.ToolSourceObserved, Description: "obs"}); err != nil {
		t.Fatal(err)
	}
	tools, _ := svc.ListTools()
	if len(tools) != 2 || tools[0].Name != "a_tool" || tools[1].Name != "z_tool" {
		t.Fatalf("tools 排序不符: %+v", tools)
	}
}

// TestMcpServersFor P2 MCP 注入选择（§5.2/§5.3）+ M7-3 的传输分派：stdlib 直连、远程折叠成网关条目。
//
// 语义（M7-3 之后）：
//   - **网关开（默认）**：stdio server 原样直连；远程 server 不再逐条直连，而是合成**一条**
//     `forge-gateway` stdio 条目 —— 于是"该 runtime 声明的传输"不再决定远程 server 的去留
//     （那正是网关的收益），仅决定 stdio 那一支。
//   - **网关关（FORGE_MCP_GATEWAY=off）**：回到老路 —— 按该 runtime 的 McpTransports 过滤后逐条直连。
//   - 白名单优先级两态都生效：nil = 全量；非 nil = 命中制；空 = 本 agent 禁用注入。未知 runtime → 不下发。
func TestMcpServersFor(t *testing.T) {
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "stub", McpTransports: []string{"stdio"}},
		runtime.Spec{ID: "opencode", McpTransports: []string{"stdio", "sse", "http"}},
		runtime.Spec{ID: "plain"}, // 未声明 → 保守仅 stdio
	)
	svc := NewService(reg, eventbus.New())
	must := func(id, transport string) {
		t.Helper()
		// stdio server 须绝对 command（ACP 校验；注册表已拦相对路径）。
		if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: id, Name: id, Transport: transport, Command: "/usr/local/bin/npx"}); err != nil {
			t.Fatal(err)
		}
	}
	must("fs_stdio", capability.TransportStdio)
	must("evt_sse", capability.TransportSSE)
	must("web_http", capability.TransportHTTP)

	noBind := &AgentRec{ID: "a0"} // Mcps nil = 未声明 → 全量
	onlyFS := &AgentRec{ID: "a1", Mcps: ptrOf([]string{"fs_stdio"})}
	denyAll := &AgentRec{ID: "a2", Mcps: ptrOf([]string{})}

	// 网关条目断言：一条 stdio 条目、命令是平台自身、参数是 mcp bridge + 稳定 scope。
	assertGatewayEntry := func(entries []acl.McpServer) {
		t.Helper()
		last := entries[len(entries)-1]
		if last.Name != mcp.GatewayServerName || last.Transport != capability.TransportStdio {
			t.Fatalf("末条应是网关条目（stdio/forge-gateway），got %+v", last)
		}
		if last.Command == "" || !strings.HasSuffix(last.Command, "manager.test") {
			t.Fatalf("网关条目命令应为平台自身可执行文件，got %q", last.Command)
		}
		args := strings.Join(last.Args, " ")
		if !strings.HasPrefix(args, "mcp bridge --agent a0 --runtime stub") {
			t.Fatalf("网关条目参数应是 mcp bridge + 稳定 scope，got %v", last.Args)
		}
	}

	// 网关开（默认）：stdio 直连 1 条 + 网关 1 条。注意 stub **只声明 stdio**，而远程 server
	// 照样经网关可达——这正是 §10.6-① 的收益。
	got := svc.McpServersFor(noBind, "stub", nil)
	if len(got) != 2 || got[0].Name != "fs_stdio" || got[0].Transport != capability.TransportStdio {
		t.Fatalf("仅 stdio 的 runtime 应取「stdio 直连 + 网关」2 条，got %+v", got)
	}
	assertGatewayEntry(got)
	if got := svc.McpServersFor(noBind, "opencode", nil); len(got) != 2 {
		t.Fatalf("opencode 未声明白名单应取「stdio 直连 + 网关」2 条（不再逐条直连远程），got %+v", got)
	}
	// 白名单只钉 stdio server：直连 1 条 + **网关条目照样在**（条目恒定，不跟定义集走）；
	// 它的远程面会是空的（网关按同一份白名单取数），所以"钉了 stdio 就没网关条目"不是判据。
	if got := svc.McpServersFor(onlyFS, "opencode", nil); len(got) != 2 || got[0].Name != "fs_stdio" || got[1].Name != mcp.GatewayServerName {
		t.Fatalf("白名单 fs_stdio 应取「直连 1 条 + 恒定网关条目」，got %+v", got)
	}
	if got := svc.McpServersFor(denyAll, "opencode", nil); len(got) != 0 {
		t.Fatalf("空白名单应禁用注入，got %+v", got)
	}
	if got := svc.McpServersFor(noBind, "ghost", nil); len(got) != 0 {
		t.Fatalf("未知 runtime 应不下发，got %+v", got)
	}
	// Spec.McpTransports 为空 → 该 runtime 的直连只承载 stdio（ACP 全 agent 必须支持）。
	if got := svc.McpServersFor(noBind, "plain", nil); len(got) != 2 || got[0].Name != "fs_stdio" {
		t.Fatalf("未声明传输的 runtime 应取「stdio 直连 + 网关」，got %+v", got)
	}

	// 回退开关：off → 老口径逐条直连（远程按该 runtime 的声明过滤）。
	svc.SetMcpGatewayEnabled(false)
	if got := svc.McpServersFor(noBind, "stub", nil); len(got) != 1 || got[0].Name != "fs_stdio" {
		t.Fatalf("网关关时应回到「仅承载得起的直连」，got %+v", got)
	}
	if got := svc.McpServersFor(noBind, "opencode", nil); len(got) != 3 {
		t.Fatalf("网关关时 opencode 应逐条直连 3 台，got %+v", got)
	}
	if got := svc.McpServersFor(noBind, "plain", nil); len(got) != 1 {
		t.Fatalf("网关关时未声明传输的 runtime 应保守仅 stdio，got %+v", got)
	}
}

// TestAgentCapabilities 统一能力目录（§8.1）可见性规则：skill 按候选 runtime 裁剪、
// MCP 全量 + enabled/runtimes 标注、tool 按归属 server 的启用×可承载过滤、未知 runtime 降级记 notes。
func TestAgentCapabilities(t *testing.T) {
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "opencode", McpTransports: []string{"stdio", "sse", "http"}},
	)
	svc := NewService(reg, eventbus.New())
	// 本进程解析出 opencode 的 skill 投影（ghost 未注册 → 不可能有投影）。
	svc.SetSkillProjections([]SkillProjection{skillProj(t.TempDir(), "opencode")})
	svc.agents["a1"] = &AgentRec{
		ID: "a1", Name: "A1", Runtimes: []string{"opencode", "ghost"},
		Mcps: ptrOf([]string{"fs"}), State: AgentStateStopped,
	}
	mustSkill := func(s capability.Skill) {
		t.Helper()
		if err := svc.caps.RegisterSkill(&s); err != nil {
			t.Fatal(err)
		}
	}
	mustSkill(capability.Skill{ID: "plan", Name: "plan"}) // 未声明 runtimes = 全部
	// 声明只给 ghost 用：ghost 是候选但没登记投影 → 不适用（但**必须列出**，不能静默消失）。
	mustSkill(capability.Skill{ID: "no-src", Name: "no-src", Runtimes: []string{"ghost"}})
	mustMcp := func(id, transport string) {
		t.Helper()
		if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: id, Name: id, Transport: transport, Command: "/usr/local/bin/npx"}); err != nil {
			t.Fatal(err)
		}
	}
	mustMcp("fs", capability.TransportStdio)
	mustMcp("sse_only", capability.TransportSSE)
	mustMcp("http_ok", capability.TransportHTTP)
	mustTool := func(tl capability.Tool) {
		t.Helper()
		if err := svc.caps.RegisterTool(&tl); err != nil {
			t.Fatal(err)
		}
	}
	mustTool(capability.Tool{Name: "web_search", Source: capability.ToolSourceBuiltin})
	mustTool(capability.Tool{Name: "obs_tool", Source: capability.ToolSourceObserved})
	mustTool(capability.Tool{Name: "fs_read", Source: capability.ToolSourceMCP, McpServer: "fs"})
	mustTool(capability.Tool{Name: "sse_tool", Source: capability.ToolSourceMCP, McpServer: "sse_only"})
	mustTool(capability.Tool{Name: "orphan_tool", Source: capability.ToolSourceMCP, McpServer: "not_registered"})

	got, err := svc.AgentCapabilities("a1")
	if err != nil {
		t.Fatalf("AgentCapabilities: %v", err)
	}
	if got.AgentID != "a1" || len(got.Runtimes) != 2 || got.Runtimes[0] != "opencode" {
		t.Fatalf("agent/runtimes 不符: %+v", got)
	}
	// skill：§13.4 S5 起**不再按候选 runtime 静默过滤**——两条都列出；适用性取决于
	// 「候选 runtime ∩ 技能声明的 runtimes ∩ 已解析投影」，不适用时带人话原因。
	if len(got.Skills) != 2 {
		t.Fatalf("skills 应全量列出（含不适用）: %+v", got.Skills)
	}
	skByID := map[string]dtocapability.AgentSkillResp{}
	for _, sk := range got.Skills {
		skByID[sk.ID] = sk
	}
	if plan := skByID["plan"]; !plan.Applicable || !plan.Enabled ||
		len(plan.Runtimes) != 1 || plan.Runtimes[0] != "opencode" || len(plan.Reasons) != 0 {
		t.Fatalf("plan 应启用且适用、runtimes 裁到候选∩投影: %+v", plan)
	}
	if noSrc := skByID["no-src"]; noSrc.Applicable || len(noSrc.Runtimes) != 0 || len(noSrc.Reasons) == 0 {
		t.Fatalf("no-src 应不适用（runtimes 空）+ 有原因: %+v", noSrc)
	}
	// MCP：全量 3 条；白名单只启用 fs；carriers 只含能承载该传输的候选（ghost 未注册不计）。
	if len(got.McpServers) != 3 {
		t.Fatalf("mcp_servers 应全量 3 条，got %+v", got.McpServers)
	}
	byID := map[string]dtocapability.AgentMcpServerResp{}
	for _, m := range got.McpServers {
		byID[m.ID] = m
	}
	if m := byID["fs"]; !m.Enabled || len(m.Runtimes) != 1 || m.Runtimes[0] != "opencode" {
		t.Fatalf("fs 应 enabled+opencode 承载，got %+v", m)
	}
	if m := byID["sse_only"]; m.Enabled || len(m.Runtimes) != 1 {
		t.Fatalf("sse_only 应未启用但 opencode 可承载，got %+v", m)
	}
	if m := byID["http_ok"]; m.Enabled {
		t.Fatalf("http_ok 应未启用（白名单只有 fs），got %+v", m)
	}
	// tool：builtin/observed 恒可见；fs_read 可见（fs 启用且可承载）；
	// sse_tool 被白名单挡；orphan_tool 归属未注册 server → 不可见。
	names := make([]string, 0, len(got.Tools))
	for _, tl := range got.Tools {
		names = append(names, tl.Name)
	}
	want := map[string]bool{"web_search": true, "obs_tool": true, "fs_read": true}
	if len(got.Tools) != len(want) {
		t.Fatalf("tools 应 %d 条，got %v", len(want), names)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("tools 含不可见条目 %q（全量 %v）", n, names)
		}
	}
	// notes：ghost 未注册降级说明。
	if len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "ghost") {
		t.Fatalf("notes 应记录未注册 runtime，got %+v", got.Notes)
	}

	// mcps: [] → 禁用注入：fs 未启用、fs_read 不可见、notes 记明。
	svc.agents["a2"] = &AgentRec{ID: "a2", Runtimes: []string{"opencode"}, Mcps: ptrOf([]string{}), State: AgentStateStopped}
	got2, err := svc.AgentCapabilities("a2")
	if err != nil {
		t.Fatalf("AgentCapabilities(a2): %v", err)
	}
	for _, m := range got2.McpServers {
		if m.Enabled {
			t.Fatalf("a2 声明 mcps:[] 应全部未启用，got %+v", m)
		}
	}
	for _, tl := range got2.Tools {
		if tl.Source == capability.ToolSourceMCP {
			t.Fatalf("a2 禁用 MCP 时不应出现 mcp 工具: %+v", tl)
		}
	}
	found := false
	for _, n := range got2.Notes {
		if strings.Contains(n, "mcps: []") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a2 notes 应记禁用说明，got %+v", got2.Notes)
	}

	// 未知 agent → 40401。
	if _, err := svc.AgentCapabilities("nope"); err == nil {
		t.Fatal("未知 agent 应报错")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != 40401 {
			t.Fatalf("未知 agent 应 40401，got %v", err)
		}
	}
}

func ptrOf[T any](v T) *T { return &v }

// TestProbeMcpServer MCP live 探测（P2 §5.2）：http 探测成功 → Tool 目录整体替换 + 归属
// 标注 + 状态记录；重复探测收敛（旧条目删除）；失败记录 last_probe_error；未知 id → 40401；
// sse 传输明确降级 → 40501（ClassUnsupported）。
func TestProbeMcpServer(t *testing.T) {
	// 可切换返回的 tools/list 结果（验证「整体替换」语义）。
	tools := []map[string]any{{"name": "read_file", "description": "读文件"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result map[string]any
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "mgr-helper", "version": "3.1"}}
		case "tools/list":
			result = map[string]any{"tools": tools}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}))
	defer srv.Close()

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: "h", Name: "H", Transport: capability.TransportHTTP, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	// 预置一条同名旧条目 + 一条 builtin（替换不得误伤）。
	if err := svc.caps.RegisterTool(&capability.Tool{Name: "read_file", Source: capability.ToolSourceMCP, McpServer: "h", Description: "旧描述"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.caps.RegisterTool(&capability.Tool{Name: "web_search", Source: capability.ToolSourceBuiltin}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ProbeMcpServer(context.Background(), "h")
	if err != nil {
		t.Fatalf("ProbeMcpServer: %v", err)
	}
	if got.ServerName != "mgr-helper" || got.ServerVersion != "3.1" || got.Added != 1 || got.Removed != 1 || got.ToolCount != 1 {
		t.Fatalf("探测结果不符: %+v", got)
	}
	if got.ProbedAt == "" {
		t.Fatal("ProbedAt 不应为空")
	}
	if len(got.Tools) != 1 || got.Tools[0].McpServer != "h" || got.Tools[0].Description != "读文件" {
		t.Fatalf("tools 归属/内容不符: %+v", got.Tools)
	}
	// 旧条目被替换（描述更新），builtin 保留。
	all, _ := svc.ListTools()
	if len(all) != 2 {
		t.Fatalf("tools 应 2 条（mcp+builtin），got %+v", all)
	}
	for _, tl := range all {
		// M7-4 起，**远程**（http）server 的工具在目录里按网关命名策略落前缀（`<serverID>__<tool>`），
		// 与模型真实能调的名字逐字一致；builtin 不受影响（`web_search` 仍是原名）。
		if tl.Name == "h__read_file" && tl.Description != "读文件" {
			t.Fatalf("旧条目未被替换: %+v", tl)
		}
	}
	// 状态写回 GET /mcps。
	mcps, _ := svc.ListMcpServers()
	if len(mcps) != 1 || mcps[0].ToolCount != 1 || mcps[0].LastProbeAt == "" || mcps[0].LastProbeErr != "" {
		t.Fatalf("mcps 探测状态不符: %+v", mcps)
	}

	// 第二轮：server 只剩 write_file → read_file 删除、write_file 写入（整体替换）。
	tools = []map[string]any{{"name": "write_file"}}
	got2, err := svc.ProbeMcpServer(context.Background(), "h")
	if err != nil {
		t.Fatalf("二次探测: %v", err)
	}
	if got2.Added != 1 || got2.Removed != 1 || got2.ToolCount != 1 {
		t.Fatalf("二次探测增删不符: %+v", got2)
	}
	names := map[string]bool{}
	for _, tl := range mustListTools(t, svc) {
		names[tl.Name] = true
	}
	if names["h__read_file"] || !names["h__write_file"] || !names["web_search"] {
		t.Fatalf("二次探测后目录不符（远程工具应带 serverID 前缀）: %+v", names)
	}

	// 未知 id → 40401；sse 传输 → 40501（明确降级，不臆造）。
	if _, err := svc.ProbeMcpServer(context.Background(), "nope"); err == nil {
		t.Fatal("未知 server 应报错")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != 40401 {
			t.Fatalf("未知 server 应 40401，got %v", err)
		}
	}
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: "sse1", Transport: capability.TransportSSE, URL: "http://127.0.0.1:1/sse"}); err != nil {
		t.Fatal(err)
	}
	// sse（B3 起已支持）指向不可达端点 → 50201 连接失败（不再是不支持 40501）。
	if _, err := svc.ProbeMcpServer(context.Background(), "sse1"); err == nil {
		t.Fatal("sse 不可达应报错")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != 50201 {
			t.Fatalf("sse 不可达应 50201，got %v", err)
		}
	}
	// 失败状态落盘可读回。
	mcps, _ = svc.ListMcpServers()
	for _, m := range mcps {
		if m.ID == "sse1" && !strings.Contains(m.LastProbeErr, "sse") {
			t.Fatalf("sse1 应记录 last_probe_error，got %+v", m)
		}
	}
}

func mustListTools(t *testing.T, svc *Service) []dtocapability.ToolResp {
	t.Helper()
	out, err := svc.ListTools()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestAgentMcpAllowlistFromDefinitions agent 定义文件 `mcps` 三态：缺省 → nil（全启用）；
// 声明白名单 → 指针携带；`[]` → 非 nil 空指针（禁用注入）。经 LoadAgentDefinitions 装载。
func TestAgentMcpAllowlistFromDefinitions(t *testing.T) {
	dir := t.TempDir()
	writeCapFile(t, filepath.Join(dir, "a.json"), `{"id":"a","runtimes":["stub"],"mcps":["fs_stdio"]}`)
	writeCapFile(t, filepath.Join(dir, "b.json"), `{"id":"b","runtimes":["stub"],"mcps":[]}`)
	writeCapFile(t, filepath.Join(dir, "c.json"), `{"id":"c","runtimes":["stub"]}`)

	svc := NewService(runtime.NewRegistry(runtime.Spec{ID: "stub"}), eventbus.New()) // 定义引用 echo runtime
	if n := svc.LoadAgentDefinitions(dir); n != 3 {
		t.Fatalf("期望装载 3 条，got %d", n)
	}
	a, _ := svc.AgentByID("a")
	if a.Mcps == nil || len(*a.Mcps) != 1 || (*a.Mcps)[0] != "fs_stdio" {
		t.Fatalf("白名单应携带 fs_stdio，got %v", a.Mcps)
	}
	b, _ := svc.AgentByID("b")
	if b.Mcps == nil || len(*b.Mcps) != 0 {
		t.Fatalf("mcps:[] 应得非 nil 空指针，got %v", b.Mcps)
	}
	c, _ := svc.AgentByID("c")
	if c.Mcps != nil {
		t.Fatalf("缺省 mcps 应为 nil（全启用），got %v", c.Mcps)
	}
}

// TestServiceReconcileSkills 启用物化重校：按投影形态写为 <root>/<id>/SKILL.md 并计数；
// runtimes 不含本投影方的 skill 跳过；幂等重跑 0 写。
func TestServiceReconcileSkills(t *testing.T) {
	svc, _, _ := rigWithRuntime(t)
	if err := svc.caps.RegisterSkill(&capability.Skill{
		ID: "forge-demo", Name: "Demo", Body: "# demo body",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.caps.RegisterSkill(&capability.Skill{
		ID: "plain", Name: "P", Body: "# p", Runtimes: []string{"deepseekharness"}, // 不投放给 opencode → 跳过
	}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	w, r, err := svc.ReconcileSkillProjection(skillProj(root, "opencode"))
	if err != nil {
		t.Fatalf("ReconcileSkills: %v", err)
	}
	if w != 1 || r != 0 {
		t.Fatalf("期望 written=1 removed=0，got w=%d r=%d", w, r)
	}
	p := filepath.Join(root, "forge-demo", "SKILL.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "---\nname: \"forge-demo\"\ndescription: \"Demo\"\n---\n# demo body" {
		t.Fatalf("物化内容不符（应带 frontmatter）: %q", b)
	}
	// 幂等重跑：无写无删。
	if w, r, err := svc.ReconcileSkillProjection(skillProj(root, "opencode")); err != nil || w != 0 || r != 0 {
		t.Fatalf("幂等重校应 0/0，got w=%d r=%d err=%v", w, r, err)
	}
}

// restCode 取 REST 信封错误码（非 gerror → -1）。
func restCode(err error) int {
	var ge gerror.Error
	if errors.As(err, &ge) {
		return ge.Code
	}
	return -1
}

// TestCallMcpTool B1：MCP 工具调用（http 传输）——参数透传、工具级失败透传、未知 server 40401、
// sse 40501、超时 40901、非法 arguments 40001。
func TestCallMcpTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result map[string]any
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "mgr-call", "version": "1.0"}}
		case "tools/call":
			switch msg.Params.Name {
			case "boom":
				result = map[string]any{"content": []map[string]any{{"type": "text", "text": "工具内部失败"}}, "isError": true}
			default:
				result = map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "called " + msg.Params.Name},
						{"type": "text", "text": "args " + string(msg.Params.Arguments)},
					},
					"structuredContent": map[string]any{"passthrough": true},
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}))
	defer srv.Close()

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: "h", Name: "H", Transport: capability.TransportHTTP, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.CallMcpTool(context.Background(), "h", "read_file", dtocapability.McpToolCallReq{Arguments: json.RawMessage(`{"path":"/tmp/a"}`)})
	if err != nil {
		t.Fatalf("CallMcpTool: %v", err)
	}
	if got.ServerID != "h" || got.Tool != "read_file" || got.IsError {
		t.Fatalf("结果元数据不符: %+v", got)
	}
	if got.Text != "called read_file\nargs {\"path\":\"/tmp/a\"}" {
		t.Fatalf("文本块拼接不符: %q", got.Text)
	}
	if string(got.StructuredContent) != `{"passthrough":true}` || got.ServerName != "mgr-call" {
		t.Fatalf("structuredContent/serverName 不符: %+v", got)
	}

	// 工具级失败：不是平台错误。
	gotErr, err := svc.CallMcpTool(context.Background(), "h", "boom", dtocapability.McpToolCallReq{})
	if err != nil {
		t.Fatalf("工具级失败不应报错: %v", err)
	}
	if !gotErr.IsError || gotErr.Text != "工具内部失败" {
		t.Fatalf("is_error 透传不符: %+v", gotErr)
	}

	// 未知 server → 40401。
	if _, err := svc.CallMcpTool(context.Background(), "nope", "t", dtocapability.McpToolCallReq{}); restCode(err) != 40401 {
		t.Fatalf("未知 server 应 40401，got %v", err)
	}
	// sse（B3 起已支持）指向不可达端点 → 连接失败 50201（不再是不支持 40501）。
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: "s", Name: "S", Transport: capability.TransportSSE, URL: "http://127.0.0.1:1/sse"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CallMcpTool(context.Background(), "s", "t", dtocapability.McpToolCallReq{}); restCode(err) != 50201 {
		t.Fatalf("sse 不可达应 50201，got %v", err)
	}
	// 非法 arguments → 40001（ClassRequest）。
	if _, err := svc.CallMcpTool(context.Background(), "h", "read_file", dtocapability.McpToolCallReq{Arguments: json.RawMessage(`{oops`)}); restCode(err) != 40001 {
		t.Fatalf("非法 arguments 应 40001，got %v", err)
	}
	// 空工具名 → 40001。
	if _, err := svc.CallMcpTool(context.Background(), "h", "", dtocapability.McpToolCallReq{}); restCode(err) != 40001 {
		t.Fatalf("空工具名应 40001，got %v", err)
	}
}

// TestCallMcpToolTimeout B1：调用预算经 ctx 生效 → 40901（ClassLimit）。
func TestCallMcpToolTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if msg.Method == "tools/call" {
			time.Sleep(400 * time.Millisecond) // 超过下方 timeout_ms
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
			"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "slow"}, "content": []map[string]any{},
		}})
	}))
	defer srv.Close()

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: "slow", Name: "Slow", Transport: capability.TransportHTTP, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.CallMcpTool(context.Background(), "slow", "t", dtocapability.McpToolCallReq{TimeoutMS: 50})
	if restCode(err) != 40901 {
		t.Fatalf("超时应 40901，got %v (code=%d)", err, restCode(err))
	}
}

// TestMcpResourcesPrompts B2：资源/提示词 live 拉取（http 传输）——字段映射、模板降级、
// 空 uri/空名 40001、未知 server 40401、sse 40501、服务端错误 50201。
func TestMcpResourcesPrompts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result map[string]any
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "mgr-b2", "version": "2.0"}}
		case "resources/list":
			result = map[string]any{"resources": []map[string]any{
				{"uri": "file:///tmp/a.txt", "name": "a", "mimeType": "text/plain", "size": 3},
			}}
		case "resources/templates/list":
			result = map[string]any{"resourceTemplates": []map[string]any{{"uriTemplate": "file:///{p}", "name": "f"}}}
		case "resources/read":
			result = map[string]any{"contents": []map[string]any{{"uri": "file:///tmp/a.txt", "mimeType": "text/plain", "text": "内容"}}}
		case "prompts/list":
			result = map[string]any{"prompts": []map[string]any{
				{"name": "greet", "description": "问候", "arguments": []map[string]any{{"name": "who", "required": true}}},
			}}
		case "prompts/get":
			result = map[string]any{"description": "问候模板", "messages": []map[string]any{
				{"role": "user", "content": map[string]any{"type": "text", "text": "你好"}},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}))
	defer srv.Close()

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: "h", Name: "H", Transport: capability.TransportHTTP, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ListMcpResources(context.Background(), "h")
	if err != nil {
		t.Fatalf("ListMcpResources: %v", err)
	}
	if res.ServerID != "h" || res.ServerName != "mgr-b2" || len(res.Resources) != 1 {
		t.Fatalf("资源列表不符: %+v", res)
	}
	if res.Resources[0].URI != "file:///tmp/a.txt" || res.Resources[0].MimeType != "text/plain" ||
		res.Resources[0].Size == nil || *res.Resources[0].Size != 3 {
		t.Fatalf("资源条目不符: %+v", res.Resources[0])
	}
	if len(res.Templates) != 1 || res.Templates[0].URITemplate != "file:///{p}" || res.TemplatesError != "" {
		t.Fatalf("模板不符: %+v", res)
	}

	rd, err := svc.ReadMcpResource(context.Background(), "h", "file:///tmp/a.txt")
	if err != nil {
		t.Fatalf("ReadMcpResource: %v", err)
	}
	if len(rd.Contents) != 1 || rd.Contents[0].Text != "内容" || rd.URI != "file:///tmp/a.txt" {
		t.Fatalf("读取结果不符: %+v", rd)
	}
	if _, err := svc.ReadMcpResource(context.Background(), "h", "  "); restCode(err) != 40001 {
		t.Fatalf("空 uri 应 40001，got %v", err)
	}

	ps, err := svc.ListMcpPrompts(context.Background(), "h")
	if err != nil {
		t.Fatalf("ListMcpPrompts: %v", err)
	}
	if len(ps.Prompts) != 1 || ps.Prompts[0].Name != "greet" || len(ps.Prompts[0].Arguments) != 1 ||
		!ps.Prompts[0].Arguments[0].Required {
		t.Fatalf("提示词列表不符: %+v", ps)
	}

	gp, err := svc.GetMcpPrompt(context.Background(), "h", "greet", dtocapability.McpPromptGetReq{Arguments: map[string]string{"who": "世界"}})
	if err != nil {
		t.Fatalf("GetMcpPrompt: %v", err)
	}
	if gp.Description != "问候模板" || len(gp.Messages) != 1 || gp.Messages[0].Role != "user" {
		t.Fatalf("提示词结果不符: %+v", gp)
	}
	if _, err := svc.GetMcpPrompt(context.Background(), "h", "", dtocapability.McpPromptGetReq{}); restCode(err) != 40001 {
		t.Fatalf("空提示词名应 40001，got %v", err)
	}

	// 未知 server → 40401；sse → 40501。
	if _, err := svc.ListMcpResources(context.Background(), "nope"); restCode(err) != 40401 {
		t.Fatalf("未知 server 应 40401，got %v", err)
	}
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: "s", Name: "S", Transport: capability.TransportSSE, URL: "http://127.0.0.1:1/sse"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListMcpPrompts(context.Background(), "s"); restCode(err) != 50201 {
		t.Fatalf("sse 不可达应 50201，got %v", err)
	}
}

// TestAutoProbeMcpServers B4：后台自动探测全部已注册 server——成功者写 Tool 目录，
// 失败者记状态且不中断其余；ctx 取消即返回。
func TestAutoProbeMcpServers(t *testing.T) {
	okServer := func(name string, tools []map[string]any) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&msg)
			if msg.Method == "notifications/initialized" {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			var result map[string]any
			switch msg.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": name}}
			case "tools/list":
				result = map[string]any{"tools": tools}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	srvA := okServer("a", []map[string]any{{"name": "tool_a"}})
	srvB := okServer("b", []map[string]any{{"name": "tool_b"}})

	svc := NewService(runtime.NewRegistry(), eventbus.New())
	for _, in := range []struct{ id, url string }{{"a", srvA.URL}, {"b", srvB.URL}, {"down", "http://127.0.0.1:1"}} {
		if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: in.id, Name: in.id, Transport: capability.TransportHTTP, URL: in.url}); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.AutoProbeMcpServers(context.Background()); err != nil {
		t.Fatalf("AutoProbeMcpServers 应为尽力而为（不返回单个失败）: %v", err)
	}
	tools := svc.caps.ListTools()
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "a__tool_a,b__tool_b" {
		t.Fatalf("Tool 目录应含两个自动探测结果（远程工具带 serverID 前缀）: %v", names)
	}
	mcps, _ := svc.ListMcpServers()
	byID := map[string]dtocapability.McpServerResp{}
	for _, m := range mcps {
		byID[m.ID] = m
	}
	if byID["a"].LastProbeAt == "" || byID["b"].LastProbeAt == "" {
		t.Fatalf("成功者应有 last_probe_at: %+v", byID)
	}
	if byID["down"].LastProbeErr == "" {
		t.Fatalf("失败者应记 last_probe_error: %+v", byID["down"])
	}

	// ctx 取消：立即返回 ctx.Err()。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.AutoProbeMcpServers(ctx); err == nil {
		t.Fatal("ctx 已取消应返回错误")
	}
}
