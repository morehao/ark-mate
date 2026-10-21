package sessionruntime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/skill"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/infra/protocol/acp"
	"github.com/morehao/ark-mate/internal/runtime"
)

// selRig 一套「两个可注入 MCP + 一个可投影 skill」的最小装配（M3-2 的选定语义）。
func selRig(t *testing.T) *Service {
	t.Helper()
	reg := runtime.NewRegistry(runtime.Spec{
		ID: "rtx", McpTransports: []string{"stdio", "http"},
		Projections: []runtime.Projection{rtxSkillProjectionForTest()},
	})
	svc := NewService(reg, eventbus.New())
	// 本组钉的是**选择优先级**（会话选定 > 未声明时的全量），与"远程是否折叠成网关条目"
	// 无关：网关开着时远程会被折叠成一条常量条目，断言里的 id 就看不出来了。故显式关掉网关，
	// 让这组用例继续逐条断言被选中的定义；折叠语义由 gateway_folding 那组用例负责。
	svc.Planes().SetMcpGatewayEnabled(false)
	for _, m := range []*capability.McpServer{
		{ID: "fs", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx"},
		{ID: "web", Transport: capability.TransportHTTP, URL: "https://x.invalid/mcp"},
	} {
		if err := svc.planes.Capabilities.RegisterMcpServer(m); err != nil {
			t.Fatal(err)
		}
	}
	svc.planes.Skills.Projections().Set([]skill.Projection{{
		Runtime:    "rtx",
		Projection: rtxSkillProjectionForTest(),
	}})
	for _, id := range []string{"alpha", "beta"} {
		sk := capability.Skill{ID: id, Body: "# " + id}
		if err := svc.planes.Capabilities.RegisterSkill(&sk); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

// names 注入结果的 server 名（升序稳定：选择顺序已规范化，但读模型顺序取决于注册表）。
func names(servers []acp.McpServer) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.Name)
	}
	return out
}

// TestCapabilitySelectionMcpPrecedence 会话选定 > 未声明时的全量；空数组 = 一个都不要。
//
// 三态的每一态都对应一种用户表达，缺一态用户就没法说清自己的意图：
// 不传（全量）/ 传 []（这个会话什么都不要）/ 传 [id...]（只要这些）。
//
// v19 撤销 agent 面后"回落 agent 白名单"这一态不存在了：选区只有会话自己钉住的那一份。
func TestCapabilitySelectionMcpPrecedence(t *testing.T) {
	svc := selRig(t)

	// 未声明 → 全量（该 runtime 承载得起的全部）。
	if got := names(svc.McpServersFor("rtx", nil)); len(got) != 2 {
		t.Fatalf("未声明会话选定应取全量，got %v", got)
	}
	// 声明了空数组 → 一个都不要（**不是**回落全量）。
	if got := svc.McpServersFor("rtx", &session.CapabilitySelection{Mcps: ptrOf([]string{})}); len(got) != 0 {
		t.Fatalf("会话选定空数组应禁用注入，got %v", names(got))
	}
	// 声明了 web → 只要 web（求交/并集都说不清意图，命中制只有这一种读法）。
	if got := names(svc.McpServersFor("rtx", &session.CapabilitySelection{Mcps: ptrOf([]string{"web"})})); len(got) != 1 || got[0] != "web" {
		t.Fatalf("会话选定应逐条命中，got %v", got)
	}
	// 只声明 skill 侧 → MCP 仍是全量（两个 kind 互相独立）。
	if got := names(svc.McpServersFor("rtx", &session.CapabilitySelection{Skills: ptrOf([]string{"alpha"})})); len(got) != 2 {
		t.Fatalf("只声明 skill 时 MCP 应保持全量，got %v", got)
	}
	// 空选定对象（`capabilities: {}`）→ 与未声明同义：全量。
	if got := names(svc.McpServersFor("rtx", session.NewCapabilitySelection())); len(got) != 2 {
		t.Fatalf("空选定对象应取全量，got %v", got)
	}
}

