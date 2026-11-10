package sessionruntime

import (
	"testing"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/domain/session"
)

// ——— WP-C4 T4.c：运行期包工具注入通道的对照测试 ———

// fakeRuntimeScope 模拟装配根：把某些 app id 映射成运行期包 stdio 适配条目；记录被问次数。
type fakeRuntimeScope struct {
	serversByApp map[string]capability.McpServer
	calls        int
}

func (f *fakeRuntimeScope) RuntimeAppServers(appIDs []string) []capability.McpServer {
	f.calls++
	var out []capability.McpServer
	seen := map[string]bool{}
	for _, id := range appIDs {
		if s, ok := f.serversByApp[id]; ok && !seen[s.ID] {
			seen[s.ID] = true
			out = append(out, s)
		}
	}
	return out
}

func runtimeSvc(t *testing.T, scope RuntimeAppToolScope) *Service {
	t.Helper()
	svc := appSvc(t, nil) // 复用 app_injection_test 的 service 装配（stub 本地 + remote 两个 runtime）
	svc.SetRuntimeAppToolScope(scope)
	return svc
}

func rtServer(id, args string) capability.McpServer {
	return capability.McpServer{
		ID: id, Name: "mate-rt-" + id, Transport: capability.TransportStdio,
		Command: "/self/mate", Args: []string{"mcp", "app", "--apps=" + args},
	}
}

// TestRuntimeAppZeroInjectionByDefault 三态 nil / 空数组 → 零运行期工具、且不询问端口（默认零注入）。
func TestRuntimeAppZeroInjectionByDefault(t *testing.T) {
	scope := &fakeRuntimeScope{serversByApp: map[string]capability.McpServer{"rtapp": rtServer("rtapp", "rtapp")}}
	svc := runtimeSvc(t, scope)

	for name, sel := range map[string]*session.CapabilitySelection{
		"nil 未声明": selOf(nil),
		"空数组明确不要": selOf(&[]string{}),
	} {
		name, sel := name, sel
		t.Run(name, func(t *testing.T) {
			scope.calls = 0
			got := svc.McpServersFor("stub", sel)
			if hasRuntimeServer(got) {
				t.Fatalf("%s：不该注入任何运行期工具，got %+v", name, namesOf(got))
			}
			if scope.calls != 0 {
				t.Fatalf("%s：默认/空时根本不该询问运行期端口（越权红线），got calls=%d", name, scope.calls)
			}
		})
	}
}

// TestRuntimeAppInjectsWhenSelected 非空 app 白名单里有运行期包 app → 注入其 stdio 适配条目。
func TestRuntimeAppInjectsWhenSelected(t *testing.T) {
	scope := &fakeRuntimeScope{serversByApp: map[string]capability.McpServer{
		"rtapp": rtServer("rtapp", "rtapp"),
	}}
	svc := runtimeSvc(t, scope)
	got := svc.McpServersFor("stub", selOf(&[]string{"rtapp"}))
	entry := findRuntimeServer(got)
	if entry == nil {
		t.Fatalf("选中的运行期包 app 应注入适配条目，got %+v", namesOf(got))
	}
	if entry.Transport != capability.TransportStdio || entry.Command == "" {
		t.Fatalf("运行期条目形状不对：%+v", entry)
	}
}

// TestRuntimeAppNoPrivilegeEscalation 越权 0 例外：session 空数组 → 端口不被问、零注入（贪心端口）。
func TestRuntimeAppNoPrivilegeEscalation(t *testing.T) {
	greedy := &greedyRuntime{}
	svc := runtimeSvc(t, greedy)

	got := svc.McpServersFor("stub", &session.CapabilitySelection{Apps: &[]string{}})
	if hasRuntimeServer(got) {
		t.Fatalf("越权：session 空数组时不得注入运行期工具，got %+v", namesOf(got))
	}
	if greedy.calls != 0 {
		t.Fatalf("越权：session 空数组时根本不该询问运行期端口，got calls=%d", greedy.calls)
	}

	svc2 := runtimeSvc(t, &greedyRuntime{})
	if got := svc2.McpServersFor("stub", &session.CapabilitySelection{}); hasRuntimeServer(got) {
		t.Fatalf("越权：nil 未声明时不得注入运行期工具，got %+v", namesOf(got))
	}
}

