package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/morehao/ark-forge/pkg/acl"
)

// 权限确认通道配置（AionUi F-PERM-01：F5.1 一致性）。
const (
	// DefaultPermissionTimeout 挂起权限请求在无用户决策时自动拒绝的时长（对齐 AionUi 30min 超时默认）。
	DefaultPermissionTimeout = 30 * time.Minute
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
	callID         string // 稳定 call_id（决策回传锚点）
	conversationID string
	title          string
	toolCall       acl.ToolCallInfo
	options        []acl.PermissionOption
	ch             chan acl.PermissionDecision
	timeout        time.Time
}

// PermissionManager 管理挂起权限请求：注册 → 发布事件 → 等待决策 → 按 call_id 回馈；
// 并维护会话级 allow/reject 双侧记忆（AutoDecision 快路径）。
// mock 不了，权限决策的 RPC 阻塞在 SDK 派发的 goroutine 里（见 acl/client.go 说明）。
type PermissionManager struct {
	mu         sync.Mutex
	pending    map[string]*pendingPermission     // key = callID
	mem        map[string]map[string]memoryEntry // conversationID → action+identifier → 记忆（allow/reject 同键互斥）
	timeout    time.Duration
	logger     *slog.Logger
	onResolved func(conversationID, callID, reason string, d acl.PermissionDecision)

	// 会话级记忆持久化挂钩（sqlite 模式：写 permission_memories 表；nil = 不持久化）。
	// 所有记忆变更（Remember/Revoke/ForgetConversation）都经此旁路，进程内存与 DB 保持一致。
	memAdd    func(conversationID, title, kind, decision string, since time.Time)
	memDel    func(conversationID, title, kind string)
	memDelAll func(conversationID string)
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
func (m *PermissionManager) SetOnResolved(fn func(conversationID, callID, reason string, d acl.PermissionDecision)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onResolved = fn
}

// genCallID 生成稳定 call_id。
func genCallID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "perm-" + hex.EncodeToString(b)
}