// TestCapabilitySelectionSkillSubsetInFingerprint skill 子集进指纹：选了不同的技能集 →
// 不同代际；顺序不同/重复项 → **同一代际**（否则用户换个勾选顺序就白起一个进程）。
func TestCapabilitySelectionSkillSubsetInFingerprint(t *testing.T) {
	svc := selRig(t)

	all := svc.sessionPlane.CapabilityFingerprint("rtx", session.NewCapabilitySelection())
	onlyAlpha := svc.sessionPlane.CapabilityFingerprint("rtx", &session.CapabilitySelection{Skills: ptrOf([]string{"alpha"})})
	onlyBeta := svc.sessionPlane.CapabilityFingerprint("rtx", &session.CapabilitySelection{Skills: ptrOf([]string{"beta"})})
	if all == "" || onlyAlpha == "" || onlyBeta == "" {
		t.Fatal("声明了能力选择就必须有指纹")
	}
	if onlyAlpha == onlyBeta || onlyAlpha == all || onlyBeta == all {
		t.Fatalf("不同技能子集必须不同指纹: all=%s alpha=%s beta=%s", all, onlyAlpha, onlyBeta)
	}
	// 顺序/重复/空白字符不影响指纹（规范化在进指纹之前）。
	same := svc.sessionPlane.CapabilityFingerprint("rtx",
		&session.CapabilitySelection{Skills: ptrOf([]string{" beta", "beta", "alpha", ""})})
	if same != svc.sessionPlane.CapabilityFingerprint("rtx",
		&session.CapabilitySelection{Skills: ptrOf([]string{"alpha", "beta"})}) {
		t.Fatalf("顺序/重复/空白不该改变指纹: %s", same)
	}
	// 未声明 → 空指纹（R11：缺省路径的键必须与引入代际之前逐字相同）。
	if got := svc.sessionPlane.CapabilityFingerprint("rtx", nil); got != "" {
		t.Fatalf("未声明必须得空指纹，got %q", got)
	}
}

