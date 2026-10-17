package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/morehao/ark-mate/internal/domain/session"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/service/svccapability"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/mcp"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/gerror"
)

// mcpCatalog 取 MCP 注册表读模型的**用例层**（P4a 正文第十四刀：读模型从 manager 收进
// `svccapability`，夹具链不变——注册表/平面/密钥面本来就是真的）。
func mcpCatalog(svc *Service) svccapability.McpCatalog {
	return svccapability.NewServers(svc.Planes().Capabilities, svc.Planes().Mcp, svc.Planes().McpSecrets)
}

// skillPlaneOf 取技能域平面：技能目录的投影在域里（`skill.Plane.ListSkills`），
// `manager` 侧那份一行转发已删（P4a 正文第十三刀）。
func skillPlaneOf(svc *Service) *skill.Plane { return svc.planes.Skills }

// toolCatalog 取工具目录的**用例层**（P4a 正文第十二刀：读模型从 manager 收进 svccapability）。
func toolCatalog(svc *Service) svccapability.ToolCatalog {
	return svccapability.NewTools(svc.Planes().Capabilities)
}

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
	if n := svc.Planes().LoadCapabilityDefinitions(dir); n != 3 {
		t.Fatalf("期望加载 3 条，got %d", n)
	}

	skills, _ := skillPlaneOf(svc).ListSkills()
	if len(skills) != 1 || skills[0].ID != "plan" || skills[0].Name != "规划" {
		t.Fatalf("skills 不符: %+v", skills)
	}

	mcps, _ := mcpCatalog(svc).ListMcpServers()
	if len(mcps) != 1 || mcps[0].ID != "fs" || mcps[0].Transport != "stdio" {
		t.Fatalf("mcps 不符: %+v", mcps)
	}

	tools, _ := toolCatalog(svc).ListTools()
	if len(tools) != 1 || tools[0].Name != "web_search" || tools[0].Source != capability.ToolSourceBuiltin {
		t.Fatalf("tools 不符: %+v", tools)
	}
}

