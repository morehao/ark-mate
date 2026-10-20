package sessionruntime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/skill"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/testutil"
)

// fpRig 一套「已注册 runtime + 一个已解析 skill 投影」的最小装配。
func fpRig(t *testing.T) *Service {
	t.Helper()
	reg := runtime.NewRegistry(runtime.Spec{
		ID: "rtx", Name: "rtx", Version: "rtx acp",
		McpTransports:   []string{"stdio", "http"},
		CapabilityScope: runtime.CapabilityScopeProcess,
		Projections:     []runtime.Projection{rtxSkillProjectionForTest()},
	})
	svc := newServiceForTest(reg, eventbus.New())
	planesOf(svc).Skills.Projections().Set([]skill.Projection{{
		Runtime: "rtx",
		Projection: runtime.Projection{
			Kind: runtime.ProjectionSkill, Scope: runtime.ScopeGlobal, Root: t.TempDir(),
			Layout: runtime.LayoutIDToken + "/SKILL.md", Format: capability.FormatSkillMD, Tested: true,
		},
	}})
	return svc
}

// rtxSkillProjectionForTest 测试用的 skill 投影形态（与生产 adapter 声明同形）。
func rtxSkillProjectionForTest() runtime.Projection {
	return runtime.Projection{
		Kind: runtime.ProjectionSkill, Scope: runtime.ScopeGlobal, Root: "${HOME}/.agents/skills",
		Layout: runtime.LayoutIDToken + "/SKILL.md", Format: capability.FormatSkillMD,
	}
}

// TestCapabilityFingerprintDeclaredGate `declared=false` 必须得到**空指纹**：这是 R11
// （缺省零回归）的载体——connKey 见到空指纹要保持与今天逐字相同，同配置的两个会话仍共享一个暖进程。
func TestCapabilityFingerprintDeclaredGate(t *testing.T) {
	svc := fpRig(t)
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", nil); got != "" {
		t.Fatalf("未声明能力选择应得空指纹，got %q", got)
	}
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection()); got == "" {
		t.Fatal("声明了能力选择应有非空指纹")
	}
}

// TestCapabilityFingerprintMCPSecretValueNotInFingerprint 计划里点名的那条：**改密钥值不改指纹、
// 改引用名改指纹**。
//
// v20 起密钥值在库里（`mcp_secrets`）：换一份值（轮换密钥）→ 指纹不变（否则一次密钥轮换会让所有
// 会话换一次底座进程）；而把引用名换掉（换一把钥匙 / 换一个库里条目）→ 指纹变。
func TestCapabilityFingerprintMCPSecretValueNotInFingerprint(t *testing.T) {
	home := t.TempDir()
	svc := providerTestSvc(t, filepath.Join(home, "providers"))
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	enableStore(svc, st)
	ctx := context.Background()
	const ref = "store:mcp/fs-token"
	if err := st.SetMcpSecret(ctx, ref, "secret-one"); err != nil {
		t.Fatal(err)
	}
	planesOf(svc).Skills.Projections().Set([]skill.Projection{{
		Runtime: "stub",
		Projection: runtime.Projection{
			Kind: runtime.ProjectionSkill, Scope: runtime.ScopeGlobal, Root: t.TempDir(),
			Layout: runtime.LayoutIDToken + "/SKILL.md", Format: capability.FormatSkillMD, Tested: true,
		},
	}})

	reg := func(secretRef string) {
		t.Helper()
		planesOf(svc).Capabilities = capability.NewRegistry()
		rewireFacts(svc)
		if err := planesOf(svc).Capabilities.RegisterMcpServer(&capability.McpServer{
			ID: "fs", Name: "fs", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
			Args:      []string{"-y", "pkg"},
			Env:       map[string]string{"FS_ROOT": "/tmp/root"},
			SecretEnv: map[string]string{"FS_TOKEN": secretRef},
		}); err != nil {
			t.Fatal(err)
		}
	}
	reg(ref)
	base := svc.sessionPlane.CapabilityFingerprint("stub", session.NewCapabilitySelection())
	if base == "" {
		t.Fatal("应有非空指纹")
	}
	// 记录一下：解析出来的明文确实存在且会进注入值（对照面）。
	got := directMcp(svc.McpServersFor("stub", nil)) // 网关条目另计（它不含密钥）
	if len(got) != 1 || got[0].Env["FS_TOKEN"] != "secret-one" {
		t.Fatalf("注入侧应解析出明文: %+v", got)
	}

	// 1) 轮换密钥值（同一引用指向的内容变了）→ 指纹不变。
	if err := st.SetMcpSecret(ctx, ref, "secret-two-rotated"); err != nil {
		t.Fatal(err)
	}
	if again := svc.sessionPlane.CapabilityFingerprint("stub", session.NewCapabilitySelection()); again != base {
		t.Fatalf("轮换密钥值不该改指纹:\n%s\n%s", base, again)
	}
	// 2) 换明文 env 的值 → 同样不改指纹（只取键名）。
	reg(ref)
	m := planesOf(svc).Capabilities.ListMcpServers()[0]
	m.Env = map[string]string{"FS_ROOT": "/other/root"}
	if err := planesOf(svc).Capabilities.RegisterMcpServer(&m); err != nil {
		t.Fatal(err)
	}
	if again := svc.sessionPlane.CapabilityFingerprint("stub", session.NewCapabilitySelection()); again != base {
		t.Fatalf("明文 env 的值不该改指纹（只取键名）:\n%s\n%s", base, again)
	}
	// 3) 换引用名 → 指纹必须变（换了一把钥匙）。
	reg("store:mcp/other-token")
	if again := svc.sessionPlane.CapabilityFingerprint("stub", session.NewCapabilitySelection()); again == base {
		t.Fatal("换密钥引用名必须改指纹")
	}
	// 4) 换明文 env 的**键名** → 也必须变。
	reg(ref)
	m2 := planesOf(svc).Capabilities.ListMcpServers()[0]
	m2.Env = map[string]string{"FS_BASE": "/tmp/root"}
	if err := planesOf(svc).Capabilities.RegisterMcpServer(&m2); err != nil {
		t.Fatal(err)
	}
	if again := svc.sessionPlane.CapabilityFingerprint("stub", session.NewCapabilitySelection()); again == base {
		t.Fatal("换明文 env 键名必须改指纹")
	}
}

