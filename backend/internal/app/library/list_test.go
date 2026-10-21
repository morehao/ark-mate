package library

import (
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/contract/dtolibrary"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 可达性判决的判据（P4a 正文第六刀：随实现从 `manager/library.go` 搬来） ———
//
// 这一条原先是 `manager/skills_test.go` 里的用例：判定的**唯一实现**在库读面这一侧，
// 所以判据跟着它走。

func boolPtr(v bool) *bool { return &v }

// TestSkillRuntimeVerdictReasons 可达性判定（§13.4 S5）：每个 status 都可判定、都给人话原因。
//
// v19：判定入口 `SkillActivationFor(sk, 候选 runtime, inAgent, projected)` 随 agent 候选面撤销，
// 收口成 `skillRuntimeVerdict(sk, 投影)`——**唯一实现**（Library 清单与建会话的选定校验共用它）。
// 于是这里的输入从"agent 的候选列表"变成"本进程解析出的那一条投影"。
func TestSkillRuntimeVerdictReasons(t *testing.T) {
	proj := func(rt string, tested bool, errText string) skill.Projection {
		return skill.Projection{Runtime: rt, Err: errText, Projection: runtime.Projection{
			Kind: runtime.ProjectionSkill, Root: "/root/" + rt,
			Layout: runtime.LayoutIDToken + "/SKILL.md", Tested: tested,
		}}
	}

	// 适用（未声明 runtimes = 全部）+ 实测过 → projected。
	if got := skillRuntimeVerdict(capability.Skill{ID: "a", Runtimes: []string{"rtx"}}, proj("rtx", true, "")); got.Status != dtolibrary.StatusProjected || !got.OK {
		t.Fatalf("适用且实测过应 projected: %+v", got)
	}
	// 未实测 → untested，但**照常投放**（OK=true：不担保不等于不投放）。
	if got := skillRuntimeVerdict(capability.Skill{ID: "a"}, proj("rtx", false, "")); got.Status != dtolibrary.StatusUntested || !got.OK {
		t.Fatalf("未实测应 untested 且 OK=true: %+v", got)
	}
	// 白名单不含它 → not_applicable，并给出改法。
	if got := skillRuntimeVerdict(capability.Skill{ID: "c", Runtimes: []string{"stub"}}, proj("rtx", true, "")); got.Status != dtolibrary.StatusNotApplicable || !strings.Contains(got.Reason, "runtimes") {
		t.Fatalf("白名单不相交应 not_applicable 并给出改法: %+v", got)
	}
	// 声明了投影但本进程没解析出落点 → not_configured，给出**配置出路**（不是只说不可用）。
	if got := skillRuntimeVerdict(capability.Skill{ID: "f"}, proj("rtx", false, "没有投影根")); got.Status != dtolibrary.StatusNotConfigured ||
		!strings.Contains(got.Reason, "MATE_PROJECTION_DIRS") {
		t.Fatalf("未解析出落点应 not_configured 且指向配置项: %+v", got)
	}
	// 禁用优先于一切，且原因里给出恢复动作。
	if got := skillRuntimeVerdict(capability.Skill{ID: "g", Enabled: boolPtr(false)}, proj("rtx", true, "")); got.Status != dtolibrary.StatusDisabled ||
		!strings.Contains(got.Reason, "enabled:true") {
		t.Fatalf("禁用应最先判且给出恢复动作: %+v", got)
	}
}