// TestRegisterToolAndSort 代码注入工具 + 列表排序（工具表按 name）。
func TestRegisterToolAndSort(t *testing.T) {
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	if err := svc.Planes().RegisterTool(capability.Tool{Name: "z_tool", Source: capability.ToolSourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Planes().RegisterTool(capability.Tool{Name: "a_tool", Source: capability.ToolSourceObserved, Description: "obs"}); err != nil {
		t.Fatal(err)
	}
	tools, _ := toolCatalog(svc).ListTools()
	if len(tools) != 2 || tools[0].Name != "a_tool" || tools[1].Name != "z_tool" {
		t.Fatalf("tools 排序不符: %+v", tools)
	}
}

// TestMcpServersFor P2 MCP 注入选择（§5.2/§5.3）+ M7-3 的传输分派：stdlib 直连、远程折叠成网关条目。
//
// 语义（M7-3 之后）：
//   - **网关开（默认）**：stdio server 原样直连；远程 server 不再逐条直连，而是合成**一条**
//     `mate-gateway` stdio 条目 —— 于是"该 runtime 声明的传输"不再决定远程 server 的去留
//     （那正是网关的收益），仅决定 stdio 那一支。
//   - **网关关（MATE_MCP_GATEWAY=off）**：回到老路 —— 按该 runtime 的 McpTransports 过滤后逐条直连。
//   - 白名单优先级两态都生效：nil = 全量；非 nil = 命中制；空 = 本会话禁用注入。未知 runtime → 不下发。
func TestMcpServersFor(t *testing.T) {
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "stub", McpTransports: []string{"stdio"}},
		runtime.Spec{ID: "rtx", McpTransports: []string{"stdio", "sse", "http"}},
		runtime.Spec{ID: "plain"}, // 未声明 → 保守仅 stdio
	)
	svc := NewService(reg, eventbus.New())
	must := func(id, transport string) {
		t.Helper()
		// stdio server 须绝对 command（ACP 校验；注册表已拦相对路径）。
		if err := svc.planes.Capabilities.RegisterMcpServer(&capability.McpServer{ID: id, Name: id, Transport: transport, Command: "/usr/local/bin/npx"}); err != nil {
			t.Fatal(err)
		}
	}
	must("fs_stdio", capability.TransportStdio)
	must("evt_sse", capability.TransportSSE)
	must("web_http", capability.TransportHTTP)

	// v19：选区就是会话自己钉住的那一份（agent 声明面已撤销），nil = 未声明 → 全量。
	noBind := &session.CapabilitySelection{}
	onlyFS := &session.CapabilitySelection{Mcps: ptrOf([]string{"fs_stdio"})}
	denyAll := &session.CapabilitySelection{Mcps: ptrOf([]string{})}

	// 网关条目断言：一条 stdio 条目、命令是平台自身、参数是 mcp bridge + 稳定 scope。
	assertGatewayEntry := func(entries []acl.McpServer) {
		t.Helper()
		last := entries[len(entries)-1]
		if last.Name != mcp.GatewayServerName || last.Transport != capability.TransportStdio {
			t.Fatalf("末条应是网关条目（stdio/mate-gateway），got %+v", last)
		}
		// 网关条目用**本进程的可执行文件**；测试进程就是本包的测试二进制，
		// 所以判据取 `os.Args[0]` 的基名（写死包名会在改名后假红）。
		if last.Command == "" || !strings.HasSuffix(last.Command, filepath.Base(os.Args[0])) {
			t.Fatalf("网关条目命令应为平台自身可执行文件（%s），got %q", filepath.Base(os.Args[0]), last.Command)
		}
		args := strings.Join(last.Args, " ")
		if !strings.HasPrefix(args, "mcp bridge --runtime stub") {
			t.Fatalf("网关条目参数应是 mcp bridge + 稳定 scope，got %v", last.Args)
		}
	}

	// 网关开（默认）：stdio 直连 1 条 + 网关 1 条。注意 stub **只声明 stdio**，而远程 server
	// 照样经网关可达——这正是 §10.6-① 的收益。
	got := svc.McpServersFor("stub", noBind)
	if len(got) != 2 || got[0].Name != "fs_stdio" || got[0].Transport != capability.TransportStdio {
		t.Fatalf("仅 stdio 的 runtime 应取「stdio 直连 + 网关」2 条，got %+v", got)
	}
	assertGatewayEntry(got)
	if got := svc.McpServersFor("rtx", noBind); len(got) != 2 {
		t.Fatalf("rtx 未声明白名单应取「stdio 直连 + 网关」2 条（不再逐条直连远程），got %+v", got)
	}
	// 白名单只钉 stdio server：直连 1 条 + **网关条目照样在**（条目恒定，不跟定义集走）；
	// 它的远程面会是空的（网关按同一份白名单取数），所以"钉了 stdio 就没网关条目"不是判据。
	if got := svc.McpServersFor("rtx", onlyFS); len(got) != 2 || got[0].Name != "fs_stdio" || got[1].Name != mcp.GatewayServerName {
		t.Fatalf("白名单 fs_stdio 应取「直连 1 条 + 恒定网关条目」，got %+v", got)
	}
	if got := svc.McpServersFor("rtx", denyAll); len(got) != 0 {
		t.Fatalf("空白名单应禁用注入，got %+v", got)
	}
	if got := svc.McpServersFor("ghost", noBind); len(got) != 0 {
		t.Fatalf("未知 runtime 应不下发，got %+v", got)
	}
	// Spec.McpTransports 为空 → 该 runtime 的直连只承载 stdio（ACP 全 agent 必须支持）。
	if got := svc.McpServersFor("plain", noBind); len(got) != 2 || got[0].Name != "fs_stdio" {
		t.Fatalf("未声明传输的 runtime 应取「stdio 直连 + 网关」，got %+v", got)
	}

	// 回退开关：off → 老口径逐条直连（远程按该 runtime 的声明过滤）。
	svc.Planes().SetMcpGatewayEnabled(false)
	if got := svc.McpServersFor("stub", noBind); len(got) != 1 || got[0].Name != "fs_stdio" {
		t.Fatalf("网关关时应回到「仅承载得起的直连」，got %+v", got)
	}
	if got := svc.McpServersFor("rtx", noBind); len(got) != 3 {
		t.Fatalf("网关关时 rtx 应逐条直连 3 台，got %+v", got)
	}
	if got := svc.McpServersFor("plain", noBind); len(got) != 1 {
		t.Fatalf("网关关时未声明传输的 runtime 应保守仅 stdio，got %+v", got)
	}
}