// CancelConversation 取消某会话的全部挂起权限请求（conversation/cancel 后按 ACP 规范回 Cancelled）。
func (m *PermissionManager) CancelConversation(conversationID string) {
	m.mu.Lock()
	var victims []*pendingPermission
	for _, p := range m.pending {
		if p.conversationID == conversationID {
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
func (m *PermissionManager) pendingCount(conversationID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.pending {
		if p.conversationID == conversationID {
			n++
		}
	}
	return n
}

// pendingForConversation 列出某会话的全部挂起权限（按 call_id 稳定序）。
func (m *PermissionManager) pendingForConversation(conversationID string) []*pendingPermission {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*pendingPermission
	for _, p := range m.pending {
		if p.conversationID == conversationID {
			out = append(out, p)
		}
	}
	return out
}

// RegisterAndWait 注册一条权限请求并阻塞等待决策（SDK 派发 goroutine 中调用）。
// publish 副作用（发布 conversation.permission_request 事件）由调用方注入，避免 manager 依赖 bus；
// publisher 携带超时时刻（timeout），供 UI 端判断卡片展示。
// 返回最终决策；超时/上下文取消时自动拒绝（Cancelled）。
func (m *PermissionManager) RegisterAndWait(ctx context.Context, conversationID string, req acl.PermissionRequest, publish func(callID, title string, tool acl.ToolCallInfo, opts []acl.PermissionOption, timeout time.Time)) acl.PermissionDecision {
	callID := genCallID()
	p := &pendingPermission{
		callID:         callID,
		conversationID: conversationID,
		title:          req.ToolCall.Title,
		toolCall:       req.ToolCall,
		options:        req.Options,
		ch:             make(chan acl.PermissionDecision, 1),
		timeout:        time.Now().Add(m.timeout),
	}
	m.mu.Lock()
	m.pending[callID] = p
	m.mu.Unlock()
	m.logger.Info("permission: request registered", "conversation", conversationID, "call_id", callID,
		"title", req.ToolCall.Title, "wire_request_id", req.RequestID)

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
		m.logger.Warn("permission: request timeout, auto-reject", "conversation", conversationID, "call_id", callID, "title", req.ToolCall.Title)
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
			m.Remember(conversationID, req.ToolCall, decision, d.OptionID)
		}
	}
	if onResolved != nil {
		onResolved(conversationID, callID, reason, d)
	}
	m.logger.Info("permission: resolved", "conversation", conversationID, "call_id", callID,
		"reason", reason, "allowed", d.Allowed, "rejected", d.Rejected, "always", d.Always, "option_id", d.OptionID)
	return d
}

// ResolveAllow 允许（选中指定/第一个可选项）。带 always 标记会话级始终允许。
// 按 call_id 直投、不做会话归属校验——REST 决策走 ResolveDecision（带归属校验）。
func (m *PermissionManager) ResolveAllow(callID, optionID string, always bool) bool {
	ok, _ := m.ResolveDecision("", callID, acl.PermissionDecision{Allowed: true, OptionID: optionID, Always: always})
	return ok
}

// ResolveReject 显式拒绝。optionID 为所选 deny 选项（ACP 层会 Selected 回执，agent 可区分拒绝与取消）；
// always=true 时记录会话级「始终拒绝」记忆。
// 按 call_id 直投、不做会话归属校验——REST 决策走 ResolveDecision（带归属校验）。
func (m *PermissionManager) ResolveReject(callID, optionID string, always bool) bool {
	ok, _ := m.ResolveDecision("", callID, acl.PermissionDecision{Rejected: true, OptionID: optionID, Always: always})
	return ok
}

// DecisionOutcome 是一次权限决策的**实际回执**（投递给 agent 的 ACP 结果），供 REST 回执如实描述。
type DecisionOutcome struct {
	// Kind ACP 回执形态：selected | cancelled（acl.OutcomeSelected/OutcomeCancelled）。
	Kind string
	// OptionID 实际选中的 option_id（Kind=cancelled 时为空）。
	OptionID string
}

// ResolveDecision 投递一次决策并返回**实际回执**（按该请求的 options 计算，与 ACP 层同一规则，
// 见 acl.EffectivePermissionOutcome）：请求不存在/已过期时 found=false、回执为零值。
// 这是 REST 回执的事实来源——不再回显请求参数（option_id 可能被兜底为第一个选项或降级 Cancelled）。
//
// conversationID 是决策发起方会话（C3 scope 归属，与 ElicitationManager.resolve 同一立场）：
// 只有登记该请求的会话能决策——跨会话决策（陈旧标签页/串号调用）一律拒绝，既不投递也不删除
// 挂起项，对外统一表现为「不存在」（found=false，不泄露他会话挂起项）。空串 = 跳过归属校验，
// 仅供 ResolveAllow/ResolveReject 这类按 call_id 直投的内部入口使用。
func (m *PermissionManager) ResolveDecision(conversationID, callID string, d acl.PermissionDecision) (found bool, out DecisionOutcome) {
	if callID == "" {
		return false, DecisionOutcome{}
	}
	m.mu.Lock()
	p, ok := m.pending[callID]
	if !ok {
		m.mu.Unlock()
		return false, DecisionOutcome{}
	}
	if conversationID != "" && p.conversationID != conversationID {
		owner := p.conversationID
		m.mu.Unlock()
		m.logger.Warn("permission: 决策会话与归属会话不符，拒绝（scope mismatch）",
			"call_id", callID, "owner", owner, "caller", conversationID)
		return false, DecisionOutcome{}
	}
	kind, oid := acl.EffectivePermissionOutcome(d, p.options)
	// 非阻塞投递（通道容量 1）。若消费者（RegisterAndWait）已退出，静默丢弃。
	select {
	case p.ch <- d:
	default:
	}
	m.mu.Unlock()
	return true, DecisionOutcome{Kind: kind, OptionID: oid}
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
