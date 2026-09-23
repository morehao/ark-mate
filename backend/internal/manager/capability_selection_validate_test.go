package manager

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/dto/dtoconversation"
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/service/svclibrary"
	"github.com/morehao/ark-forge/internal/testutil"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// validateRig 一套带 skill 投影、两个可注入 MCP、一个 agent 的架子（M3-3 校验用）。
//
// 刻意让两个 MCP 的**可达性不同**（一个 stdio 可承载、一个 http 不被 stub 承载），
// 否则"不可达"这条断言就没有对照组。
func validateRig(t *testing.T) (*Service, *Orchestrator, *runtime.Registry) {
	t.Helper()
	spec := stubACPSpec(t)
	spec.McpTransports = []string{"stdio"}
	reg := runtime.NewRegistry(spec)
	svc := NewService(reg, eventbus.New())
	svc.agents["a1"] = &AgentRec{ID: "a1", Name: "A1", Runtimes: []string{"stub"}, State: AgentStateStopped}
	svc.SetSkillProjections([]SkillProjection{{
		Runtime:    "stub",
		Projection: opencodeSkillProjectionForTest(),
	}})
	// skill：一条正常、一条禁用、一条 runtimes 白名单不含 stub。
	for _, sk := range []capability.Skill{
		{ID: "ok-skill", Body: "# ok"},
		{ID: "off-skill", Body: "# off", Enabled: boolPtr(false)},
		{ID: "other-skill", Body: "# other", Runtimes: []string{"opencode"}},
	} {
		sk := sk
		if err := svc.caps.RegisterSkill(&sk); err != nil {
			t.Fatal(err)
		}
	}
	// MCP：stdio 可承载、http 不被 stub 承载、一条禁用。
	for _, m := range []*capability.McpServer{
		{ID: "fs", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx"},
		{ID: "web", Transport: capability.TransportHTTP, URL: "https://x.invalid/mcp"},
		{ID: "off-mcp", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx", Enabled: boolPtr(false)},
	} {
		if err := svc.caps.RegisterMcpServer(m); err != nil {
			t.Fatal(err)
		}
	}
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	return svc, orch, reg
}

// TestValidateSelectionVerdicts 逐条钉住"哪些算不可达、以及报的是哪个码"。
//
// 两类分开报的理由是修法不同：id 不存在 = 拼错了（40000 invalid_param）；
// id 存在但对该 runtime 不可达 = 定义/配置问题（40001 capability_not_applicable）。
// 两类都必须**列出原因**，且必须列出**全部**问题而不是只报第一个——
// 一次只报一个会让用户来回试错，而这些问题都是本地静态可判的。
func TestValidateSelectionVerdicts(t *testing.T) {
	svc, _, _ := validateRig(t)

	ok := func(name string, sel *CapabilitySelection) {
		t.Helper()
		if err := svc.validateSelection(sel, "stub"); err != nil {
			t.Fatalf("%s 应通过，got %v", name, err)
		}
	}
	bad := func(name string, sel *CapabilitySelection, wantNum int, wantName string, wantReasons ...string) {
		t.Helper()
		err := svc.validateSelection(sel, "stub")
		if err == nil {
			t.Fatalf("%s 应被拒", name)
		}
		if got := numericCode(err); got != wantNum {
			t.Fatalf("%s 的码 = %d，期望 %d（err=%v）", name, got, wantNum, err)
		}
		if code.FromError(err) == nil {
			t.Fatalf("%s 归一后必须有码", name)
		}
		if got := errCode(err); got != wantName {
			t.Fatalf("%s 的稳定 code = %q，期望 %q", name, got, wantName)
		}
		for _, want := range wantReasons {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s 的原因应包含 %q，got %v", name, want, err)
			}
		}
	}

	ok("未声明", nil)
	ok("声明了但什么都不限制", NewCapabilitySelection())
	ok("可达 skill + 可达 MCP", &CapabilitySelection{Skills: ptrOf([]string{"ok-skill"}), Mcps: ptrOf([]string{"fs"})})
	ok("显式空数组", &CapabilitySelection{Skills: ptrOf([]string{}), Mcps: ptrOf([]string{})})

	bad("未知 skill id", &CapabilitySelection{Skills: ptrOf([]string{"ghost"})},
		40001, "capability_not_applicable", `skill "ghost"`, "注册表里没有这个 skill")
	bad("未知 mcp id", &CapabilitySelection{Mcps: ptrOf([]string{"ghost"})},
		40001, "capability_not_applicable", `mcp "ghost"`, "注册表里没有这个 MCP server")
	bad("禁用的 skill", &CapabilitySelection{Skills: ptrOf([]string{"off-skill"})},
		40001, "capability_not_applicable", "已被禁用")
	bad("runtimes 白名单没放行", &CapabilitySelection{Skills: ptrOf([]string{"other-skill"})},
		40001, "capability_not_applicable", "runtimes 白名单没有")
	bad("runtime 不承载该 transport", &CapabilitySelection{Mcps: ptrOf([]string{"web"})},
		40001, "capability_not_applicable", "不承载 http 传输")
	bad("禁用的 mcp", &CapabilitySelection{Mcps: ptrOf([]string{"off-mcp"})},
		40001, "capability_not_applicable", "已被禁用")
	// 一次报全部：两个 skill 问题 + 一个 mcp 问题都在文案里。
	bad("多个问题一次报全",
		&CapabilitySelection{Skills: ptrOf([]string{"off-skill", "other-skill"}), Mcps: ptrOf([]string{"web"})},
		40001, "capability_not_applicable", "off-skill", "other-skill", "web")
	// 不存在（拼错）排在不可达之前，两类都列出。
	bad("存在拼错 id 时两类都列",
		&CapabilitySelection{Skills: ptrOf([]string{"ghost"}), Mcps: ptrOf([]string{"web"})},
		40001, "capability_not_applicable", "ghost", "web")
	if err := svc.validateSelection(&CapabilitySelection{Skills: ptrOf([]string{"ghost"}), Mcps: ptrOf([]string{"web"})}, "stub"); err != nil {
		if !strings.Contains(err.Error(), `skill "ghost"`) {
			t.Fatalf("不存在的 id 该排在前面: %v", err)
		}
		if strings.Index(err.Error(), `skill "ghost"`) > strings.Index(err.Error(), "不承载") {
			t.Fatalf("顺序应是「不存在」在前：%v", err)
		}
	}

	// 判定与 Library 同源：Library 说不可达的，这里也必须拒（反之亦然）。
	lib, err := svc.ListLibrary(libQueryAll())
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range lib {
		if it.Kind != "skill" && it.Kind != "mcp" {
			continue
		}
		if it.Kind == "skill" && it.ID == "other-skill" {
			continue // 它在 opencode 上可达、在 stub 上不可达；Library 是逐 runtime 报的
		}
		reachable := false
		for _, a := range it.Applicability {
			if a.Runtime == "stub" && a.OK {
				reachable = true
			}
		}
		if reachable {
			continue
		}
		// Library 判为 stub 不可达 → 选定它必须被拒。
		var sel *CapabilitySelection
		if it.Kind == "skill" {
			sel = &CapabilitySelection{Skills: ptrOf([]string{it.ID})}
		} else {
			sel = &CapabilitySelection{Mcps: ptrOf([]string{it.ID})}
		}
		if err := svc.validateSelection(sel, "stub"); err == nil {
			t.Fatalf("Library 判 %s %q 在 stub 不可达，选定校验却放行", it.Kind, it.ID)
		}
	}
}

// TestCreateConversationRejectsUnreachableWithoutSideEffects M3-3 的验收核心：
// 不可达 id → 报错**且不建会话**（不拉进程、不写库、不留代建目录）。
func TestCreateConversationRejectsUnreachableWithoutSideEffects(t *testing.T) {
	svc, orch, reg := validateRig(t)
	ctx := context.Background()
	_ = reg

	// 代建工作空间的场景最容易留副作用：ResolveWorkspace 会先建目录。
	dbPath := testutil.TempDBPath(t)
	st := testutil.OpenStore(t, dbPath)
	defer func() { _ = st.Close() }()
	svc.EnableStore(st)
	wsDir := t.TempDir()
	if err := svc.SetWorkspacePolicy(wsDir, nil); err != nil { // 允许代建、白名单不限制
		t.Fatalf("SetWorkspacePolicy: %v", err)
	}

	_, err := svc.CreateConversation(ctx, dtoconversation.ConversationCreateReq{
		AgentID:      "a1",
		Capabilities: &dtoconversation.CapabilitiesReq{Skills: ptrOf([]string{"ghost"})},
	})
	if err == nil {
		t.Fatal("不可达 id 应被拒")
	}
	if got := numericCode(err); got != 40001 {
		t.Fatalf("不可达选定的码 = %d，期望 40001", got)
	}
	if n := len(orch.conns); n != 0 {
		t.Fatalf("校验失败不该建立任何连接，got %d", n)
	}
	list, _ := st.ListConversations(ctx, nil)
	if len(list) != 0 {
		t.Fatalf("校验失败不该写库，got %+v", list)
	}
	if list, _ := svc.ListConversations(false, ""); len(list) != 0 {
		t.Fatalf("校验失败不该登记会话，got %+v", list)
	}
	// 代建目录必须被清掉（否则每被拒一次就多一个空目录）。
	entries, err := os.ReadDir(wsDir)
	if err != nil {
		t.Fatalf("读工作空间目录: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("校验失败不该留下代建目录，got %v", entries)
	}

	// 可达的选定则一切照常。
	res, err := svc.CreateConversation(ctx, dtoconversation.ConversationCreateReq{
		AgentID:      "a1",
		Capabilities: &dtoconversation.CapabilitiesReq{Mcps: ptrOf([]string{"fs"})},
	})
	if err != nil {
		t.Fatalf("可达选定应建会话成功: %v", err)
	}
	if n := len(orch.conns); n != 1 {
		t.Fatalf("应建立一个连接，got %d", n)
	}
	detail, err := svc.GetConversation(ctx, res.ID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if detail.Capabilities == nil {
		t.Fatal("声明过的会话必须回显 capabilities")
	}
	if detail.Capabilities.Mcps == nil || !sameSet(*detail.Capabilities.Mcps, []string{"fs"}) {
		t.Fatalf("回显的 mcps 不符: %+v", detail.Capabilities.Mcps)
	}
	if detail.Capabilities.Skills != nil {
		t.Fatalf("未声明的 kind 应**缺键**（回落上一层），而不是回一个空数组: %+v", *detail.Capabilities.Skills)
	}
	// 形态必须与落库的 capability_json 一致：缺键就是缺键（"未声明"不能被画成"一个都不要"）。
	raw, err := json.Marshal(detail.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"skills"`) {
		t.Fatalf("未声明的 kind 不该出现在回显里: %s", raw)
	}
	if detail.Capabilities.Fingerprint == "" {
		t.Fatal("回显应带上钉住的指纹")
	}
}

// TestCreateConversationDefaultPathUnchanged M3-3 的第二条验收：**缺省路径逐字节等同今天**。
//
// "逐字节"落在两处可观测形态上：① 响应 JSON 里**没有** `capabilities` 键（omitempty）；
// ② 不产生能力指纹（→ 代际键与引入代际之前逐字相同，不新起进程）。
func TestCreateConversationDefaultPathUnchanged(t *testing.T) {
	svc, orch, _ := validateRig(t)
	ctx := context.Background()
	// 带库：缺省路径的落库形态（v7 三列都该是空串）也是"逐字节等同今天"的一部分。
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer func() { _ = st.Close() }()
	svc.EnableStore(st)
	res, err := svc.CreateConversation(ctx, dtoconversation.ConversationCreateReq{AgentID: "a1", Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	detail, err := svc.GetConversation(ctx, res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Capabilities != nil {
		t.Fatalf("未声明能力选择时不该回显 capabilities: %+v", detail.Capabilities)
	}
	b, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"capabilities"`) {
		t.Fatalf("缺省路径的响应 JSON 里不该有 capabilities 键: %s", b)
	}
	// 记录的指纹为空 → 代际键与基础键逐字相同（同配置共享暖进程的既有规则不被破坏）。
	rec := svc.conversationRec(res.ID)
	if rec == nil || rec.Fingerprint != "" || rec.CapabilityJSON != "" {
		t.Fatalf("缺省路径不该带能力绑定: %+v", rec)
	}
	spec, _ := orch.reg.Get("stub")
	agent, _ := svc.AgentByID("a1")
	base := connKey(spec.ID, svc.nativePermissionEnvFor(spec.ID, agent.PermissionPolicy), svc.providerEnvFor(agent.Provider))
	if got := generationKey(base, rec.Fingerprint); got != base {
		t.Fatal("缺省路径的代际键必须与基础键逐字相同（R11）")
	}
	// 落库形态同样是空串（旧行与新行的默认值同形）。
	row, ok, err := st.GetConversation(ctx, res.ID)
	if err != nil || !ok {
		t.Fatalf("读库: ok=%v err=%v", ok, err)
	}
	if row.CapabilityJSON != "" || row.CapabilityFingerprint != "" || row.Origin != "" {
		t.Fatalf("缺省路径的 v7 三列都该是空串: %+v", row)
	}
}

// TestCreateConversationDeclaredEmptySelectionWorks `capabilities: {}` 是"声明了但不限制"：
// 它必须照常建会话、回显空数组、并且**产生指纹**（因此与"未声明"是两个代际）。
func TestCreateConversationDeclaredEmptySelectionWorks(t *testing.T) {
	svc, orch, _ := validateRig(t)
	ctx := context.Background()

	declared, err := svc.CreateConversation(ctx, dtoconversation.ConversationCreateReq{
		AgentID: "a1", Cwd: t.TempDir(), Capabilities: &dtoconversation.CapabilitiesReq{},
	})
	if err != nil {
		t.Fatalf("空选定应能建会话: %v", err)
	}
	plain, err := svc.CreateConversation(ctx, dtoconversation.ConversationCreateReq{AgentID: "a1", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	recD := svc.conversationRec(declared.ID)
	recP := svc.conversationRec(plain.ID)
	if recD.CapabilityJSON != "{}" {
		t.Fatalf("声明的空选定落库应为 {}，got %q", recD.CapabilityJSON)
	}
	if recD.Fingerprint == "" || recP.Fingerprint != "" {
		t.Fatalf("声明与未声明的指纹应不同: declared=%q plain=%q", recD.Fingerprint, recP.Fingerprint)
	}
	// 两个不同的代际 → 两条连接（能力集是按进程绑定的）。
	if n := len(orch.conns); n != 2 {
		t.Fatalf("声明与未声明应是两个代际（2 条连接），got %d", n)
	}
	detail, err := svc.GetConversation(ctx, declared.ID)
	if err != nil {
		t.Fatal(err)
	}
	// `{}` = 声明了但两个 kind 都不限制 → 回显对象在、两个 kind **缺键**（回落上一层）。
	if detail.Capabilities == nil {
		t.Fatal("声明过的会话必须回显 capabilities 对象")
	}
	if detail.Capabilities.Skills != nil || detail.Capabilities.Mcps != nil {
		t.Fatalf("{} 的两个 kind 都该是缺键: %+v", detail.Capabilities)
	}
	raw, err := json.Marshal(detail.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"skills"`) || strings.Contains(string(raw), `"mcps"`) {
		t.Fatalf("{} 的回显不该出现 kind 键: %s", raw)
	}
	if detail.Capabilities.Fingerprint == "" {
		t.Fatal("{} 也要有指纹（它是一次显式声明）")
	}

	// `{skills:[],mcps:[]}` = 明确一个都不要 → 回显两个空数组，且是**另一个代际**。
	none, err := svc.CreateConversation(ctx, dtoconversation.ConversationCreateReq{
		AgentID: "a1", Cwd: t.TempDir(),
		Capabilities: &dtoconversation.CapabilitiesReq{Skills: ptrOf([]string{}), Mcps: ptrOf([]string{})},
	})
	if err != nil {
		t.Fatalf("显式空数组应能建会话: %v", err)
	}
	recN := svc.conversationRec(none.ID)
	if recN.CapabilityJSON != `{"skills":[],"mcps":[]}` {
		t.Fatalf("显式空数组的落库形态: %q", recN.CapabilityJSON)
	}
	if recN.Fingerprint == recD.Fingerprint {
		t.Fatal("「不限制」与「一个都不要」是不同的能力集，必须不同指纹")
	}
	detailNone, err := svc.GetConversation(ctx, none.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detailNone.Capabilities.Skills == nil || detailNone.Capabilities.Mcps == nil ||
		len(*detailNone.Capabilities.Skills) != 0 || len(*detailNone.Capabilities.Mcps) != 0 {
		t.Fatalf("显式空数组应回显 []（与「未声明」的缺键不同）: %+v", detailNone.Capabilities)
	}
	rawNone, err := json.Marshal(detailNone.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawNone), `"skills":[]`) || !strings.Contains(string(rawNone), `"mcps":[]`) {
		t.Fatalf("显式空数组必须回显为 []: %s", rawNone)
	}
}

// TestValidateSelectionUnknownRuntimeNoPanic 未注册 runtime 不得 panic（可达性判定依赖 spec）。
func TestValidateSelectionUnknownRuntimeNoPanic(t *testing.T) {
	svc, _, _ := validateRig(t)
	err := svc.validateSelection(&CapabilitySelection{Mcps: ptrOf([]string{"fs"})}, "ghost")
	if err == nil {
		t.Fatal("未注册 runtime 应不可达")
	}
	if ae, ok := acl.As(err); !ok || ae.Code != "capability_not_applicable" {
		t.Fatalf("应是 capability_not_applicable，got %v", err)
	}
	// 一个只含空白的 id 会被规范化丢掉 → 不构成问题（与"未声明"同义）。
	if err := svc.validateSelection(&CapabilitySelection{Mcps: ptrOf([]string{"  "})}, "ghost"); err != nil {
		t.Fatalf("空白 id 规范化后应无问题，got %v", err)
	}
}

// TestSelectionProblemsAreNotCodeWrapped 校验错误必须以**已分类的 acl 错误**上抛（不被提前归一），
// 由控制器出口做 `code.FromError` 归一。在 manager 里提前归一（`code.FromError`）会把
// acl 的稳定 code 名压成 `{code,msg}`，`notFoundCodes` 这类按名字判定的提档就全失效。
func TestSelectionProblemsAreNotCodeWrapped(t *testing.T) {
	svc, _, _ := validateRig(t)
	for _, sel := range []*CapabilitySelection{
		{Skills: ptrOf([]string{"ghost"})},
		{Skills: ptrOf([]string{"off-skill"})},
		{Mcps: ptrOf([]string{"web"})},
	} {
		err := svc.validateSelection(sel, "stub")
		ae, ok := acl.As(err)
		if !ok {
			t.Fatalf("应是 acl 错误（未被提前归一）: %v", err)
		}
		if ae.Class != acl.ClassRequest {
			t.Fatalf("类应为 request，got %v", ae.Class)
		}
		if code.FromError(err) == nil {
			t.Fatal("归一后必须有码")
		}
	}
}

// libQueryAll 全量 Library 查询（校验与 Library 同源的对照用）。
func libQueryAll() svclibrary.Query { return svclibrary.Query{} }
