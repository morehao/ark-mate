package manager

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/mcp"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// ——— M7-3 网关注入分派与指纹 ———
//
// 头号断言（本计划最重要的一条）：**远程 MCP 的变化不换代际**。它成立的原因是"条目恒定"——
// 网关条目里只有 scope（agent + runtime + 会话钉住的白名单），**没有任何每台 server 的信息**，
// 所以注册表里增删改远程 server 都不会改变注入项，也就不会改变代际键。工具面的变化由网关在
// **下一次取数**时重读定义体现（P-4：可见性下一轮、可执行性即时；M7-2 已落地）。
//
// 对照面同样重要：加一台 **stdio** server 仍然换代际——那条注入命令真的变了，底座必须被重新
// 告知（R11 的"行为不变"指的是缺省路径，不是这一条）。

// webServer 造一台远程（http）MCP 定义。
func webServer(id, url string) capability.McpServer {
	return capability.McpServer{ID: id, Name: id, Transport: capability.TransportHTTP, URL: url}
}

// TestGatewayRemoteMcpEntersFaceWithoutGenerationSwitch 远程定义/密钥引用的任何变化都不换代际；
// 加一台 stdio server 则换代际。
func TestGatewayRemoteMcpEntersFaceWithoutGenerationSwitch(t *testing.T) {
	r := newGenRig(t) // stub：其 Spec 只声明 stdio 承载（正是网关收益最明显的那种 runtime）
	log := sessionNewLog(t)

	// 会话**声明了选定**（非 nil ⇒ 有真实指纹与代际），但 Mcps 未声明 ⇒ 回落 agent 全量。
	convID, as1 := r.start(t, NewCapabilitySelection())
	if as1.Fingerprint == "" {
		t.Fatal("声明了选定就该有指纹（否则这条用例退化成缺省路径，证明不了什么）")
	}
	if got := r.boundary(t, convID); got != as1 {
		t.Fatal("没有任何变化时不得换代际")
	}

	// ① 注册表里新增一台远程 server → 注入面只多/少"有没有网关条目"这一位（这里是 0 → 1），
	//    而条目内容与远程定义无关 ⇒ 同一台 server 的后续任何改动都不该再动代际。
	r.registerMcp(t, webServer("web", "https://a.invalid/mcp"))
	if got := r.boundary(t, convID); got != as1 {
		t.Fatalf("加一台远程 server 不得换代际（got %s，want %s）", got.Fingerprint, as1.Fingerprint)
	}

	// ② 远程**定义**变化（URL、密钥引用、再加一台远程）→ 代际不变。
	//    这是"改一个凭据就换会话"那一类缺陷的正面对照：凭据轮换与 URL 变更由网关下一次取数生效。
	r.registerMcp(t, webServer("web", "https://b.invalid/mcp"))
	r.registerMcp(t, capability.McpServer{ID: "web2", Name: "web2", Transport: capability.TransportHTTP,
		URL: "https://c.invalid/mcp", SecretHeaders: map[string]string{"Authorization": "vault:web2/token"}})
	if got := r.boundary(t, convID); got != as1 {
		t.Fatalf("远程定义/密钥引用变化不得换代际（got %s，want %s）", got.Fingerprint, as1.Fingerprint)
	}

	// 注入面**确实**有东西（否则上面两条会被"什么都不注入"蒙过去）。
	news := log()
	if len(news) == 0 {
		t.Fatal("应有 session/new 记录")
	}
	servers := mcpServersOf(t, news[len(news)-1])
	if len(servers) != 1 {
		t.Fatalf("应只注入一条网关条目（远程全被折叠），got %v", servers)
	}
	entry := string(servers[0])
	for _, want := range []string{mcp.GatewayServerName, "bridge", "--agent"} {
		if !strings.Contains(entry, want) {
			t.Fatalf("网关条目应含 %q：%s", want, entry)
		}
	}
	// 凭据与远端细节**不进底座可见面**（§10.6-②）：URL 与 vault 引用都不该出现在条目里。
	for _, leak := range []string{"invalid", "vault:", "Authorization"} {
		if strings.Contains(entry, leak) {
			t.Fatalf("网关条目泄漏了远程定义/凭据（%q）：%s", leak, entry)
		}
	}

	// ③ 加一台 **stdio** server → 注入命令真的变了 ⇒ 必须换代际（行为与 M7 之前一致）。
	r.registerMcp(t, capability.McpServer{ID: "sh", Name: "sh", Transport: capability.TransportStdio, Command: "/bin/echo"})
	as2 := r.boundary(t, convID)
	if as2 == as1 {
		t.Fatal("加一台 stdio server 应换代际")
	}
	if as2.client == as1.client {
		t.Fatal("新代际必须落在另一个连接上")
	}
	// 新代际的注入集 = stdio 直连 + 网关条目（远程仍只占一条）。
	servers2 := mcpServersOf(t, log()[len(log())-1])
	if len(servers2) != 2 {
		t.Fatalf("新代际应注入「stdio 直连 + 网关」2 条，got %v", servers2)
	}
}

