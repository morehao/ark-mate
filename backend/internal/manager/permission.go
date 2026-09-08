package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
)

// 权限确认通道配置（AionUi F-PERM-01：F5.1 一致性）。
const (
	// DefaultPermissionTimeout 挂起权限请求在无用户决策时自动拒绝的时长（对齐 AionUi 30min 超时默认）。
	DefaultPermissionTimeout = 30 * time.Minute
)

// 会话级权限记忆的决策侧（与 allow_always / reject_always 选项一一对应）。
const (
	memDecisionAllow  = "allow"  // 会话级「始终允许」
	memDecisionReject = "reject" // 会话级「始终拒绝」
)

// permissionRequestKind 对应 acl.PermissionOption.Kind 的可读判别（透传）。
const (
	permKindAllowOnce    = "allow_once"
	permKindAllowAlways  = "allow_always"
	permKindRejectOnce   = "reject_once"
	permKindRejectAlways = "reject_always"
)

// pendingPermission 是一条挂起中的权限请求：持有一个「待用户决策」的通道与超时。
type pendingPermission struct {
	callID    string // 稳定 call_id（决策回传锚点）
	sessionID string
	title     string
	toolCall  acl.ToolCallInfo
	options   []acl.PermissionOption
	ch        chan acl.PermissionDecision
	timeout   time.Time
}

// memoryEntry 是一条会话级权限记忆（allow_always / reject_always 双侧对称，同键互斥覆盖）。
type memoryEntry struct {
	title    string
	kind     string
	decision string // memDecisionAllow | memDecisionReject
	optionID string // 用户当初选择的选项（auto-decision 时尽量沿用）
	since    time.Time
}

// MemoryItem 是一条会话级权限记忆的对外表示（REST/详情载荷；decision 区分始终允许/始终拒绝）。
// sessionID 仅用于装载（sqlite 恢复），不进对外载荷。
type MemoryItem struct {
	sessionID string
	Title     string    `json:"title"`
	Kind      string    `json:"kind"`
	Decision  string    `json:"decision"` // allow | reject
	Since     time.Time `json:"since"`
}

// PermissionManager 管理挂起权限请求：注册 → 发布事件 → 等待决策 → 按 call_id 回馈；
// 并维护会话级 allow/reject 双侧记忆（AutoDecision 快路径）。
// mock 不了，权限决策的 RPC 阻塞在 SDK 派发的 goroutine 里（见 acl/client.go 说明）。
type PermissionManager struct {
	mu         sync.Mutex
	pending    map[string]*pendingPermission     // key = callID
	mem        map[string]map[string]memoryEntry // sessionID → action+identifier → 记忆（allow/reject 同键互斥）
	timeout    time.Duration
	logger     *slog.Logger
	onResolved func(sessionID, callID, reason string, d acl.PermissionDecision)

	// 会话级记忆持久化挂钩（sqlite 模式：写 permission_memories 表；nil = 不持久化）。
	// 所有记忆变更（Remember/Revoke/ForgetSession）都经此旁路，进程内存与 DB 保持一致。
	memAdd    func(sessionID, title, kind, decision string, since time.Time)
	memDel    func(sessionID, title, kind string)
	memDelAll func(sessionID string)
}

// NewPermissionManager 构造权限管理器。
func NewPermissionManager(timeout time.Duration) *PermissionManager {
	if timeout <= 0 {
		timeout = DefaultPermissionTimeout
	}
	return &PermissionManager{
		pending: make(map[string]*pendingPermission),
		mem:     make(map[string]map[string]memoryEntry),
		timeout: timeout,
		logger:  slog.Default(),
	}
}

// SetLogger 设置日志。
func (m *PermissionManager) SetLogger(l *slog.Logger) {
	if l != nil {
		m.logger = l
	}
}

// SetTimeout 覆盖挂起超时（0 → 用默认）。
func (m *PermissionManager) SetTimeout(d time.Duration) {
	if d > 0 {
		m.timeout = d
	}
}

// SetOnResolved 注入「挂起请求已决」回调（user/timeout/cancel 全路径，供发布收敛事件；可为 nil）。
func (m *PermissionManager) SetOnResolved(fn func(sessionID, callID, reason string, d acl.PermissionDecision)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onResolved = fn
}

