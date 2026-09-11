package manager

import (
	"sort"
	"time"

	"github.com/morehao/ark-forge/pkg/acl"
)

// 会话级权限记忆的决策侧（与 allow_always / reject_always 选项一一对应）。
const (
	memDecisionAllow  = "allow"  // 会话级「始终允许」
	memDecisionReject = "reject" // 会话级「始终拒绝」
)

// memoryEntry 是一条会话级权限记忆（allow_always / reject_always 双侧对称，同键互斥覆盖）。
type memoryEntry struct {
	title    string
	kind     string
	decision string // memDecisionAllow | memDecisionReject
	optionID string // 用户当初选择的选项（auto-decision 时尽量沿用）
	since    time.Time
}

// MemoryItem 是一条会话级权限记忆的对外表示（REST/详情载荷；decision 区分始终允许/始终拒绝）。
// conversationID 仅用于装载（sqlite 恢复），不进对外载荷。
type MemoryItem struct {
	conversationID string
	Title          string    `json:"title"`
	Kind           string    `json:"kind"`
	Decision       string    `json:"decision"` // allow | reject
	Since          time.Time `json:"since"`
}

// SetMemoryHooks 注入会话级记忆持久化挂钩（sqlite 模式；可为 nil 表示不持久化）。
// 挂钩在锁外调用（DB 写是 best-effort，不入关键路径）。
func (m *PermissionManager) SetMemoryHooks(add func(conversationID, title, kind, decision string, since time.Time),
	del func(conversationID, title, kind string), delAll func(conversationID string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.memAdd = add
	m.memDel = del
	m.memDelAll = delAll
}

// alwaysKeyOf 归一化「action+identifier」集合键：以 tool title + kind 作为可辨识键。
func alwaysKeyOf(title, kind string) string { return title + "|" + kind }

// alwaysKey 由工具信息算集合键。
func alwaysKey(t acl.ToolCallInfo) string { return alwaysKeyOf(t.Title, t.Kind) }

// AutoDecision 决定「无需征询」的快速路径：会话级记忆命中 → 直接放行/拒绝。
// 返回 (decision, ok)：ok=false 表示需要真正挂起等待用户。
func (m *PermissionManager) AutoDecision(conversationID string, req acl.PermissionRequest) (acl.PermissionDecision, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.lookupLocked(conversationID, req.ToolCall)
	if e == nil {
		return acl.PermissionDecision{}, false
	}
	switch e.decision {
	case memDecisionAllow:
		return acl.PermissionDecision{Allowed: true, OptionID: e.optionID}, true
	case memDecisionReject:
		return acl.PermissionDecision{Rejected: true, OptionID: e.optionID}, true
	default:
		return acl.PermissionDecision{}, false
	}
}

// lookupLocked 查某会话对某工具的会话级记忆（须持锁）。
func (m *PermissionManager) lookupLocked(conversationID string, t acl.ToolCallInfo) *memoryEntry {
	set := m.mem[conversationID]
	if set == nil {
		return nil
	}
	e, ok := set[alwaysKey(t)]
	if !ok {
		return nil
	}
	cp := e
	return &cp
}

// Remember 记录（或覆盖）一条会话级权限记忆。decision ∈ allow|reject。
// sqlite 模式下同步旁路持久化（permission_memories 表，同键覆盖）。
func (m *PermissionManager) Remember(conversationID string, t acl.ToolCallInfo, decision, optionID string) {
	m.mu.Lock()
	set := m.mem[conversationID]
	if set == nil {
		set = make(map[string]memoryEntry)
		m.mem[conversationID] = set
	}
	k := alwaysKey(t)
	prev, ok := set[k]
	since := time.Now().UTC()
	if ok && prev.decision == decision {
		since = prev.since // 同侧重复授权不刷新授予时间
	}
	set[k] = memoryEntry{title: t.Title, kind: t.Kind, decision: decision, optionID: optionID, since: since}
	add := m.memAdd
	m.mu.Unlock()
	if add != nil {
		add(conversationID, t.Title, t.Kind, decision, since)
	}
}

// LoadMemories 恢复会话级权限记忆（sqlite 启动恢复：从 permission_memories 表装载，
// 双侧 allow/reject 一并重建；重复装载按 since 覆盖，行为与 Remember 一致）。
func (m *PermissionManager) LoadMemories(items []MemoryItem) {
	for _, it := range items {
		m.memPut(it)
	}
}

// memPut 装载单条记忆（给定 since，复用同键覆盖语义）。
func (m *PermissionManager) memPut(it MemoryItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.mem[it.conversationID]
	if set == nil {
		set = make(map[string]memoryEntry)
		m.mem[it.conversationID] = set
	}
	k := alwaysKeyOf(it.Title, it.Kind)
	since := it.Since
	if since.IsZero() {
		since = time.Now().UTC()
	}
	set[k] = memoryEntry{title: it.Title, kind: it.Kind, decision: it.Decision, since: since}
}

// ForgetConversation 清除整会话的权限记忆（会话结束/重置/一键撤销）。
// sqlite 模式下同步旁路删除（permission_memories 表整会话）。
func (m *PermissionManager) ForgetSession(conversationID string) {
	m.mu.Lock()
	delete(m.mem, conversationID)
	delAll := m.memDelAll
	m.mu.Unlock()
	if delAll != nil {
		delAll(conversationID)
	}
}

// Revoke 撤销一条会话级权限记忆（title+kind 精确匹配，不区分 decision）；返回该条是否存在。
// sqlite 模式下同步旁路删除（permission_memories 表同键）。
func (m *PermissionManager) Revoke(conversationID, title, kind string) bool {
	m.mu.Lock()
	set := m.mem[conversationID]
	if set == nil {
		m.mu.Unlock()
		return false
	}
	k := alwaysKeyOf(title, kind)
	if _, ok := set[k]; !ok {
		m.mu.Unlock()
		return false
	}
	delete(set, k)
	if len(set) == 0 {
		delete(m.mem, conversationID)
	}
	del := m.memDel
	m.mu.Unlock()
	if del != nil {
		del(conversationID, title, kind)
	}
	return true
}

// List 列出某会话的全部权限记忆（按授予时间升序；decision 区分 allow/reject）。
func (m *PermissionManager) List(conversationID string) []MemoryItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.mem[conversationID]
	out := make([]MemoryItem, 0, len(set))
	for _, e := range set {
		out = append(out, MemoryItem{Title: e.title, Kind: e.kind, Decision: e.decision, Since: e.since})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}
