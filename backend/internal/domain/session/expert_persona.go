package session

import "strings"

// ——— P1：专家角色设定（首轮隐藏 user-context）———
//
// 形态借用 WorkBuddy 专家模式的 `Role Override`（依据见
// `.dsh/docs/notes/2026-10-29-workbuddy-rea-v2-notes.md` §6）：专家角色**压过**产品身份，
// 但仍是一段平台拼进首条 prompt 的**隐藏 user-context**，不新增 runtime 能力、不换进程。
//
// 关键边界：
//   - 只**首轮**注入一次（由 `RunPrefix` 的一次性消费保证）；次轮原样发送；
//   - 前缀**确定性**：同一 expert + 同一 instructions 逐字节相同（剥离与测试都依赖它）；
//   - 不落库：它只进发给底座的 prompt 正文，`session.user_message`/transcript/标题/搜索保存的
//     始终是**用户原文**（`runner.Prompt` 在发布事件后才 `Compose`）。

// ExpertRoleAttr 专家角色块的稳定标识（data-role 取值；测试与诊断靠它认这一段）。
const ExpertRoleAttr = `mate-expert-role`

// BuildExpertRoleBlock 构造专家角色块（instructions 为空时返回空串 = 不注入）。
func BuildExpertRoleBlock(expertID, name, instructions string) string {
	if strings.TrimSpace(instructions) == "" {
		return ""
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = expertID
	}
	var b strings.Builder
	b.WriteString(`<system-reminder data-role="`)
	b.WriteString(ExpertRoleAttr)
	b.WriteString(`" data-expert-id="`)
	b.WriteString(expertID)
	b.WriteString("\">\n")
	b.WriteString("Role Override: You are the session-level expert \"")
	b.WriteString(name)
	b.WriteString("\". This role takes precedence over any previously established product persona or identity context for this session.\n")
	b.WriteString("<expert_instructions>\n")
	b.WriteString(instructions)
	b.WriteString("\n</expert_instructions>\n")
	b.WriteString("</system-reminder>")
	return b.String()
}

// StripExpertRolePrefix 从一段文本里剥离**精确前缀** `block`（及其后的空行分隔）。
//
// 返回剥离后的文本与是否剥离成功。它是一层**防御**：正常路径下前缀从不会进入落库文本
// （事件发布的是用户原文），但若某底座把首轮 prompt 原样回显并因此被下游落库，这里能按精确
// 前缀清掉；不匹配时**原样返回**（不静默吞掉可能是用户自己写的内容）。
func StripExpertRolePrefix(text, block string) (string, bool) {
	if block == "" {
		return text, false
	}
	if !strings.HasPrefix(text, block) {
		return text, false
	}
	rest := text[len(block):]
	rest = strings.TrimPrefix(rest, "\n\n")
	return rest, true
}
