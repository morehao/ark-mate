package sessionruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/service/svcsession"
)

// ——— M3-8：会话内改能力（技能 / MCP / 专家）———
//
// 判据分两层，缺一层都不算过：
//
//  ① **写面**：三态补丁 → 物化 → 校验 → 落库。失败必须**零副作用**（校验不过时 `capability_json`
//     一个字节都不动），因为"改了没生效"与"改了一半"是两种完全不同的故障。
//  ② **生效**：写完之后**真的**在下一个轮次边界换到新代际，而且忙时顺延、不打断当前轮。
//     只测第①层会漏掉"写对了但没人读"——那正是 M3 想消灭的静默失败。

// capEditSvc 造一次能力写面用例（依赖是同一个 Service 的两份适配器）。
func capEditSvc(svc *Service) svcsession.CapabilityEditSvc {
	return svcsession.NewCapabilityEdit(svc.sessionPlane, svc)
}

// rawPatch 拼一个三态补丁（nil 参数 = 该键缺失；字符串按**原始 JSON** 解释）。
func rawPatch(skills, mcps, expert string) dtosession.SessionCapabilitiesPatch {
	p := dtosession.SessionCapabilitiesPatch{}
	if skills != "" {
		p.Skills = json.RawMessage(skills)
	}
	if mcps != "" {
		p.Mcps = json.RawMessage(mcps)
	}
	if expert != "" {
		p.Expert = json.RawMessage(expert)
	}
	return p
}

// registerEditSession 直接登记一条会话记录（不起进程：本组只测写面与裁决，不测建连）。
func registerEditSession(t *testing.T, svc *Service, capJSON string) string {
	t.Helper()
	id := "ses_" + strings.ReplaceAll(t.Name(), "/", "_") + "_" + time.Now().Format("150405.000000000")
	svc.sessionPlane.RegisterSession(&session.SessionRec{
		ID: id, RuntimeID: "stub", Cwd: t.TempDir(), Status: session.SessionStatusCompleted,
		CapabilityJSON: capJSON,
	})
	return id
}

// TestCapabilityEditAppliesAtNextTurnBoundary 写面 + 生效链（忙则顺延、下一轮按新能力跑）。
func TestCapabilityEditAppliesAtNextTurnBoundary(t *testing.T) {
	r := newGenRig(t)
	log := sessionNewLog(t)
	r.registerMcp(t, fsServer("v1"))
	r.registerMcp(t, capability.McpServer{ID: "git", Name: "git", Transport: capability.TransportStdio, Command: "/bin/echo"})

	// 缺省路径起步：两个 MCP 都注入。
	convID, as1 := r.start(t, nil)
	edit := capEditSvc(r.svc)

	// ① 会话**忙**时改能力：接口照样成功（顺延是代际迁移的事，不是编辑的失败）。
	setBusy(as1, true)
	resp, err := edit.UpdateCapabilities(context.Background(), convID, rawPatch("", `["fs"]`, ""))
	if err != nil {
		t.Fatalf("忙时改能力应成功（下一轮生效），got %v", err)
	}
	if resp.Capabilities == nil || resp.Capabilities.Mcps == nil || !sameSet(*resp.Capabilities.Mcps, []string{"fs"}) {
		t.Fatalf("响应应回显新的选定，got %+v", resp.Capabilities)
	}
	// 写库：库里的选定明细已是新值（下一轮边界靠它算指纹）。
	if _, capJSON := r.row(t, convID); capJSON != `{"mcps":["fs"]}` {
		t.Fatalf("库里 capability_json 应已更新，got %q", capJSON)
	}
	// 当前轮不受影响：句柄没换。
	if got := r.boundary(t, convID); got != as1 {
		t.Fatal("有在途轮次时不得换代际（绝不打断当前轮）")
	}

	// ② 空闲后的下一个轮次边界：迁到新代际，底座收到的是**新**能力集。
	setBusy(as1, false)
	as2 := r.boundary(t, convID)
	if as2 == as1 {
		t.Fatal("空闲后应在下一个轮次边界迁到新代际")
	}
	if as2.Fingerprint == as1.Fingerprint {
		t.Fatal("新句柄应挂在新指纹上")
	}
	if fp, _ := r.row(t, convID); fp != as2.Fingerprint {
		t.Fatalf("库里指纹应追上实际挂上的代际：row=%q handle=%q", fp, as2.Fingerprint)
	}
	news := log()
	if len(news) < 2 {
		t.Fatalf("应有两次 session/new（建会话 + 迁移），got %d", len(news))
	}
	direct := nonGatewayServers(t, news[len(news)-1]["mcpServers"])
	if len(direct) != 1 || !strings.Contains(direct[0], "fs") {
		t.Fatalf("新代际的注入集应只剩 fs，got %v", direct)
	}
	if strings.Contains(strings.Join(direct, " "), "git") {
		t.Fatalf("新代际不得再注入白名单外的 git: %v", direct)
	}

	// ③ 再清空（三格回未声明）→ 又换一代：这条是"空选定必须写成 `{}`"的**行为**证据。
	//    若写成空串（= 未声明），`planGenerationSwitch` 会把目标指纹回落成句柄当前值，
	//    判"没变化"，清空就永远不生效。
	if _, err := edit.UpdateCapabilities(context.Background(), convID, rawPatch(`null`, `null`, `null`)); err != nil {
		t.Fatalf("清空应成功: %v", err)
	}
	as3 := r.boundary(t, convID)
	if as3 == as2 {
		t.Fatal("清空能力后应在下一个轮次边界换代际（空选定写成 {} 才判得出变化）")
	}
	direct = nonGatewayServers(t, log()[len(log())-1]["mcpServers"])
	if len(direct) != 2 {
		t.Fatalf("清空后应回到默认投放（fs + git 两个都注入），got %v", direct)
	}
}

