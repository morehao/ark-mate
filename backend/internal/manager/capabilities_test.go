package manager

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/capability"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
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

// TestMcpServersFor P2 MCP 注入选择（§5.2/§5.3）：注册表全量 → runtime 的 McpTransports 过滤 →
// agent.Mcps 白名单（nil = 全量；非 nil = 白名单；空 = 本 agent 禁用注入）。未知 runtime → 不下发。
func TestMcpServersFor(t *testing.T) {
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "echo", McpTransports: []string{"stdio"}},
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

	got := svc.McpServersFor(noBind, "echo")
	if len(got) != 1 || got[0].Name != "fs_stdio" || got[0].Transport != capability.TransportStdio {
		t.Fatalf("echo 仅 stdio 应取 1 条，got %+v", got)
	}
	if got := svc.McpServersFor(noBind, "opencode"); len(got) != 3 {
		t.Fatalf("opencode 未声明白名单应取 3 条，got %+v", got)
	}
	if got := svc.McpServersFor(onlyFS, "opencode"); len(got) != 1 || got[0].Name != "fs_stdio" {
		t.Fatalf("白名单 fs_stdio 应取 1 条，got %+v", got)
	}
	if got := svc.McpServersFor(denyAll, "opencode"); len(got) != 0 {
		t.Fatalf("空白名单应禁用注入，got %+v", got)
	}
	if got := svc.McpServersFor(noBind, "ghost"); len(got) != 0 {
		t.Fatalf("未知 runtime 应不下发，got %+v", got)
	}
	// Spec.McpTransports 为空 → 保守仅 stdio（ACP 全 agent 必须支持）。
	if got := svc.McpServersFor(noBind, "plain"); len(got) != 1 || got[0].Name != "fs_stdio" {
		t.Fatalf("未声明传输的 runtime 应保守取 stdio，got %+v", got)
	}
}

// TestAgentCapabilities 统一能力目录（§8.1）可见性规则：skill 按候选 runtime 裁剪、
// MCP 全量 + enabled/runtimes 标注、tool 按归属 server 的启用×可承载过滤、未知 runtime 降级记 notes。
func TestAgentCapabilities(t *testing.T) {
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "opencode", McpTransports: []string{"stdio", "sse", "http"}},
	)
	svc := NewService(reg, eventbus.New())
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
	mustSkill(capability.Skill{ID: "plan", Name: "plan", Sources: []capability.SkillSource{
		{Runtime: "opencode", Path: "p1", Format: "SKILL.md"},
		{Runtime: "deepseekharness", Path: "p2", Format: "SKILL.md"}, // 非候选 → 裁剪
	}})
	mustSkill(capability.Skill{ID: "no-src", Name: "no-src"}) // 无物化源 → 不可见
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
	// skill：plan 可见且 sources 裁到候选（仅 opencode）；no-src 无源不可见。
	if len(got.Skills) != 1 || got.Skills[0].ID != "plan" || len(got.Skills[0].Sources) != 1 ||
		got.Skills[0].Sources[0].Runtime != "opencode" {
		t.Fatalf("skills 不符: %+v", got.Skills)
	}
	// MCP：全量 3 条；白名单只启用 fs；carriers 只含能承载该传输的候选（ghost 未注册不计）。
	if len(got.McpServers) != 3 {
		t.Fatalf("mcp_servers 应全量 3 条，got %+v", got.McpServers)
	}
	byID := map[string]handler.AgentMcpServerResp{}
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

// TestAgentMcpAllowlistFromDefinitions agent 定义文件 `mcps` 三态：缺省 → nil（全启用）；
// 声明白名单 → 指针携带；`[]` → 非 nil 空指针（禁用注入）。经 LoadAgentDefinitions 装载。
func TestAgentMcpAllowlistFromDefinitions(t *testing.T) {
	dir := t.TempDir()
	writeCapFile(t, filepath.Join(dir, "a.json"), `{"id":"a","runtimes":["echo"],"mcps":["fs_stdio"]}`)
	writeCapFile(t, filepath.Join(dir, "b.json"), `{"id":"b","runtimes":["echo"],"mcps":[]}`)
	writeCapFile(t, filepath.Join(dir, "c.json"), `{"id":"c","runtimes":["echo"]}`)

	svc := NewService(runtime.NewRegistry(runtime.Spec{ID: "echo"}), eventbus.New()) // 定义引用 echo runtime
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

// TestServiceReconcileSkills 启用物化重校：Service.ReconcileSkills 把 opencode SKILL.md 源
// skill 写为 <root>/<id>/SKILL.md 并计数；无源 skill 跳过；幂等重跑 0 写。
func TestServiceReconcileSkills(t *testing.T) {
	svc, _, _ := rigWithRuntime(t)
	if err := svc.caps.RegisterSkill(&capability.Skill{
		ID: "forge-demo", Name: "Demo", Body: "# demo body",
		Sources: []capability.SkillSource{
			{Runtime: "opencode", Path: "/unused", Format: "SKILL.md"}, // root 覆盖 Path
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.caps.RegisterSkill(&capability.Skill{
		ID: "plain", Name: "P", Body: "# p", // 无 source → 跳过
	}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	w, r, err := svc.ReconcileSkills(root)
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
	if string(b) != "# demo body" {
		t.Fatal("物化内容不符")
	}
	// 幂等重跑：无写无删。
	if w, r, err := svc.ReconcileSkills(root); err != nil || w != 0 || r != 0 {
		t.Fatalf("幂等重校应 0/0，got w=%d r=%d err=%v", w, r, err)
	}
}