// TestCapabilitySelectionJSONRoundTrip `capability_json` 的落库形态：**空数组与缺键必须能区分**
// （都是"合法的空"，但语义相反：一个是"这一类一个都不要"，一个是"未声明、回落"）。
func TestCapabilitySelectionJSONRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		sel      *session.CapabilitySelection
		wantJSON string // "" = 空串（未声明）
	}{
		{"未声明", nil, ""},
		{"空对象（声明了但什么都不限制）", session.NewCapabilitySelection(), "{}"},
		{"显式空数组", &session.CapabilitySelection{Skills: ptrOf([]string{}), Mcps: ptrOf([]string{})},
			`{"skills":[],"mcps":[]}`},
		{"规范化（去重升序）", &session.CapabilitySelection{Skills: ptrOf([]string{"b", "a", "a"}), Mcps: ptrOf([]string{"web"})},
			`{"skills":["a","b"],"mcps":["web"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := session.MarshalCapabilitySelection(tc.sel)
			if raw != tc.wantJSON {
				t.Fatalf("序列化 = %q，期望 %q", raw, tc.wantJSON)
			}
			back := session.ParseCapabilitySelection(raw)
			if tc.sel == nil {
				if back != nil {
					t.Fatalf("空串必须读回 nil（未声明），got %+v", back)
				}
				return
			}
			if back == nil {
				t.Fatal("非空串必须读回非 nil")
			}
			// nil 与空数组的区分必须原样保留。
			if (back.Skills == nil) != (tc.sel.Skills == nil) || (back.Mcps == nil) != (tc.sel.Mcps == nil) {
				t.Fatalf("nil/空数组的区分在往返中丢失: got %+v want %+v", back, tc.sel)
			}
			// 二次序列化必须逐字相同（幂等：同一次选定不会产生两种落库形态）。
			if again := session.MarshalCapabilitySelection(back); again != raw {
				t.Fatalf("二次序列化不幂等: %q → %q", raw, again)
			}
		})
	}

	// 坏 JSON → 按未声明处理（能力选择不是安全边界；宁可回落缺省，也不让会话打不开）。
	if got := session.ParseCapabilitySelection(`{"skills":`); got != nil {
		t.Fatalf("坏 JSON 应按未声明处理，got %+v", got)
	}
}

// TestMcpInjectionRestorePathMatchesPinnedSelection M3-2 的验收核心：
// **恢复路径的注入集必须与会话钉住的那一份一致**。
//
// 场景是重启：进程内存全丢，会话记录（含 capability_json）从库里读回。漏掉这一处传参，
// 会话就会退回全量能力集——不报错、不留痕，用户只看到"我钉住的那一份没生效"。
func TestMcpInjectionRestorePathMatchesPinnedSelection(t *testing.T) {
	svc := selRig(t)

	// 会话 A 钉住"只要 web"（与全量不同，才验得出来）。
	pinned := &session.CapabilitySelection{Mcps: ptrOf([]string{"web"})}
	agentA := &session.SessionRec{ID: "convA", CapabilityJSON: session.MarshalCapabilitySelection(pinned)}
	// 会话 B 什么都不要。
	none := &session.CapabilitySelection{Mcps: ptrOf([]string{})}
	agentB := &session.SessionRec{ID: "convB", CapabilityJSON: session.MarshalCapabilitySelection(none)}
	// 会话 C 未声明（旧会话：v7 迁移后 capability_json 为空串）→ 全量。
	legacy := &session.SessionRec{ID: "convC"}

	injectedFor := func(rec *session.SessionRec) []string {
		t.Helper()
		sel := session.ParseCapabilitySelection(rec.CapabilityJSON)
		return names(svc.McpServersFor("rtx", sel))
	}

	// 恢复路径的注入集与会话钉住的选定一致。
	if got := injectedFor(agentA); len(got) != 1 || got[0] != "web" {
		t.Fatalf("会话 A 恢复后应注入 web，got %v", got)
	}
	if got := injectedFor(agentB); len(got) != 0 {
		t.Fatalf("会话 B 恢复后应一个都不注入，got %v", got)
	}
	if got := injectedFor(legacy); len(got) != 2 {
		t.Fatalf("未声明选定的旧会话应取全量，got %v", got)
	}

	// 同一份选定在"新建 → 落库 → 重启 → 恢复"后必须逐字一致（含指纹：代际不能漂）。
	before := svc.sessionPlane.CapabilityFingerprint("rtx", pinned)
	after := svc.sessionPlane.CapabilityFingerprint("rtx", session.ParseCapabilitySelection(session.MarshalCapabilitySelection(pinned)))
	if before != after {
		t.Fatalf("重启后指纹漂移：%s → %s（会话会被挂到另一个代际）", before, after)
	}
	if before == "" {
		t.Fatal("声明了选定就该有指纹")
	}
	// 反向：若恢复路径漏传选定（用 nil 算），指纹会变空——这正是要防的漂移。
	if svc.sessionPlane.CapabilityFingerprint("rtx", nil) != "" {
		t.Fatal("未声明应为空指纹")
	}
}

// TestCapabilitySelectionJSONShapeIsStable 序列化形态是外部可见的（`capability_json` 落库 +
// REST 回显），故把键名也钉住：改名会让已落库的旧行读不回来。
func TestCapabilitySelectionJSONShapeIsStable(t *testing.T) {
	raw := session.MarshalCapabilitySelection(&session.CapabilitySelection{Skills: ptrOf([]string{"s1"}), Mcps: ptrOf([]string{"m1"})})
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["skills"]; !ok {
		t.Fatalf("键名必须是 skills（已落库的行按它读回）: %s", raw)
	}
	if _, ok := probe["mcps"]; !ok {
		t.Fatalf("键名必须是 mcps: %s", raw)
	}
	if !strings.Contains(raw, `"skills":["s1"]`) || !strings.Contains(raw, `"mcps":["m1"]`) {
		t.Fatalf("形态不符: %s", raw)
	}
}
