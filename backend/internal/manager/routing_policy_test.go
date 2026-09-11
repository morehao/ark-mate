package manager

import (
	"errors"
	"os"
	"testing"

	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// TestServiceResolveRuntimeCapabilityRouted capability_routed 策略：Agent 显式 MCP 白名单
// （fs, stdio）只在能承载 stdio 的 runtime 中选；无候选可承载时报 runtime_capability_mismatch。
func TestServiceResolveRuntimeCapabilityRouted(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	svc, _, _ := rigWithRuntime(t,
		runtime.Spec{ID: "http-rt", Launch: runtime.LaunchSpec{Command: exe}, McpTransports: []string{"http"}},
		runtime.Spec{ID: "stdio-rt", Launch: runtime.LaunchSpec{Command: exe}, McpTransports: []string{"stdio"}},
	)
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{
		ID: "fs", Name: "Files", Transport: "stdio", Command: "/usr/local/bin/npx",
	}); err != nil {
		t.Fatal(err)
	}
	mcps := []string{"fs"}
	codeOf := func(err error) string {
		var ae *acl.Error
		if errors.As(err, &ae) {
			return ae.Code
		}
		return ""
	}

	seed := func(id string, policy string, runtimes []string, mcps *[]string) {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		svc.agents[id] = &AgentRec{ID: id, Name: id, Runtimes: runtimes,
			Mcps: mcps, RoutingPolicy: policy}
	}

	// http-rt 在前但承载不了 stdio → 应选 stdio-rt。
	seed("m1", "capability_routed", []string{"http-rt", "stdio-rt"}, &mcps)
	if id, err := svc.ResolveRuntime(svc.agents["m1"]); err != nil || id != "stdio-rt" {
		t.Fatalf("应跳过 http-rt 取 stdio-rt，id=%q err=%v", id, err)
	}
	// 仅 http-rt 候选 → capability mismatch。
	seed("m2", "capability_routed", []string{"http-rt"}, &mcps)
	if _, err := svc.ResolveRuntime(svc.agents["m2"]); codeOf(err) != "runtime_capability_mismatch" {
		t.Fatalf("期望 runtime_capability_mismatch，got %v", err)
	}
	// 未声明 Mcps（nil）→ 无硬约束，capability_routed 等价 first_available（取首可用）。
	seed("m3", "capability_routed", []string{"http-rt", "stdio-rt"}, nil)
	if id, err := svc.ResolveRuntime(svc.agents["m3"]); err != nil || id != "http-rt" {
		t.Fatalf("无白名单应取首可用 http-rt，id=%q err=%v", id, err)
	}
	// 未知策略回退默认 first_available。
	seed("m4", "sticky", []string{"http-rt", "stdio-rt"}, &mcps)
	if id, err := svc.ResolveRuntime(svc.agents["m4"]); err != nil || id != "http-rt" {
		t.Fatalf("未知策略应回退默认取首可用，id=%q err=%v", id, err)
	}
}
