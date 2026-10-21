package session

import (
	"sync"

	"github.com/morehao/ark-mate/internal/infra/protocol/acp"
)

// ——— 工具调用详情的短期记忆（P4a 正文第六十一刀：从 `internal/manager/tool_calls.go` 搬入）———
//
// 为什么需要它（2026-10-17 从底座源码 `packages/acp/acp/src/index.ts` 确认）：
// dsh 的 `session/request_permission` 只发 `{toolCallId}`，**title/kind/rawInput 全空**；
// 命令行在同 id 的 `session/tool_call` 更新里（`rawInput: {"command": "...", ...}`）。
// 权限请求到达时按 id 回查，命令门禁才拿得到"这条 bash 到底要跑什么"。
//
// 生命周期：`tool_call` 写入；`tool_call_update` 带 input 时刷新；终态（completed/failed）
// 即删除。上限 `MaxRememberedToolCalls`，超出按插入序淘汰——它是**短期**记忆，不是审计日志
// （`events` 表才是审计）。
//
// 归属：按 toolCallId 存活、不按轮次，所以在会话域里由 `Runner` 的事件出口写入、由权限面回查；
// 跨轮次共享同一个实例（换句柄/代际迁移不重置它——底层进程换了，同一次工具调用的上下文还在
// 权限请求里）。

// MaxRememberedToolCalls 上限（单进程）。256 足够覆盖"同会话并发工具 + 权限等待"的窗口；
// 不设上限的后果是长跑进程里这个 map 只增不减（工具调用每秒都在发生）。
const MaxRememberedToolCalls = 256

// ToolMemory 工具调用详情的短期记忆：toolCallId → acp.ToolCallInfo。
type ToolCallMemory struct {
	mu    sync.Mutex
	calls map[string]acp.ToolCallInfo
	order []string
}

// NewToolMemory 造一份空的工具调用记忆。
func NewToolCallMemory() *ToolCallMemory {
	return &ToolCallMemory{calls: make(map[string]acp.ToolCallInfo)}
}

// Remember 记住一次工具调用（空 id 忽略）。
func (m *ToolCallMemory) Remember(info acp.ToolCallInfo) {
	if info.ID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls == nil {
		m.calls = make(map[string]acp.ToolCallInfo)
	}
	if _, seen := m.calls[info.ID]; !seen {
		m.order = append(m.order, info.ID)
	}
	// 只覆盖**非空**字段：`tool_call_update` 常常只带 status，拿它整条覆盖会把命令行抹掉。
	prev := m.calls[info.ID]
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
	m.calls[info.ID] = merged
	// 淘汰最旧（order 里可能有已被 Forget 的 id，循环到找到一个还在的为止）。
	for len(m.order) > MaxRememberedToolCalls {
		old := m.order[0]
		m.order = m.order[1:]
		if _, ok := m.calls[old]; ok {
			delete(m.calls, old)
			break
		}
	}
}

// Get 按 id 回查工具调用详情（不存在 → false）。
func (m *ToolCallMemory) Get(id string) (acp.ToolCallInfo, bool) {
	if id == "" {
		return acp.ToolCallInfo{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.calls[id]
	return info, ok
}

// Forget 删除一条工具调用记忆（终态或会话收尾）。
func (m *ToolCallMemory) Forget(id string) {
	if id == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.calls, id)
}

// Len 当前记住的条数（诊断/测试用）。
func (m *ToolCallMemory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}
