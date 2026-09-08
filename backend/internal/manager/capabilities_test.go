package manager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-forge/internal/capability"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
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

// TestServiceMaterializeSkills 启用物化：Service.MaterializeSkills 把 opencode SKILL.md 源
// skill 写为 <root>/<id>/SKILL.md 并计数；无源/不可物化者跳过不中断。
func TestServiceMaterializeSkills(t *testing.T) {
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
	n, err := svc.MaterializeSkills(root)
	if err != nil {
		t.Fatalf("MaterializeSkills: %v", err)
	}
	if n != 1 {
		t.Fatalf("期望物化 1 条，got %d", n)
	}
	p := filepath.Join(root, "forge-demo", "SKILL.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "# demo body" {
		t.Fatal("物化内容不符")
	}
}
