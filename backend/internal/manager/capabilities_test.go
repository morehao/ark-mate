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
		`{"id":"fs","name":"Files","transport":"stdio","command":"npx","headers":{"Authorization":"Bearer x"}}`)
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

// TestMcpServersForRuntime P2 MCP 注入选择（§5.2/§5.3）：平台注册表全量按 runtime 的
// McpTransports 过滤；未知 runtime → 不下发（维持现状空注入）；headers/凭证随 acl.McpServer 携带。
func TestMcpServersForRuntime(t *testing.T) {
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "echo", McpTransports: []string{"stdio"}},
		runtime.Spec{ID: "opencode", McpTransports: []string{"stdio", "sse", "http"}},
		runtime.Spec{ID: "plain"}, // 未声明 → 保守仅 stdio
	)
	svc := NewService(reg, eventbus.New())
	must := func(id, transport string) {
		t.Helper()
		if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: id, Name: id, Transport: transport}); err != nil {
			t.Fatal(err)
		}
	}
	must("fs_stdio", capability.TransportStdio)
	must("evt_sse", capability.TransportSSE)
	must("web_http", capability.TransportHTTP)

	got := svc.McpServersForRuntime("echo")
	if len(got) != 1 || got[0].Name != "fs_stdio" || got[0].Transport != capability.TransportStdio {
		t.Fatalf("echo 仅 stdio 应取 1 条，got %+v", got)
	}
	if got := svc.McpServersForRuntime("opencode"); len(got) != 3 {
		t.Fatalf("opencode 应取 3 条，got %+v", got)
	}
	if got := svc.McpServersForRuntime("ghost"); len(got) != 0 {
		t.Fatalf("未知 runtime 应不下发，got %+v", got)
	}
	// Spec.McpTransports 为空 → 保守仅 stdio（ACP 全 agent 必须支持）。
	if got := svc.McpServersForRuntime("plain"); len(got) != 1 || got[0].Name != "fs_stdio" {
		t.Fatalf("未声明传输的 runtime 应保守取 stdio，got %+v", got)
	}
}
