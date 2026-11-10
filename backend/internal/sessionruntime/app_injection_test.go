package sessionruntime

import (
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— WP5 T5.4：app 业务工具独立注入通道的对照测试（G3）———
//
// 逐 kind 三态（nil / 空 / 非空）× app scope：断言注入集合。
// 越权红线（设计 §5.5 不变量①「越权不可发生」）：app 想加、session 空数组 → 0 例外。
// 平台注入恒化不动：本组测试同时验证 platformMcpServers 的注入与 app 通道**互不干扰**。

// fakeAppScope 记忆化 app id → 工具域的映射（模拟装配根的实现）。
//
// 未知 app id（不在 map 里）→ 不贡献任何域（与真实实现"停用/删除 app 不贡献"同口径）。
type fakeAppScope struct {
	domainsByApp map[string][]string
	// wantToAdd 是**越权探针**：即便某个 app 想额外贡献这些域，也只有在 session Apps 非空时才会被问到。
	// 它验证"app 想加、session 空数组 → 0"——空数组时根本不会调用本端口。
	calls int
}

func (f *fakeAppScope) EnabledToolDomains(appIDs []string) []string {
	f.calls++
	seen := map[string]bool{}
	var out []string
	for _, id := range appIDs {
		for _, d := range f.domainsByApp[id] {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}

// appSvc 造一个装了 stub（local stdio）+ remote（远程端点）两个 runtime、并接入 app 工具域的 Service。
func appSvc(t *testing.T, scope AppToolScope) *Service {
	t.Helper()
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "stub", McpTransports: []string{"stdio"}},
		runtime.RemoteSpec("remote", "Remote", "ws", "wss://x.invalid/acp", nil),
	)
	svc := NewService(reg, eventbus.New())
	if scope != nil {
		svc.SetAppToolScope(scope)
	}
	return svc
}

// TestAppMcpZeroInjectionByDefault 三态 nil / 空数组 → 零 app 工具（默认零是硬要求，G6）。
func TestAppMcpZeroInjectionByDefault(t *testing.T) {
	scope := &fakeAppScope{domainsByApp: map[string][]string{"fooapp": {"foo"}}}
	svc := appSvc(t, scope)

	for name, sel := range map[string]*session.CapabilitySelection{
		"nil 未声明": selOf(nil),
		"空数组明确不要": selOf(&[]string{}),
	} {
		name, sel := name, sel
		t.Run(name, func(t *testing.T) {
			scope.calls = 0
			got := svc.McpServersFor("stub", sel)
			if hasAppServer(got) {
				t.Fatalf("%s：不该注入任何 app 工具，got %+v", name, namesOf(got))
			}
			if sel != nil && sel.Apps != nil && len(*sel.Apps) == 0 && scope.calls != 0 {
				t.Fatal("空数组（明确不要）时不该询问 app 端口（越权探针见 TestAppMcpNoPrivilegeEscalation）")
			}
		})
	}
}

// TestAppMcpInjectsSelectedApps 非空 app 白名单 → 注入单条合并条目，Args 含排序后的 --apps。
func TestAppMcpInjectsSelectedApps(t *testing.T) {
	scope := &fakeAppScope{domainsByApp: map[string][]string{
		"fooapp": {"foo"},
		"barapp": {"bar"},
	}}
	svc := appSvc(t, scope)
	sel := selOf(&[]string{"fooapp", "barapp"})

	got := svc.McpServersFor("stub", sel)
	entry := findAppMcpServer(got)
	if entry == nil {
		t.Fatalf("启用两个 app 应注入一条 app 工具 server，got %+v", namesOf(got))
	}
	if entry.Name != AppMcpAppName || entry.Transport != "stdio" || entry.Command == "" {
		t.Fatalf("app 条目形状不对：%+v", entry)
	}
	args := strings.Join(entry.Args, " ")
	if !strings.HasPrefix(args, "mcp app --apps=") {
		t.Fatalf("app 条目参数应为 'mcp app --apps=...'，got %q", args)
	}
	// 合并成一条（不是每 app 一条）：单条 --apps=a,b，升序。
	if !strings.Contains(args, "--apps=barapp,fooapp") {
		t.Fatalf("--apps 应升序合并 barapp,fooapp，got %q", args)
	}
	var appCount int
	for _, m := range got {
		if m.Name == AppMcpAppName {
			appCount++
		}
	}
	if appCount != 1 {
		t.Fatalf("应恰注入一条 app 条目（合并），got %d", appCount)
	}
}

// TestAppMcpNoPrivilegeEscalation 越权红线（设计 §5.5）：app 想加、session 空数组 → 0 例外。
//
// 造一个"贪心"端口：无论问什么都返回域（模拟某个 app 想扩张工具面）。会话钉 `[]`（明确不要 app）
// 时，appendAppMcpServers **根本不该询问端口**，注入集合必须为空——0 例外。
func TestAppMcpNoPrivilegeEscalation(t *testing.T) {
	greedy := &greedyScope{domains: []string{"foo", "bar", "baz"}}
	svc := appSvc(t, greedy)

	emptyApps := &session.CapabilitySelection{Apps: &[]string{}}
	got := svc.McpServersFor("stub", emptyApps)
	if hasAppServer(got) {
		t.Fatalf("越权：session 空数组时 app 不得注入任何工具，got %+v", namesOf(got))
	}
	if greedy.calls != 0 {
		t.Fatalf("越权：session 空数组时根本不该询问 app 端口，got calls=%d", greedy.calls)
	}

	// 同理 nil（未声明）也不该问端口：默认零注入。
	nilScope := &greedyScope{domains: []string{"foo"}}
	svc2 := appSvc(t, nilScope)
	if got := svc2.McpServersFor("stub", &session.CapabilitySelection{}); hasAppServer(got) {
		t.Fatalf("越权：nil 未声明时 app 不得注入任何工具，got %+v", namesOf(got))
	}
	if nilScope.calls != 0 {
		t.Fatalf("越权：nil 未声明时不该询问 app 端口，got calls=%d", nilScope.calls)
	}
}

// TestAppMcpRemoteEndpointSkipped 远程端点（本机无该可执行文件）不注入 app stdio 条目。
func TestAppMcpRemoteEndpointSkipped(t *testing.T) {
	scope := &fakeAppScope{domainsByApp: map[string][]string{"fooapp": {"foo"}}}
	svc := appSvc(t, scope)
	got := svc.McpServersFor("remote", selOf(&[]string{"fooapp"}))
	if hasAppServer(got) {
		t.Fatalf("远程端点不该注入 app stdio server，got %+v", namesOf(got))
	}
}

// TestAppMcpPlatformUnaffected 平台注入恒化：app 通道开关/存在都不改变平台内置与网关条目。
//
// 两个方向都断：默认会话平台内置照常注入；带 app scope 的会话平台内置也照常注入（互不干扰）。
func TestAppMcpPlatformUnaffected(t *testing.T) {
	scope := &fakeAppScope{domainsByApp: map[string][]string{"fooapp": {"foo"}}}
	svc := appSvc(t, scope)
	svc.planes.SetPlatformMcpEnabled(true)

	// 带 app scope：平台内置仍在。
	got := svc.McpServersFor("stub", selOf(&[]string{"fooapp"}))
	if !hasName(got, PlatformMcpPlatformName) {
		t.Fatalf("带 app scope 时平台内置仍应注入（恒注入不可退让），got %+v", namesOf(got))
	}
	if !hasAppServer(got) {
		t.Fatalf("带 app scope 时应注入 app 工具面，got %+v", namesOf(got))
	}

	// 默认会话（无 app）：平台内置照常，无 app 工具。
	got2 := svc.McpServersFor("stub", &session.CapabilitySelection{})
	if !hasName(got2, PlatformMcpPlatformName) {
		t.Fatalf("默认会话平台内置应注入，got %+v", namesOf(got2))
	}
	if hasAppServer(got2) {
		t.Fatalf("默认会话不该有 app 工具，got %+v", namesOf(got2))
	}
}

// TestAppMcpFingerprint 进代际指纹：app scope 改变 ⇒ 声明会话指纹改变；缺省路径（nil）恒空。
func TestAppMcpFingerprint(t *testing.T) {
	scope := &fakeAppScope{domainsByApp: map[string][]string{
		"fooapp": {"foo"},
		"barapp": {"bar"},
	}}
	svc := appSvc(t, scope)

	// 同一份声明、不同 app scope → 指纹不同（app 工具面真的变了）。
	fpFoo := svc.sessionPlane.CapabilityFingerprint("stub", selOf(&[]string{"fooapp"}))
	fpFooBar := svc.sessionPlane.CapabilityFingerprint("stub", selOf(&[]string{"fooapp", "barapp"}))
	if fpFoo == "" || fpFooBar == "" {
		t.Fatal("声明了 app 的会话应有指纹")
	}
	if fpFoo == fpFooBar {
		t.Fatal("app scope 变化应改变指纹（否则注入了不同 app 工具却不换代际）")
	}

	// 有 app vs 无 app 的同一会话 → 指纹不同（多了 app 工具 server）。
	fpNoApp := svc.sessionPlane.CapabilityFingerprint("stub", &session.CapabilitySelection{})
	if fpNoApp == fpFoo {
		t.Fatal("有/无 app 工具的会话指纹应不同")
	}

	// 缺省路径（sel==nil）恒空（零回归）。
	if svc.sessionPlane.CapabilityFingerprint("stub", nil) != "" {
		t.Fatal("缺省路径（sel==nil）指纹必须恒为空串")
	}
}

// ——— 测试辅助 ———

type greedyScope struct {
	domains []string
	calls   int
}

func (g *greedyScope) EnabledToolDomains([]string) []string { g.calls++; return g.domains }

func selOf(apps *[]string) *session.CapabilitySelection {
	return &session.CapabilitySelection{Apps: apps}
}

// findAppMcpServer 在注入结果里找 app 通道条目（找不到返回 nil）。
func findAppMcpServer(got []protocol.McpServer) *protocol.McpServer {
	for i := range got {
		if got[i].Name == AppMcpAppName {
			return &got[i]
		}
	}
	return nil
}

// hasAppServer 注入结果里有没有 app 通道条目。
func hasAppServer(got []protocol.McpServer) bool { return findAppMcpServer(got) != nil }

// hasName 注入结果里有没有指定 name 的 server。
func hasName(got []protocol.McpServer, name string) bool {
	for _, m := range got {
		if m.Name == name {
			return true
		}
	}
	return false
}

// namesOf 注入结果的 server 名（调试/断言输出）。
func namesOf(got []protocol.McpServer) []string {
	out := make([]string, 0, len(got))
	for _, m := range got {
		out = append(out, m.Name)
	}
	return out
}