// TestGatewayEntryCarriesPinnedSelectionOnly 会话钉住的白名单进条目（否则钉过的会话会按 agent
// 默认面拿到更多远程 server = 授权放大），但它**只**影响 args 里的 `--mcps`：远程定义依旧不进条目。
//
// 三态必须在条目上可分辨：未声明（不带 `--mcps`）/ 显式空数组（`--mcps []`）/ 数组。
func TestGatewayEntryCarriesPinnedSelectionOnly(t *testing.T) {
	reg := stubACPSpec(t)
	reg.McpTransports = []string{"stdio"}
	svc := NewService(runtime.NewRegistry(reg), nil)
	for _, m := range []capability.McpServer{
		webServer("web", "https://x.invalid/mcp"),
		webServer("web2", "https://y.invalid/mcp"),
		{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo"},
	} {
		m := m
		if err := svc.caps.RegisterMcpServer(&m); err != nil {
			t.Fatal(err)
		}
	}
	agent := &AgentRec{ID: "a"}

	entryFor := func(sel *CapabilitySelection) string {
		t.Helper()
		entries := svc.McpServersFor(agent, "stub", sel)
		var gw *acl.McpServer
		for i := range entries {
			if entries[i].Name == mcp.GatewayServerName {
				gw = &entries[i]
			}
		}
		if gw == nil {
			return ""
		}
		return strings.Join(gw.Args, " ")
	}

	// 未声明 → 回落 agent 全量（nil 白名单 = 全量）⇒ 有网关条目，且条目里**不带** --mcps。
	open := entryFor(NewCapabilitySelection())
	if open == "" || strings.Contains(open, "--mcps") {
		t.Fatalf("未声明白名单时不该带 --mcps（否则 agent 白名单的任何改动都会换代际）：%q", open)
	}
	// 数组 → 逐字进 args（顺序/重复由 normalized 之外的原样传入：这里是用户给定顺序）。
	pinned := entryFor(&CapabilitySelection{Mcps: ptrOf([]string{"web2", "web"})})
	if !strings.Contains(pinned, `--mcps ["web2","web"]`) {
		t.Fatalf("钉住的白名单应原样进 args：%q", pinned)
	}
	// 显式空数组 → 一个远程都不要 ⇒ **没有**网关条目（连条目都不该出现）。
	if got := entryFor(&CapabilitySelection{Mcps: ptrOf([]string{})}); got != "" {
		t.Fatalf("显式空白名单不该注入网关条目：%q", got)
	}
	// 只钉 stdio server → 网关条目**照样在**（条目恒定：它不跟"定义里有没有远程"走），
	// 只是它的远程面按同一份钉住白名单取数、结果为空。
	if got := entryFor(&CapabilitySelection{Mcps: ptrOf([]string{"fs"})}); !strings.Contains(got, `--mcps ["fs"]`) {
		t.Fatalf("钉住的白名单应原样进 args（含只钉 stdio 的情形）：%q", got)
	}
	// 钉住非 nil 的**空内容**（只钉 skill 面）→ Mcps 为 nil ⇒ 回落 agent 全量，故不带 --mcps。
	if got := entryFor(&CapabilitySelection{Skills: ptrOf([]string{})}); got == "" || strings.Contains(got, "--mcps") {
		t.Fatalf("只钉 skill 面时 MCP 应回落 agent 全量（条目仍在）：%q", got)
	}
}

// TestGatewayFoldingKeepsSecretValuesOutOfFingerprint 指纹里只能有**引用名**，不能有密钥值：
// 网关把远程定义整个移出了指纹，而 stdio 那一支仍按引用入指纹（§13.4 边界①）。
func TestGatewayFoldingKeepsSecretValuesOutOfFingerprint(t *testing.T) {
	reg := stubACPSpec(t)
	reg.McpTransports = []string{"stdio"}
	svc := NewService(runtime.NewRegistry(reg), nil)
	agent := &AgentRec{ID: "a"}
	sel := NewCapabilitySelection()

	// 远程密钥引用：值不进指纹（连引用都不进——远程定义完全不在指纹里）。
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: "web", Name: "web",
		Transport: capability.TransportHTTP, URL: "https://x.invalid/mcp",
		SecretHeaders: map[string]string{"Authorization": "vault:web/sekret-value"}}); err != nil {
		t.Fatal(err)
	}
	fp := svc.capabilityFingerprint(agent, "stub", sel)
	if strings.Contains(fp, "sekret-value") || strings.Contains(fp, "vault:") {
		t.Fatalf("指纹不得含密钥值/引用：%s", fp)
	}
	// stdio 的密钥引用：引用名入指纹（换引用 = 换能力面），值仍然不入。
	if err := svc.caps.RegisterMcpServer(&capability.McpServer{ID: "fs", Name: "fs",
		Transport: capability.TransportStdio, Command: "/bin/echo",
		SecretEnv: map[string]string{"TOKEN": "vault:fs/tok"}}); err != nil {
		t.Fatal(err)
	}
	fp2 := svc.capabilityFingerprint(agent, "stub", sel)
	if fp2 == fp {
		t.Fatal("新增一台 stdio server 必须改变指纹")
	}
	if strings.Contains(fp2, "vault:fs/tok") {
		t.Fatalf("指纹只能含引用名而非引用值：%s", fp2)
	}
}

