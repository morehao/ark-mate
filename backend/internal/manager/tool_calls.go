package manager

import "github.com/morehao/ark-mate/pkg/acl"

// 工具调用详情的**短期记忆**：toolCallId → acl.ToolCallInfo。
//
// 为什么需要它（2026-10-17 从底座源码 `packages/acp/acp/src/index.ts` 确认）：
// dsh 的 `session/request_permission` 只发 `{toolCallId}`，**title/kind/rawInput 全空**；
// 命令行在同 id 的 `session/tool_call` 更新里（`rawInput: {"command": "...", ...}`）。
// 权限请求到达时按 id 回查，命令门禁才拿得到"这条 bash 到底要跑什么"。
//
// 生命周期：`tool_call` 写入；`tool_call_update` 带 input 时刷新；终态（completed/failed）
// 即删除。上限 `maxRememberedToolCalls`，超出按插入序淘汰——它是**短期**记忆，不是审计日志
// （`events` 表才是审计）。
//
// 归属：它按 toolCallId 存活、不按轮次，所以留在编排侧，经 `ToolMemory` 端口供会话域的事件
// 出口写入（P4a 第五十五刀把事件出口搬进 `domain/session` 时只搬了消费方）。

// maxRememberedToolCalls 上限（单进程）。256 足够覆盖"同会话并发工具 + 权限等待"的窗口；
// 不设上限的后果是长跑进程里这个 map 只增不减（工具调用每秒都在发生）。
const maxRememberedToolCalls = 256

// rememberToolCall 记住一次工具调用（空 id 忽略）。
func (o *Orchestrator) rememberToolCall(info acl.ToolCallInfo) {
	if info.ID == "" {
		return
	}
	o.toolMu.Lock()
	defer o.toolMu.Unlock()
	if o.toolCalls == nil {
		o.toolCalls = make(map[string]acl.ToolCallInfo)
	}
	if _, seen := o.toolCalls[info.ID]; !seen {
		o.toolOrder = append(o.toolOrder, info.ID)
	}
	// 只覆盖**非空**字段：`tool_call_update` 常常只带 status，拿它整条覆盖会把命令行抹掉。
	prev := o.toolCalls[info.ID]
	merged := prev
	if info.Title != "" {
		merged.Title = info.Title
	}
	if info.Kind != "" {
		merged.Kind = info.Kind
	}
	if info.Status != "" {
		merged.Status = info.Status
	}
	if info.Input != nil {
		merged.Input = info.Input
	}
	if info.Output != nil {
		merged.Output = info.Output
	}
	if info.ContentText != "" {
		merged.ContentText = info.ContentText
	}
	merged.ID = info.ID
	o.toolCalls[info.ID] = merged
	// 淘汰最旧（toolOrder 里可能有已被 forget 的 id，循环到找到一个还在的为止）。
	for len(o.toolOrder) > maxRememberedToolCalls {
		old := o.toolOrder[0]
		o.toolOrder = o.toolOrder[1:]
		if _, ok := o.toolCalls[old]; ok {
			delete(o.toolCalls, old)
			break
		}
	}
}

// toolCallFor 按 id 回查工具调用详情（不存在 → false）。
func (o *Orchestrator) toolCallFor(id string) (acl.ToolCallInfo, bool) {
	if id == "" {
		return acl.ToolCallInfo{}, false
	}
	o.toolMu.Lock()
	defer o.toolMu.Unlock()
	info, ok := o.toolCalls[id]
	return info, ok
}

// forgetToolCall 删除一条工具调用记忆（终态或会话收尾）。
func (o *Orchestrator) forgetToolCall(id string) {
	if id == "" {
		return
	}
	o.toolMu.Lock()
	defer o.toolMu.Unlock()
	delete(o.toolCalls, id)
}