// TestCapabilityFingerprintSkillSelection 指纹只纳入**真正会投放**的 skill：
// 禁用的、不声明给该 runtime 的都不进（它们对这个进程的能力集没有影响）。
func TestCapabilityFingerprintSkillSelection(t *testing.T) {
	svc := fpRig(t)
	must := func(sk capability.Skill) {
		t.Helper()
		if err := planesOf(svc).Capabilities.RegisterSkill(&sk); err != nil {
			t.Fatal(err)
		}
	}
	must(capability.Skill{ID: "alpha", Runtimes: []string{"rtx"}, Body: "# alpha"})
	base := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection())

	// 加一条不投给 rtx 的 → 指纹不变。
	must(capability.Skill{ID: "for-dsh", Runtimes: []string{"dsh"}, Body: "# d"})
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection()); got != base {
		t.Fatal("不投给该 runtime 的 skill 不该影响指纹")
	}
	// 加一条禁用但适用的 → 指纹不变（禁用 = 不投放）。
	off := false
	must(capability.Skill{ID: "off", Runtimes: []string{"rtx"}, Body: "# off", Enabled: &off})
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection()); got != base {
		t.Fatal("禁用的 skill 不该影响指纹")
	}
	// 改已投放 skill 的正文 → 指纹必变。
	alpha := capability.Skill{ID: "alpha", Runtimes: []string{"rtx"}, Body: "# alpha v2"}
	if err := planesOf(svc).Capabilities.RegisterSkill(&alpha); err != nil {
		t.Fatal(err)
	}
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection()); got == base {
		t.Fatal("正文变了必须改指纹")
	}
	// 启用一条新 skill → 指纹必变。
	on := true
	must(capability.Skill{ID: "off", Runtimes: []string{"rtx"}, Body: "# off", Enabled: &on})
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection()); got == base {
		t.Fatal("新增会投放的 skill 必须改指纹")
	}
}

// TestCapabilityFingerprintBodyDigestReusesSidecarHash 正文摘要必须复用物化 sidecar 的算法：
// "指纹说没变"与"产物要重写"不能各说各话。
func TestCapabilityFingerprintBodyDigestReusesSidecarHash(t *testing.T) {
	svc := fpRig(t)
	if err := planesOf(svc).Capabilities.RegisterSkill(&capability.Skill{ID: "alpha", Body: "# alpha"}); err != nil {
		t.Fatal(err)
	}
	parts := session.ProjectedSkillParts(svc.CapabilityFactsFor("rtx", nil), nil)
	if len(parts) != 1 {
		t.Fatalf("应有 1 条投放片段: %+v", parts)
	}
	if parts[0].BodyDigest != capability.PayloadHash("# alpha") {
		t.Fatalf("正文摘要必须复用 capability.PayloadHash: %q", parts[0].BodyDigest)
	}
}