// nonGatewayServers 把 session/new 的 mcpServers 折成非网关条目的 JSON 文本（断言用）。
func nonGatewayServers(t *testing.T, v any) []string {
	t.Helper()
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("序列化注入条目: %v", err)
		}
		if strings.Contains(string(b), protocol.GatewayServerName) {
			continue
		}
		out = append(out, string(b))
	}
	return out
}

// TestCapabilityEditMaterializesExpert 选专家 → 物化它的推荐技能/MCP + 两条边界提示。
func TestCapabilityEditMaterializesExpert(t *testing.T) {
	svc, _, _ := validateRig(t)
	registerExpert(t, svc, capability.Expert{
		ID: "data-analyst", Name: "数据分析师",
		Skills: []string{"ok-skill", "off-skill"}, Mcps: []string{"fs", "web"},
		Instructions: "你是数据分析师。", Model: "mdl_x",
	})
	convID := registerEditSession(t, svc, "")

	resp, err := capEditSvc(svc).UpdateCapabilities(context.Background(), convID, rawPatch("", "", `"data-analyst"`))
	if err != nil {
		t.Fatalf("选专家应成功: %v", err)
	}
	caps := resp.Capabilities
	if caps == nil || caps.Expert == nil || *caps.Expert != "data-analyst" {
		t.Fatalf("响应应回显专家，got %+v", caps)
	}
	// 推荐只填空缺：skills/mcps 未声明 → 吃推荐，且**只留可达的**（off-skill 禁用、web 不被 stub 承载）。
	if caps.Skills == nil || !sameSet(*caps.Skills, []string{"ok-skill"}) {
		t.Fatalf("skills 应物化为可达的 [ok-skill]，got %+v", caps.Skills)
	}
	if caps.Mcps == nil || !sameSet(*caps.Mcps, []string{"fs"}) {
		t.Fatalf("mcps 应物化为可达的 [fs]，got %+v", caps.Mcps)
	}
	// 被跳过的推荐 + 两条边界提示（人格不重注入、推荐模型不切换）。
	if !hasWarning(caps.Warnings, "skill", "off-skill") || !hasWarning(caps.Warnings, "mcp", "web") {
		t.Fatalf("被跳过的推荐应逐条告警，got %+v", caps.Warnings)
	}
	if !hasWarning(caps.Warnings, "expert", "data-analyst") {
		t.Fatalf("带角色设定的专家应提示「人格不中途重注入」，got %+v", caps.Warnings)
	}
	if !hasWarning(caps.Warnings, "model", "mdl_x") {
		t.Fatalf("专家推荐模型与当前不同应提示「不切换模型」，got %+v", caps.Warnings)
	}

	// 换专家之前先把技能写成显式白名单：下一次"只改技能"不该再弹专家提示（专家没换）。
	resp2, err := capEditSvc(svc).UpdateCapabilities(context.Background(), convID, rawPatch(`["ok-skill"]`, "", ""))
	if err != nil {
		t.Fatalf("改技能应成功: %v", err)
	}
	if hasWarning(resp2.Capabilities.Warnings, "expert", "data-analyst") {
		t.Fatal("专家没换时不该重复弹人格提示")
	}
}