// TestProbeMcpServer MCP live 探测（P2 §5.2）：http 探测成功 → Tool 目录整体替换 + 归属
// 标注 + 状态记录；重复探测收敛（旧条目删除）；失败记录 last_probe_error；未知 id → 40401；
// sse 端点不可达 → 50201（连接失败）。
//
// P4a 正文第三十八刀起经 `McpPlane().Probe` 调用（`manager` 那两条转发已删）：这一层验的是
// **装配起来的那份 Service + 真平面 + 真远端**，与域自己的纯单测不互相替代。
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
	if err := svc.planes.Capabilities.RegisterMcpServer(&capability.McpServer{ID: "h", Name: "H", Transport: capability.TransportHTTP, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	// 预置一条同名旧条目 + 一条 builtin（替换不得误伤）。
	if err := svc.planes.Capabilities.RegisterTool(&capability.Tool{Name: "read_file", Source: capability.ToolSourceMCP, McpServer: "h", Description: "旧描述"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.planes.Capabilities.RegisterTool(&capability.Tool{Name: "web_search", Source: capability.ToolSourceBuiltin}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.Planes().Mcp.Probe(context.Background(), "h")
	if err != nil {
		t.Fatalf("Plane.Probe: %v", err)
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
	all, _ := toolCatalog(svc).ListTools()
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
	mcps, _ := mcpCatalog(svc).ListMcpServers()
	if len(mcps) != 1 || mcps[0].ToolCount != 1 || mcps[0].LastProbeAt == "" || mcps[0].LastProbeErr != "" {
		t.Fatalf("mcps 探测状态不符: %+v", mcps)
	}

	// 第二轮：server 只剩 write_file → read_file 删除、write_file 写入（整体替换）。
	tools = []map[string]any{{"name": "write_file"}}
	got2, err := svc.Planes().Mcp.Probe(context.Background(), "h")
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

	// 未知 id → 40401；sse 端点不可达 → 50201（不臆造成功）。
	if _, err := svc.Planes().Mcp.Probe(context.Background(), "nope"); err == nil {
		t.Fatal("未知 server 应报错")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != 40401 {
			t.Fatalf("未知 server 应 40401，got %v", err)
		}
	}
	if err := svc.planes.Capabilities.RegisterMcpServer(&capability.McpServer{ID: "sse1", Transport: capability.TransportSSE, URL: "http://127.0.0.1:1/sse"}); err != nil {
		t.Fatal(err)
	}
	// sse（B3 起已支持）指向不可达端点 → 50201 连接失败（不再是不支持 40501）。
	if _, err := svc.Planes().Mcp.Probe(context.Background(), "sse1"); err == nil {
		t.Fatal("sse 不可达应报错")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != 50201 {
			t.Fatalf("sse 不可达应 50201，got %v", err)
		}
	}
	// 失败状态落盘可读回。
	mcps, _ = mcpCatalog(svc).ListMcpServers()
	for _, m := range mcps {
		if m.ID == "sse1" && !strings.Contains(m.LastProbeErr, "sse") {
			t.Fatalf("sse1 应记录 last_probe_error，got %+v", m)
		}
	}
}

func mustListTools(t *testing.T, svc *Service) []dtocapability.ToolResp {
	t.Helper()
	out, err := toolCatalog(svc).ListTools()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestServiceReconcileSkills 启用物化重校：按投影形态写为 <root>/<id>/SKILL.md 并计数；
// runtimes 不含本投影方的 skill 跳过；幂等重跑 0 写。
func TestServiceReconcileSkills(t *testing.T) {
	svc, _, _ := rigWithRuntime(t)
	if err := svc.planes.Capabilities.RegisterSkill(&capability.Skill{
		ID: "mate-demo", Name: "Demo", Body: "# demo body",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.planes.Capabilities.RegisterSkill(&capability.Skill{
		ID: "plain", Name: "P", Body: "# p", Runtimes: []string{"dsh"}, // 不投放给 rtx → 跳过
	}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	w, r, err := svc.planes.Skills.ReconcileSkillProjection(skillProj(root, "rtx"))
	if err != nil {
		t.Fatalf("ReconcileSkills: %v", err)
	}
	if w != 1 || r != 0 {
		t.Fatalf("期望 written=1 removed=0，got w=%d r=%d", w, r)
	}
	p := filepath.Join(root, "mate-demo", "SKILL.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "---\nname: \"mate-demo\"\ndescription: \"Demo\"\n---\n# demo body" {
		t.Fatalf("物化内容不符（应带 frontmatter）: %q", b)
	}
	// 幂等重跑：无写无删。
	if w, r, err := svc.planes.Skills.ReconcileSkillProjection(skillProj(root, "rtx")); err != nil || w != 0 || r != 0 {
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
	if err := svc.planes.Capabilities.RegisterMcpServer(&capability.McpServer{ID: "h", Name: "H", Transport: capability.TransportHTTP, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.Planes().Mcp.CallTool(context.Background(), "h", "read_file", dtocapability.McpToolCallReq{Arguments: json.RawMessage(`{"path":"/tmp/a"}`)})
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
	gotErr, err := svc.Planes().Mcp.CallTool(context.Background(), "h", "boom", dtocapability.McpToolCallReq{})
	if err != nil {
		t.Fatalf("工具级失败不应报错: %v", err)
	}
	if !gotErr.IsError || gotErr.Text != "工具内部失败" {
		t.Fatalf("is_error 透传不符: %+v", gotErr)
	}

	// 未知 server → 40401。
	if _, err := svc.Planes().Mcp.CallTool(context.Background(), "nope", "t", dtocapability.McpToolCallReq{}); restCode(err) != 40401 {
		t.Fatalf("未知 server 应 40401，got %v", err)
	}
	// sse（B3 起已支持）指向不可达端点 → 连接失败 50201（不再是不支持 40501）。
	if err := svc.planes.Capabilities.RegisterMcpServer(&capability.McpServer{ID: "s", Name: "S", Transport: capability.TransportSSE, URL: "http://127.0.0.1:1/sse"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Planes().Mcp.CallTool(context.Background(), "s", "t", dtocapability.McpToolCallReq{}); restCode(err) != 50201 {
		t.Fatalf("sse 不可达应 50201，got %v", err)
	}
	// 非法 arguments → 40001（ClassRequest）。
	if _, err := svc.Planes().Mcp.CallTool(context.Background(), "h", "read_file", dtocapability.McpToolCallReq{Arguments: json.RawMessage(`{oops`)}); restCode(err) != 40001 {
		t.Fatalf("非法 arguments 应 40001，got %v", err)
	}
	// 空工具名 → 40001。
	if _, err := svc.Planes().Mcp.CallTool(context.Background(), "h", "", dtocapability.McpToolCallReq{}); restCode(err) != 40001 {
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
	if err := svc.planes.Capabilities.RegisterMcpServer(&capability.McpServer{ID: "slow", Name: "Slow", Transport: capability.TransportHTTP, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Planes().Mcp.CallTool(context.Background(), "slow", "t", dtocapability.McpToolCallReq{TimeoutMS: 50})
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
	if err := svc.planes.Capabilities.RegisterMcpServer(&capability.McpServer{ID: "h", Name: "H", Transport: capability.TransportHTTP, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Planes().Mcp.ListResources(context.Background(), "h")
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

	rd, err := svc.Planes().Mcp.ReadResource(context.Background(), "h", "file:///tmp/a.txt")
	if err != nil {
		t.Fatalf("ReadMcpResource: %v", err)
	}
	if len(rd.Contents) != 1 || rd.Contents[0].Text != "内容" || rd.URI != "file:///tmp/a.txt" {
		t.Fatalf("读取结果不符: %+v", rd)
	}
	if _, err := svc.Planes().Mcp.ReadResource(context.Background(), "h", "  "); restCode(err) != 40001 {
		t.Fatalf("空 uri 应 40001，got %v", err)
	}

	ps, err := svc.Planes().Mcp.ListPrompts(context.Background(), "h")
	if err != nil {
		t.Fatalf("ListMcpPrompts: %v", err)
	}
	if len(ps.Prompts) != 1 || ps.Prompts[0].Name != "greet" || len(ps.Prompts[0].Arguments) != 1 ||
		!ps.Prompts[0].Arguments[0].Required {
		t.Fatalf("提示词列表不符: %+v", ps)
	}

	gp, err := svc.Planes().Mcp.GetPrompt(context.Background(), "h", "greet", dtocapability.McpPromptGetReq{Arguments: map[string]string{"who": "世界"}})
	if err != nil {
		t.Fatalf("GetMcpPrompt: %v", err)
	}
	if gp.Description != "问候模板" || len(gp.Messages) != 1 || gp.Messages[0].Role != "user" {
		t.Fatalf("提示词结果不符: %+v", gp)
	}
	if _, err := svc.Planes().Mcp.GetPrompt(context.Background(), "h", "", dtocapability.McpPromptGetReq{}); restCode(err) != 40001 {
		t.Fatalf("空提示词名应 40001，got %v", err)
	}

	// 未知 server → 40401；sse → 40501。
	if _, err := svc.Planes().Mcp.ListResources(context.Background(), "nope"); restCode(err) != 40401 {
		t.Fatalf("未知 server 应 40401，got %v", err)
	}
	if err := svc.planes.Capabilities.RegisterMcpServer(&capability.McpServer{ID: "s", Name: "S", Transport: capability.TransportSSE, URL: "http://127.0.0.1:1/sse"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Planes().Mcp.ListPrompts(context.Background(), "s"); restCode(err) != 50201 {
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
		if err := svc.planes.Capabilities.RegisterMcpServer(&capability.McpServer{ID: in.id, Name: in.id, Transport: capability.TransportHTTP, URL: in.url}); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.Planes().Mcp.AutoProbe(context.Background()); err != nil {
		t.Fatalf("AutoProbe 应为尽力而为（不返回单个失败）: %v", err)
	}
	tools := svc.planes.Capabilities.ListTools()
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "a__tool_a,b__tool_b" {
		t.Fatalf("Tool 目录应含两个自动探测结果（远程工具带 serverID 前缀）: %v", names)
	}
	mcps, _ := mcpCatalog(svc).ListMcpServers()
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
	if err := svc.Planes().Mcp.AutoProbe(ctx); err == nil {
		t.Fatal("ctx 已取消应返回错误")
	}
}