// TestCapabilityFingerprintRuntimeShape runtime 的形态（版本 / 绑定粒度 / 投影）都必须参与指纹：
// 底座升级或投影约定变化后，"这个会话在哪个底座形态上跑的"必须可答。
func TestCapabilityFingerprintRuntimeShape(t *testing.T) {
	svc := fpRig(t)
	if err := planesOf(svc).Capabilities.RegisterSkill(&capability.Skill{ID: "alpha", Body: "# a"}); err != nil {
		t.Fatal(err)
	}
	base := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection())

	sp, _ := svc.regSpec("rtx")
	sp.Version = "rtx acp v2"
	planesOf(svc).Registry.Register(sp)
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection()); got == base {
		t.Fatal("底座版本变了必须改指纹")
	}
	// 还原版本，改投影 root → 也要变。
	sp.Version = "rtx acp"
	sp.Projections[0].Root = "${HOME}/.config/rtx/skill"
	planesOf(svc).Registry.Register(sp)
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection()); got == base {
		t.Fatal("投影形态变了必须改指纹")
	}
}

// TestCapabilityFingerprintNoProjectionMeansNoSkillPart 没有 skill 投影的 runtime（远程端点那样）
// 不因 skill 定义变化而换代际——技能根本没投给它。
func TestCapabilityFingerprintNoProjectionMeansNoSkillPart(t *testing.T) {
	reg := runtime.NewRegistry(runtime.Spec{ID: "remote", Version: "remote acp"})
	svc := newServiceForTest(reg, eventbus.New())
	if err := planesOf(svc).Capabilities.RegisterSkill(&capability.Skill{ID: "alpha", Body: "# a"}); err != nil {
		t.Fatal(err)
	}
	if parts := session.ProjectedSkillParts(svc.CapabilityFactsFor("remote", nil), nil); len(parts) != 0 {
		t.Fatalf("无 skill 投影的 runtime 不该有投放片段: %+v", parts)
	}
	base := svc.sessionPlane.CapabilityFingerprint("remote", session.NewCapabilitySelection())
	if err := planesOf(svc).Capabilities.RegisterSkill(&capability.Skill{ID: "beta", Body: "# b"}); err != nil {
		t.Fatal(err)
	}
	if got := svc.sessionPlane.CapabilityFingerprint("remote", session.NewCapabilitySelection()); got != base {
		t.Fatal("无投影时 skill 变化不该改指纹")
	}
}

// TestCapabilityFingerprintMCPTransportFilter 按 runtime 可承载传输过滤：不该承载的 server
// 不进指纹（它不会被注入）。
//
// **M7-3 之后这条只在网关关（MATE_MCP_GATEWAY=off）时成立**：网关在环时远程 server 对每个
// runtime 都可达（条目不逐条出现，折叠成一条常量条目），故"不可承载 ⇒ 不影响指纹"这个前提
// 本身消失了——网关在环时的折叠语义见 `TestCapabilityFingerprintGatewayFolding`。
func TestCapabilityFingerprintMCPTransportFilter(t *testing.T) {
	svc := fpRig(t) // rtx: stdio + http
	planesOf(svc).SetMcpGatewayEnabled(false)
	if err := planesOf(svc).Capabilities.RegisterMcpServer(&capability.McpServer{
		ID: "fs", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
	}); err != nil {
		t.Fatal(err)
	}
	base := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection())
	// sse 不在该 runtime 的承载列表里 → 不影响指纹。
	if err := planesOf(svc).Capabilities.RegisterMcpServer(&capability.McpServer{
		ID: "sse", Transport: capability.TransportSSE, URL: "https://x.invalid/sse",
	}); err != nil {
		t.Fatal(err)
	}
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection()); got != base {
		t.Fatal("不可承载传输的 server 不该影响指纹")
	}
	// 会话钉住的白名单（v19：选区只有这一处来源）：不在名单里的 server 不进指纹。
	one := []string{"fs"}
	both := []string{"fs", "sse"}
	both2 := []string{"fs", "http-ok"}
	onlyFS := svc.sessionPlane.CapabilityFingerprint("rtx", &session.CapabilitySelection{Mcps: &one})
	if withBoth := svc.sessionPlane.CapabilityFingerprint("rtx", &session.CapabilitySelection{Mcps: &both}); withBoth != onlyFS {
		// 白名单里多写了一个**该 runtime 不承载**的 id（sse）：注入集一模一样，所以指纹也必须
		// 一模一样。"声明了但用不上"要以能力面板里的原因呈现，不是靠多起一个进程来表达。
		t.Fatal("白名单里的不可承载项不该改变指纹")
	}
	// 白名单外再放一个可承载的 server：没被选中 → 指纹不变。
	if err := planesOf(svc).Capabilities.RegisterMcpServer(&capability.McpServer{
		ID: "http-ok", Transport: capability.TransportHTTP, URL: "https://x.invalid/mcp",
	}); err != nil {
		t.Fatal(err)
	}
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", &session.CapabilitySelection{Mcps: &one}); got != onlyFS {
		t.Fatal("白名单外新增 server 不该影响指纹（没被选中）")
	}
	// 把它一起选进白名单 → 指纹必变。
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", &session.CapabilitySelection{Mcps: &both2}); got == onlyFS {
		t.Fatal("选中新增的 server 必须改指纹")
	}
}
