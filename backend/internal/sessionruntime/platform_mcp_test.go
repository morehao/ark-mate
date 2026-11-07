package sessionruntime

import (
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 平台内置 MCP server 注入（automation / material 工具面）———
//
// 覆盖：默认注入两条（automation+material，命令是平台自身）；开关 off 不注入；
// 会话钉 `[]` 不注入；远程端点不注入；钉白名单仍注入（平台内置是产品默认能力）；
// 指纹：声明选定的会话会因平台内置变化换代际（它们真的多了工具），缺省路径（nil）仍恒空。

// platformSvc 造一个装了 stub（local stdio）+ remote（远程端点）两个 runtime 的 Service，
// 并按需开启平台内置注入。
func platformSvc(t *testing.T, enabled bool) *Service {
	t.Helper()
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "stub", McpTransports: []string{"stdio"}},
		runtime.RemoteSpec("remote", "Remote", "ws", "wss://x.invalid/acp", nil),
	)
	svc := NewService(reg, eventbus.New())
	svc.planes.SetPlatformMcpEnabled(enabled)
	return svc
}

func TestPlatformMcpInjectedByDefault(t *testing.T) {
	svc := platformSvc(t, true)
	sel := &session.CapabilitySelection{}
	got := svc.McpServersFor("stub", sel)
	var gotNames []string
	for _, m := range got {
		gotNames = append(gotNames, m.Name)
	}
	joined := strings.Join(gotNames, ",")
	if !strings.Contains(joined, PlatformMcpPlatformName) || !strings.Contains(joined, PlatformMcpMaterialName) {
		t.Fatalf("默认应注入两条平台内置 server（%s/%s），got %v",
			PlatformMcpPlatformName, PlatformMcpMaterialName, gotNames)
	}
	// 命令应为平台自身可执行文件，参数是 mcp platform / mcp material。
	for _, m := range got {
		if m.Name == PlatformMcpPlatformName {
			if !strings.HasSuffix(m.Command, "/") && m.Command == "" {
				t.Fatalf("平台内置命令不能为空: %+v", m)
			}
			if strings.Join(m.Args, " ") != "mcp platform" {
				t.Fatalf("platform 参数应为 'mcp platform'，got %v", m.Args)
			}
		}
		if m.Name == PlatformMcpMaterialName && strings.Join(m.Args, " ") != "mcp material" {
			t.Fatalf("material 参数应为 'mcp material'，got %v", m.Args)
		}
	}
}

func TestPlatformMcpToggleOff(t *testing.T) {
	svc := platformSvc(t, false)
	got := svc.McpServersFor("stub", &session.CapabilitySelection{})
	for _, m := range got {
		if m.Name == PlatformMcpPlatformName || m.Name == PlatformMcpMaterialName {
			t.Fatalf("开关 off 时不该注入平台内置 server，got %+v", m)
		}
	}
}

func TestPlatformMcpDenyAllSession(t *testing.T) {
	svc := platformSvc(t, true)
	empty := []string{}
	denyAll := &session.CapabilitySelection{Mcps: &empty}
	if got := svc.McpServersFor("stub", denyAll); len(got) != 0 {
		t.Fatalf("会话钉 [] 时平台内置也不注入，got %d 条", len(got))
	}
}

func TestPlatformMcpRemoteEndpointSkipped(t *testing.T) {
	svc := platformSvc(t, true)
	got := svc.McpServersFor("remote", &session.CapabilitySelection{})
	for _, m := range got {
		if m.Name == PlatformMcpPlatformName || m.Name == PlatformMcpMaterialName {
			t.Fatalf("远程端点（本机无该可执行文件）不该注入平台内置 stdio server，got %+v", m)
		}
	}
}

func TestPlatformMcpWhitelistStillInjects(t *testing.T) {
	svc := platformSvc(t, true)
	// 注册一台 stdio 用户 server，会话只钉它；平台内置仍应注入（产品默认能力不属可选集）。
	if err := svc.planes.Capabilities.RegisterMcpServer(&capability.McpServer{
		ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
	}); err != nil {
		t.Fatal(err)
	}
	only := []string{"fs"}
	got := svc.McpServersFor("stub", &session.CapabilitySelection{Mcps: &only})
	var hasPlatform bool
	for _, m := range got {
		if m.Name == PlatformMcpPlatformName {
			hasPlatform = true
		}
	}
	if !hasPlatform {
		t.Fatalf("钉白名单时平台内置仍应注入，got %+v", got)
	}
}

// TestPlatformMcpFingerprint PinnedSessions 平台内置进指纹；缺省路径（nil）恒空（零回归）。
func TestPlatformMcpFingerprintPinnedAndDefault(t *testing.T) {
	svcOn := platformSvc(t, true)
	svcOff := platformSvc(t, false)
	sel := &session.CapabilitySelection{} // 非 nil ⇒ 有指纹

	fpOn := svcOn.sessionPlane.CapabilityFingerprint("stub", sel)
	fpOff := svcOff.sessionPlane.CapabilityFingerprint("stub", sel)
	if fpOn == "" || fpOff == "" {
		t.Fatal("非缺省会话应有指纹")
	}
	if fpOn == fpOff {
		t.Fatal("平台内置开关变化应改变声明选定会话的指纹（否则注入了新工具却不换代际）")
	}
	// 缺省路径（sel == nil）必须恒空，不受平台内置开关影响。
	if svcOn.sessionPlane.CapabilityFingerprint("stub", nil) != "" || svcOff.sessionPlane.CapabilityFingerprint("stub", nil) != "" {
		t.Fatal("缺省路径（sel==nil）指纹必须恒为空串（零回归）")
	}
}