type greedyRuntime struct{ calls int }

func (g *greedyRuntime) RuntimeAppServers([]string) []capability.McpServer {
	g.calls++
	return []capability.McpServer{rtServer("greedy", "greedy")}
}

// TestRuntimeAppRemoteEndpointSkipped 远程端点不注入运行期 stdio 条目。
func TestRuntimeAppRemoteEndpointSkipped(t *testing.T) {
	scope := &fakeRuntimeScope{serversByApp: map[string]capability.McpServer{"rtapp": rtServer("rtapp", "rtapp")}}
	svc := runtimeSvc(t, scope)
	got := svc.McpServersFor("remote", selOf(&[]string{"rtapp"}))
	if hasRuntimeServer(got) {
		t.Fatalf("远程端点不该注入运行期 stdio server，got %+v", namesOf(got))
	}
}

// TestPlatformUnaffectedByRuntimeApp 平台注入恒化：运行期通道存在与否都不改平台内置注入。
func TestPlatformUnaffectedByRuntimeApp(t *testing.T) {
	scope := &fakeRuntimeScope{serversByApp: map[string]capability.McpServer{"rtapp": rtServer("rtapp", "rtapp")}}
	svc := runtimeSvc(t, scope)
	svc.planes.SetPlatformMcpEnabled(true)

	got := svc.McpServersFor("stub", selOf(&[]string{"rtapp"}))
	if !hasName(got, PlatformMcpPlatformName) {
		t.Fatalf("带运行期 app scope 时平台内置仍应注入（恒注入不可退让），got %+v", namesOf(got))
	}
	if !hasRuntimeServer(got) {
		t.Fatalf("应同时注入运行期工具面，got %+v", namesOf(got))
	}

	got2 := svc.McpServersFor("stub", &session.CapabilitySelection{})
	if !hasName(got2, PlatformMcpPlatformName) {
		t.Fatalf("默认会话平台内置应注入，got %+v", namesOf(got2))
	}
	if hasRuntimeServer(got2) {
		t.Fatalf("默认会话不该有运行期工具，got %+v", namesOf(got2))
	}
}

// TestRuntimeAppFingerprint 进代际指纹：选不同运行期 app → 指纹变化；有/无 → 不同。
func TestRuntimeAppFingerprint(t *testing.T) {
	scope := &fakeRuntimeScope{serversByApp: map[string]capability.McpServer{
		"rt1": rtServer("rt1", "rt1"),
		"rt2": rtServer("rt2", "rt2"),
	}}
	svc := runtimeSvc(t, scope)

	fp1 := svc.sessionPlane.CapabilityFingerprint("stub", selOf(&[]string{"rt1"}))
	fp12 := svc.sessionPlane.CapabilityFingerprint("stub", selOf(&[]string{"rt1", "rt2"}))
	if fp1 == "" || fp12 == "" {
		t.Fatal("声明了运行期 app 的会话应有指纹")
	}
	if fp1 == fp12 {
		t.Fatal("运行期 app scope 变化应改变指纹")
	}
	if svc.sessionPlane.CapabilityFingerprint("stub", &session.CapabilitySelection{}) == fp1 {
		t.Fatal("有/无运行期工具会话指纹应不同")
	}
}

// ——— 辅助 ———

func findRuntimeServer(got []protocol.McpServer) *protocol.McpServer {
	for i := range got {
		// 运行期条目用 rtServer 造，Name 前缀 "mate-rt-"，与平台内置（mate-platform）、
		// 编译期 app 通道（mate-app）区分开。
		if len(got[i].Name) >= 8 && got[i].Name[:8] == "mate-rt-" &&
			got[i].Transport == capability.TransportStdio && got[i].Command != "" {
			return &got[i]
		}
	}
	return nil
}

func hasRuntimeServer(got []protocol.McpServer) bool { return findRuntimeServer(got) != nil }