// TestCapabilityEditFailuresAreCodedAndSideEffectFree 失败带码且**零副作用**。
func TestCapabilityEditFailuresAreCodedAndSideEffectFree(t *testing.T) {
	svc, _, _ := validateRig(t)
	registerExpert(t, svc, capability.Expert{ID: "disabled", Name: "停用", Enabled: boolPtr(false)})
	convID := registerEditSession(t, svc, `{"mcps":["fs"]}`)
	edit := capEditSvc(svc)

	cases := []struct {
		name   string
		patch  dtosession.SessionCapabilitiesPatch
		wantNo int
	}{
		{"技能不可达", rawPatch(`["nope"]`, "", ""), 40001},
		{"MCP 对底座不可达", rawPatch("", `["web"]`, ""), 40001},
		{"专家不存在", rawPatch("", "", `"ghost"`), 40401},
		{"专家已禁用", rawPatch("", "", `"disabled"`), 40001},
		{"内层形状不对", rawPatch(`"not-a-list"`, "", ""), 40001},
	}
	for _, c := range cases {
		_, err := edit.UpdateCapabilities(context.Background(), convID, c.patch)
		if err == nil {
			t.Fatalf("%s：应被拒", c.name)
		}
		if got := numericCode(err); got != c.wantNo {
			t.Fatalf("%s：码 = %d，期望 %d（%v）", c.name, got, c.wantNo, err)
		}
		if _, capJSON := r0Row(t, svc, convID); capJSON != `{"mcps":["fs"]}` {
			t.Fatalf("%s：被拒的请求不得改 capability_json，got %q", c.name, capJSON)
		}
	}

	// 会话不存在：同一出口的 40401。
	if _, err := edit.UpdateCapabilities(context.Background(), "ses_missing", rawPatch("", `[]`, "")); numericCode(err) != 40401 {
		t.Fatalf("未知会话应 40401，got %v", err)
	}
}

// r0Row 读内存里那条会话记录的选定明细（本组夹具没有库）。
func r0Row(t *testing.T, svc *Service, id string) (string, string) {
	t.Helper()
	rec := svc.sessionPlane.Rec(id)
	if rec == nil {
		t.Fatalf("会话 %q 不在平面里", id)
	}
	return rec.Fingerprint, rec.CapabilityJSON
}

// TestCapabilityEditClearKeepsDeclaredEmpty 清空能力写 `{}` 而不是空串。
//
// 空串是"未声明"，而 `planGenerationSwitch` 对未声明会把目标指纹回落成句柄当前值 → 判
// "没变化" → 清空永远不生效。这条用例把那个支点钉在**存储形态**上（行为那半由迁移用例覆盖）。
func TestCapabilityEditClearKeepsDeclaredEmpty(t *testing.T) {
	svc, _, _ := validateRig(t)
	convID := registerEditSession(t, svc, `{"mcps":["fs"],"skills":["ok-skill"]}`)

	// 三格都回未声明 = 回到默认投放，但**声明过**（`{}`）。
	if _, err := capEditSvc(svc).UpdateCapabilities(context.Background(), convID,
		rawPatch(`null`, `null`, `null`)); err != nil {
		t.Fatalf("清空应成功: %v", err)
	}
	_, capJSON := r0Row(t, svc, convID)
	if capJSON != "{}" {
		t.Fatalf("清空应写成 {}（不是空串），got %q", capJSON)
	}
	if session.ParseCapabilitySelection(capJSON) == nil {
		t.Fatal("{} 必须读回成「声明了但不限制」，不能读成未声明")
	}

	// 幂等：再清一次不再写库（等价短路）——它不该白起一个进程。
	if _, err := capEditSvc(svc).UpdateCapabilities(context.Background(), convID,
		rawPatch(`null`, `null`, `null`)); err != nil {
		t.Fatalf("重复清空应成功: %v", err)
	}
	if _, again := r0Row(t, svc, convID); again != "{}" {
		t.Fatalf("重复清空应保持 {}，got %q", again)
	}
}

// hasWarning 告警里有没有某一条（kind + id）。
func hasWarning(ws []dtosession.CapabilityWarningResp, kind, id string) bool {
	for _, w := range ws {
		if w.Kind == kind && w.ID == id {
			return true
		}
	}
	return false
}