// SetMemoryHooks 注入会话级记忆持久化挂钩（sqlite 模式；可为 nil 表示不持久化）。
// 挂钩在锁外调用（DB 写是 best-effort，不入关键路径）。
func (m *PermissionManager) SetMemoryHooks(add func(sessionID, title, kind, decision string, since time.Time),
	del func(sessionID, title, kind string), delAll func(sessionID string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.memAdd = add
	m.memDel = del
	m.memDelAll = delAll
}

// genCallID 生成稳定 call_id。
func genCallID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "perm-" + hex.EncodeToString(b)
}

// alwaysKeyOf 归一化「action+identifier」集合键：以 tool title + kind 作为可辨识键。
func alwaysKeyOf(title, kind string) string { return title + "|" + kind }

// alwaysKey 由工具信息算集合键。
func alwaysKey(t acl.ToolCallInfo) string { return alwaysKeyOf(t.Title, t.Kind) }

// AutoDecision 决定「无需征询」的快速路径：会话级记忆命中 → 直接放行/拒绝。
// 返回 (decision, ok)：ok=false 表示需要真正挂起等待用户。
func (m *PermissionManager) AutoDecision(sessionID string, req acl.PermissionRequest) (acl.PermissionDecision, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.lookupLocked(sessionID, req.ToolCall)
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
func (m *PermissionManager) lookupLocked(sessionID string, t acl.ToolCallInfo) *memoryEntry {
	set := m.mem[sessionID]
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
func (m *PermissionManager) Remember(sessionID string, t acl.ToolCallInfo, decision, optionID string) {
	m.mu.Lock()
	set := m.mem[sessionID]
	if set == nil {
		set = make(map[string]memoryEntry)
		m.mem[sessionID] = set
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
		add(sessionID, t.Title, t.Kind, decision, since)
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
	set := m.mem[it.sessionID]
	if set == nil {
		set = make(map[string]memoryEntry)
		m.mem[it.sessionID] = set
	}
	k := alwaysKeyOf(it.Title, it.Kind)
	since := it.Since
	if since.IsZero() {
		since = time.Now().UTC()
	}
	set[k] = memoryEntry{title: it.Title, kind: it.Kind, decision: it.Decision, since: since}
}

// ForgetSession 清除整会话的权限记忆（会话结束/重置/一键撤销）。
// sqlite 模式下同步旁路删除（permission_memories 表整会话）。
func (m *PermissionManager) ForgetSession(sessionID string) {
	m.mu.Lock()
	delete(m.mem, sessionID)
	delAll := m.memDelAll
	m.mu.Unlock()
	if delAll != nil {
		delAll(sessionID)
	}
}

// Revoke 撤销一条会话级权限记忆（title+kind 精确匹配，不区分 decision）；返回该条是否存在。
// sqlite 模式下同步旁路删除（permission_memories 表同键）。
func (m *PermissionManager) Revoke(sessionID, title, kind string) bool {
	m.mu.Lock()
	set := m.mem[sessionID]
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
		delete(m.mem, sessionID)
	}
	del := m.memDel
	m.mu.Unlock()
	if del != nil {
		del(sessionID, title, kind)
	}
	return true
}

// List 列出某会话的全部权限记忆（按授予时间升序；decision 区分 allow/reject）。
func (m *PermissionManager) List(sessionID string) []MemoryItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.mem[sessionID]
	out := make([]MemoryItem, 0, len(set))
	for _, e := range set {
		out = append(out, MemoryItem{Title: e.title, Kind: e.kind, Decision: e.decision, Since: e.since})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// CancelSession 取消某会话的全部挂起权限请求（session/cancel 后按 ACP 规范回 Cancelled）。
func (m *PermissionManager) CancelSession(sessionID string) {
	m.mu.Lock()
	var victims []*pendingPermission
	for _, p := range m.pending {
		if p.sessionID == sessionID {
			victims = append(victims, p)
			delete(m.pending, p.callID) // 立即不再挂起；消费方后续 delete 幂等
		}
	}
	m.mu.Unlock()
	for _, p := range victims {
		// 直接向持有 channel 投递（此时条目已移出 map，不能走按 call_id 的 resolve）。
		select {
		case p.ch <- acl.PermissionDecision{}: // 取消 → Cancelled
		default:
		}
	}
}

// pendingCount 返回某会话的挂起权限请求数（侧栏/聊天头角标）。
func (m *PermissionManager) pendingCount(sessionID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.pending {
		if p.sessionID == sessionID {
			n++
		}
	}
	return n
}

// pendingForSession 列出某会话的全部挂起权限（按 call_id 稳定序）。
func (m *PermissionManager) pendingForSession(sessionID string) []*pendingPermission {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*pendingPermission
	for _, p := range m.pending {
		if p.sessionID == sessionID {
			out = append(out, p)
		}
	}
	return out
}

// RegisterAndWait 注册一条权限请求并阻塞等待决策（SDK 派发 goroutine 中调用）。
// publish 副作用（发布 session.permission_request 事件）由调用方注入，避免 manager 依赖 bus；
// publisher 携带超时时刻（timeout），供 UI 端判断卡片展示。
// 返回最终决策；超时/上下文取消时自动拒绝（Cancelled）。
func (m *PermissionManager) RegisterAndWait(ctx context.Context, sessionID string, req acl.PermissionRequest, publish func(callID, title string, tool acl.ToolCallInfo, opts []acl.PermissionOption, timeout time.Time)) acl.PermissionDecision {
	callID := genCallID()
	p := &pendingPermission{
		callID:    callID,
		sessionID: sessionID,
		title:     req.ToolCall.Title,
		toolCall:  req.ToolCall,
		options:   req.Options,
		ch:        make(chan acl.PermissionDecision, 1),
		timeout:   time.Now().Add(m.timeout),
	}
	m.mu.Lock()
	m.pending[callID] = p
	m.mu.Unlock()
	m.logger.Info("permission: request registered", "session", sessionID, "call_id", callID, "title", req.ToolCall.Title)

	if publish != nil {
		publish(callID, req.ToolCall.Title, req.ToolCall, req.Options, p.timeout)
	}

	timer := time.NewTimer(m.timeout)
	defer timer.Stop()

	var d acl.PermissionDecision
	reason := "user"
	select {
	case d = <-p.ch:
		// 用户已决策（allow/reject × once/always）
	case <-ctx.Done():
		d = acl.PermissionDecision{} // 取消 → Cancelled
		reason = "cancel"
	case <-timer.C:
		m.logger.Warn("permission: request timeout, auto-reject", "session", sessionID, "call_id", callID, "title", req.ToolCall.Title)
		d = acl.PermissionDecision{}
		reason = "timeout"
	}
	m.mu.Lock()
	delete(m.pending, callID)
	onResolved := m.onResolved
	m.mu.Unlock()

	// 决策为 allow_always / reject_always（或用户明示 always）：记录会话级记忆（双侧对称，同键互斥覆盖）。
	if d.Always {
		decision := ""
		switch {
		case d.Allowed:
			decision = memDecisionAllow
		case d.Rejected:
			decision = memDecisionReject
		}
		if decision != "" {
			m.Remember(sessionID, req.ToolCall, decision, d.OptionID)
		}
	}
	if onResolved != nil {
		onResolved(sessionID, callID, reason, d)
	}
	m.logger.Info("permission: resolved", "session", sessionID, "call_id", callID,
		"reason", reason, "allowed", d.Allowed, "rejected", d.Rejected, "always", d.Always, "option_id", d.OptionID)
	return d
}

// resolve 按 call_id 投递一次决策；返回是否存在该挂起请求。
func (m *PermissionManager) resolve(callID string, d acl.PermissionDecision) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[callID]
	if !ok {
		return false
	}
	// 非阻塞投递（通道容量 1）。若消费者（RegisterAndWait）已退出，静默丢弃。
	select {
	case p.ch <- d:
	default:
	}
	return true
}

// ResolveAllow 允许（选中指定/第一个可选项）。带 always 标记会话级始终允许。
func (m *PermissionManager) ResolveAllow(callID, optionID string, always bool) bool {
	return m.resolve(callID, acl.PermissionDecision{Allowed: true, OptionID: optionID, Always: always})
}

// ResolveReject 显式拒绝。optionID 为所选 deny 选项（ACP 层会 Selected 回执，agent 可区分拒绝与取消）；
// always=true 时记录会话级「始终拒绝」记忆。
func (m *PermissionManager) ResolveReject(callID, optionID string, always bool) bool {
	return m.resolve(callID, acl.PermissionDecision{Rejected: true, OptionID: optionID, Always: always})
}

// PermissionItem 是一条挂起权限请求的对外表示（REST/WS 载荷；options 为 ACP 可读子集）。
type PermissionItem struct {
	CallID    string                 `json:"call_id"`
	SessionID string                 `json:"session_id"`
	Title     string                 `json:"title"`
	ToolCall  acl.ToolCallInfo       `json:"tool_call"`
	Options   []acl.PermissionOption `json:"options"`
	TimeoutAt time.Time              `json:"timeout_at"`
}
