package session

import (
	"strings"
	"testing"
)

// ——— v4 P1：内置技能契约与注入顺序 ———

// TestSkillContractComposeOrder 技能契约块排在 expertRole 之后、memo 之前（design §2.5 同款纪律）：
// identity → expertRole → skillContract → memo → 原文。
func TestSkillContractComposeOrder(t *testing.T) {
	p := RunPrefix{Identity: "ID", ExpertRole: "ROLE", SkillContract: "SKILL", Memo: "MEMO"}
	want := "ID\n\nROLE\n\nSKILL\n\nMEMO\n\nU"
	if got := p.Compose("U"); got != want {
		t.Fatalf("注入顺序应为 identity→expertRole→skill→memo→原文：got=%q want=%q", got, want)
	}
	// 只有技能时：SKILL 在原文之前。
	if got, want := (RunPrefix{SkillContract: "SKILL"}).Compose("U"), "SKILL\n\nU"; got != want {
		t.Fatalf("单技能前缀：got=%q want=%q", got, want)
	}
}

// TestRunPrefixEmptyIncludesSkill 技能非空时 Empty 应为 false（否则调用方会跳过拼接、技能丢失）。
func TestRunPrefixEmptyIncludesSkill(t *testing.T) {
	if (RunPrefix{SkillContract: "x"}).Empty() {
		t.Fatal("Empty 判据必须包含 SkillContract，否则技能会被静默跳过")
	}
	if !(RunPrefix{}).Empty() {
		t.Fatal("全空应为 Empty")
	}
}

// TestSkillContractWhitelist 内置技能白名单：已知 id 返回带 data-role 的契约块；未知 id 报错；空 → 空。
func TestSkillContractWhitelist(t *testing.T) {
	// 空 → 空（无技能，不注入）。
	if c, err := SkillContract(""); err != nil || c != "" {
		t.Fatalf("空 id 应返回空契约且无错：c=%q err=%v", c, err)
	}
	// plan-first → 带标记的契约块。
	c, err := SkillContract("plan-first")
	if err != nil {
		t.Fatalf("plan-first 应被接收：%v", err)
	}
	if !strings.HasPrefix(c, "<system-reminder data-role=\"mate-skill\">") {
		t.Fatalf("契约块应带 data-role 标记：%q", c)
	}
	if !strings.HasSuffix(c, "</system-reminder>") {
		t.Fatalf("契约块应闭合：%q", c)
	}
	// 未知 id → 错误（不静默回落）。
	if _, err := SkillContract("no-such-skill"); err == nil {
		t.Fatal("未知技能应报错，不能静默回落")
	}
}

// TestBuiltinSkillsHasPlanFirst 内置目录必须含 plan-first（composer `/` 菜单与后端白名单同源）。
func TestBuiltinSkillsHasPlanFirst(t *testing.T) {
	found := false
	for _, s := range BuiltinSkills() {
		if s.ID == "plan-first" {
			found = true
			if s.Name == "" || s.Desc == "" {
				t.Fatalf("plan-first 的 name/desc 不应为空：%+v", s)
			}
		}
	}
	if !found {
		t.Fatal("内置技能目录缺少 plan-first")
	}
}

// TestStripSkillPrefix 剥离技能前缀往返；不匹配时原样返回（不吞用户内容）。
func TestStripSkillPrefix(t *testing.T) {
	block, err := SkillContract("plan-first")
	if err != nil {
		t.Fatal(err)
	}
	text := block + "\n\n用户原话"
	if got := StripSkillPrefix(text); got != "用户原话" {
		t.Fatalf("应剥离技能前缀，got=%q", got)
	}
	if got := StripSkillPrefix("别的文本"); got != "别的文本" {
		t.Fatalf("不匹配时应原样返回，got=%q", got)
	}
}
