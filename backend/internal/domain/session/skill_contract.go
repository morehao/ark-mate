package session

import (
	"fmt"
	"strings"
)

// ——— 内置技能契约（P1 · v4）———
//
// v4 把「计划」从**会话模式**降为一个**一次性技能** `plan-first`（design §0-4、§5.1-2）：
// 用户在 composer 输入 `/` 选「先给方案」，发送后一次性消费，下一轮恢复默认。
//
// ## 为什么技能走**隐藏前缀注入**而不是会话能力投影
// 本仓技能是**会话级能力**（物化为 `<root>/<id>/SKILL.md`，进指纹/代际，换技能集要换代际、
// 可能冷启，见 adapter.go `dshSkillProjection`）。而 v4 的「一次性技能」若改会话能力集，就会
// 永久改指纹——那正是 v1「模式」的老问题。ACP 也**没有**「按消息触发某技能」的 API。
// 所以走 `RunPrefix` 隐藏前缀：把技能契约正文拼进**本轮**发给底座的 prompt，不进事件/transcript，
// 下一轮自然不带（一次性由"仅本轮拼"保证，不靠清状态）。这是候选 A（design/plan §P1.4）。
//
// ## 边界
// 技能**不裁剪工具**（没有通道，且 DSH plan mode 也不裁）。它靠文本契约让模型"先探索、出方案、
// 交计划即停"——是**软约束**，与底座 plan mode 同构。要硬只读请配「仅可查看」权限。
// 契约块声明优先级高于专家角色，避免专家说"直接改"、技能说"先计划"时打架。

// skillContractAttr 技能契约块的稳定标识（data-role 取值；测试与诊断靠它认这一段）。
const skillContractAttr = "mate-skill"

// builtinSkill 一条内置技能：id + 展示名 + 一句话 + 注入契约正文。
type builtinSkill struct {
	ID   string
	Name string
	Desc string
	// Contract 注入给模型的契约正文（隐藏前缀用；空 = 不注入正文，纯占位）。
	Contract string
}

// builtinSkills 内置技能目录（唯一事实源：前端 `GET /skills` 与后端白名单共用）。
//
// 顺序即展示顺序。**不做用户自定义技能**（design 定稿 §5-2：本期后端常量，零 DB）。
var builtinSkills = []builtinSkill{
	{
		ID:   "plan-first",
		Name: "先给方案",
		Desc: "先探索并给出可评审的方案，不执行",
		Contract: "你现在处于「先方案后执行」的协作姿态：先探索、理解现状，然后给出一份**可评审的方案**，" +
			"不要直接改动文件或执行有副作用的命令。方案要决策完备：目标与成功判据、分模块改动点、风险与取舍、" +
			"验证方式。方案给出后**就停**，等用户确认或补充；不要自作主张开始执行。本条优先级高于角色设定里的执行倾向。",
	},
	{
		ID:   "weekly-report",
		Name: "周报生成",
		Desc: "把本周会话与任务整理成周报",
		Contract: "你现在处于「周报生成」姿态：把本周的会话与任务整理成一份简洁周报（进展 / 问题 / 下一步），" +
			"不要改动代码或配置，只做归纳整理。",
	},
}

// BuiltinSkill 内置技能的事实视图（供 `GET /skills` 与白名单使用）。
type BuiltinSkill struct {
	ID   string
	Name string
	Desc string
}

// BuiltinSkills 返回内置技能目录（拷贝，调用方改不到内部）。
func BuiltinSkills() []BuiltinSkill {
	out := make([]BuiltinSkill, 0, len(builtinSkills))
	for _, s := range builtinSkills {
		out = append(out, BuiltinSkill{ID: s.ID, Name: s.Name, Desc: s.Desc})
	}
	return out
}

// SkillContract 按技能 id 取注入契约正文；未知 id → 错误（不静默回落：用户的意图不能被猜掉）。
func SkillContract(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	for _, s := range builtinSkills {
		if s.ID == id {
			return skillContractBlock(s.Contract), nil
		}
	}
	return "", fmt.Errorf("unknown skill %q", id)
}

// skillContractBlock 把契约正文包成 `data-role` 标记块（与专家角色块同款防御，便于渲染/诊断）。
func skillContractBlock(contract string) string {
	contract = strings.TrimSpace(contract)
	if contract == "" {
		return ""
	}
	return "<system-reminder data-role=\"" + skillContractAttr + "\">" + contract + "</system-reminder>"
}

// StripSkillPrefix 去掉文本开头的技能契约块（诊断/渲染防御，仿 `StripExpertRolePrefix`）。
func StripSkillPrefix(s string) string {
	open := "<system-reminder data-role=\"" + skillContractAttr + "\">"
	if !strings.HasPrefix(s, open) {
		return s
	}
	rest := s[len(open):]
	if i := strings.Index(rest, "</system-reminder>"); i >= 0 {
		rest = rest[i+len("</system-reminder>"):]
		return strings.TrimLeft(rest, "\n")
	}
	return s
}