// directMcp 剔除**恒定的平台网关条目**：M7-3 之后，只要 MCP 面没被显式关掉
// （`mcps: []`），注入集里永远有一条 `forge-gateway`——它不跟定义集走，也不该干扰
// "哪几台 server 被注入"这类断言。要断言它**在不在**请用 `gatewayMcp`。
func directMcp(servers []acl.McpServer) []acl.McpServer {
	out := make([]acl.McpServer, 0, len(servers))
	for _, s := range servers {
		if s.Name == mcp.GatewayServerName {
			continue
		}
		out = append(out, s)
	}
	return out
}

// gatewayMcp 取恒定的网关条目（没有则 nil）。
func gatewayMcp(servers []acl.McpServer) *acl.McpServer {
	for i := range servers {
		if servers[i].Name == mcp.GatewayServerName {
			return &servers[i]
		}
	}
	return nil
}

// TestGatewayEntryNotInjectedIntoRemoteAcpRuntime 远程 ACP 端点拿不到网关条目。
//
// 条目是"**本机**起子进程"的配方（`<平台可执行文件> mcp bridge …`）：下发给对端等于让它按我们
// 机器上的路径起进程，而对端根本没有那个文件。这类 runtime 保持 M7 之前的形态——按自己声明的
// 传输直连注入（远端 MCP 的 URL 由对端自己连，语义成立），即**零回归**。
func TestGatewayEntryNotInjectedIntoRemoteAcpRuntime(t *testing.T) {
	spec := stubACPSpec(t)
	spec.ID = "remote"
	spec.Launch.Transport = "ws"
	spec.Launch.Endpoint = "wss://x.invalid/acp"
	spec.McpTransports = []string{"stdio", "http"}
	svc := NewService(runtime.NewRegistry(spec), nil)
	for _, m := range []capability.McpServer{
		{ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/bin/echo"},
		webServer("web", "https://x.invalid/mcp"),
	} {
		m := m
		if err := svc.caps.RegisterMcpServer(&m); err != nil {
			t.Fatal(err)
		}
	}
	got := svc.McpServersFor(&AgentRec{ID: "a"}, "remote", nil)
	if gw := gatewayMcp(got); gw != nil {
		t.Fatalf("远程端点不该拿到网关条目（本机路径对它无意义）：%+v", gw)
	}
	names := make([]string, 0, len(got))
	for _, m := range got {
		names = append(names, m.Name)
	}
	if strings.Join(names, ",") != "fs,web" {
		t.Fatalf("远程端点应回到直连口径（stdio + 它声明的远程传输），got %v", names)
	}
}

// mcpServersOf 取桩 runtime 记录里某次 session/new 的 mcpServers 原始 JSON 片段。
func mcpServersOf(t *testing.T, rec map[string]any) []json.RawMessage {
	t.Helper()
	raw, ok := rec["mcpServers"]
	if !ok {
		t.Fatalf("session/new 记录里没有 mcpServers：%v", rec)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out []json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("mcpServers 不是数组: %s", b)
	}
	return out
}
