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

// PermissionManager 管理挂起权限请求：注册 → 发布事件 → 等待决策 → 按 call_id 回馈。
// mock 不了，权限决策的 RPC 阻塞在 SDK 派发的 goroutine 里（见 acl/client.go 说明）。
type PermissionManager struct {
	mu       sync.Mutex
	pending  map[string]*pendingPermission     // key = callID
	alwaysOK map[string]map[string]alwaysEntry // sessionID → action+identifier 始终允许记忆
	timeout  time.Duration
	logger   *slog.Logger
}

// alwaysEntry 是一条会话级「始终允许」记忆（决策 v4① allow_always / reject_always 的 allow 侧）。
type alwaysEntry struct {
	title string
	kind  string
	since time.Time
}

// AlwaysAllowItem 是「始终允许」记忆的对外表示（REST/详情载荷）。
type AlwaysAllowItem struct {
	Title string    `json:"title"`
	Kind  string    `json:"kind"`
	Since time.Time `json:"since"`
}

// NewPermissionManager 构造权限管理器。
func NewPermissionManager(timeout time.Duration) *PermissionManager {
	if timeout <= 0 {
		timeout = DefaultPermissionTimeout
	}
	return &PermissionManager{
		pending:  make(map[string]*pendingPermission),
		alwaysOK: make(map[string]map[string]alwaysEntry),
		timeout:  timeout,
		logger:   slog.Default(),
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

// AutoDecision 决定「无需征询」的快速路径：会话级始终允许记忆命中 → 直接放行。
// 返回 (decision, ok)：ok=false 表示需要真正挂起等待用户。
func (m *PermissionManager) AutoDecision(sessionID string, req acl.PermissionRequest) (acl.PermissionDecision, bool) {
	if sessionID != "" && m.isAlwaysAllowed(sessionID, req.ToolCall) {
		return acl.PermissionDecision{Allowed: true}, true
	}
	return acl.PermissionDecision{}, false
}

// isAlwaysAllowed 判断该会话是否已对某 action+identifier 授过「始终允许」。
func (m *PermissionManager) isAlwaysAllowed(sessionID string, t acl.ToolCallInfo) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.alwaysOK[sessionID]
	if set == nil {
		return false
	}
	_, ok := set[alwaysKey(t)]
	return ok
}

// Remember 记录「始终允许」到会话级记忆。
func (m *PermissionManager) Remember(sessionID string, t acl.ToolCallInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.alwaysOK[sessionID]
	if set == nil {
		set = make(map[string]alwaysEntry)
		m.alwaysOK[sessionID] = set
	}
	k := alwaysKey(t)
	if _, ok := set[k]; !ok {
		set[k] = alwaysEntry{title: t.Title, kind: t.Kind, since: time.Now().UTC()}
	}
}

// ForgetSession 清除整会话的始终允许记忆（会话结束/重置/一键撤销）。
func (m *PermissionManager) ForgetSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.alwaysOK, sessionID)
}

// RevokeAlways 撤销一条会话级「始终允许」记忆；返回该条是否存在。
func (m *PermissionManager) RevokeAlways(sessionID, title, kind string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.alwaysOK[sessionID]
	if set == nil {
		return false
	}
	k := alwaysKeyOf(title, kind)
	if _, ok := set[k]; !ok {
		return false
	}
	delete(set, k)
	if len(set) == 0 {
		delete(m.alwaysOK, sessionID)
	}
	return true
}

// AlwaysAllowed 列出某会话的全部「始终允许」记忆（按授予时间升序）。
func (m *PermissionManager) AlwaysAllowed(sessionID string) []AlwaysAllowItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.alwaysOK[sessionID]
	out := make([]AlwaysAllowItem, 0, len(set))
	for _, e := range set {
		out = append(out, AlwaysAllowItem{Title: e.title, Kind: e.kind, Since: e.since})
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
		m.resolve(p.callID, acl.PermissionDecision{}) // 拒绝/取消
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
// publisher 携带超时时刻（timeout），供 UI 端到前端判断卡片过期收敛。
// 返回最终决策；超时/上下文取消时自动拒绝。
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
	select {
	case d = <-p.ch:
		// 用户已决策
	case <-ctx.Done():
		d = acl.PermissionDecision{} // 取消 → 拒绝
	case <-timer.C:
		m.logger.Warn("permission: request timeout, auto-reject", "session", sessionID, "call_id", callID, "title", req.ToolCall.Title)
		d = acl.PermissionDecision{}
	}
	m.mu.Lock()
	delete(m.pending, callID)
	m.mu.Unlock()

	// 决策为 allow_always / reject_always（或用户明示 always）：记录会话级记忆。
	if d.Allowed && d.Always {
		m.Remember(sessionID, req.ToolCall)
	}
	m.logger.Info("permission: resolved", "session", sessionID, "call_id", callID,
		"allowed", d.Allowed, "always", d.Always, "option_id", d.OptionID)
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

// ResolveReject 拒绝。
func (m *PermissionManager) ResolveReject(callID string) bool {
	return m.resolve(callID, acl.PermissionDecision{})
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
