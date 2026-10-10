package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/golib/glog"
)

// 结构化提问通道（elicitation/*）配置。与权限通道同构：注册挂起 → 发事件 → 等待决策 → 回馈。
const (
	// DefaultElicitationTimeout 挂起提问在无用户决策时自动取消的时长（对齐权限通道 30min 默认；
	// UI 按 timeout_at 提前收敛卡片，与权限卡一致）。
	DefaultElicitationTimeout = 30 * time.Minute
)

// elicitationResolution 是投递给 RegisterAndWait 的一次终局：决策结果 + 终局原因。
// 原因取值 user | timeout | cancel | complete，随 session.elicitation_resolved 如实上报，
// 前端据此区分「用户点了接受」与「agent 告知 URL 流程已完成」。
type elicitationResolution struct {
	outcome acl.ElicitationOutcome
	reason  string
}

// pendingElicitation 是一条挂起中的结构化提问：持有一个「待用户决策」的通道与超时。
type pendingElicitation struct {
	id             string // 稳定 id（决策回传锚点）
	conversationID string // 归属会话（scope；C3：决策必须由该会话发起，见 resolve）
	scopeSource    string // 归属来源：wire（`_meta`）| inferred_inflight（宿主推断）| ambiguous（不归属）
	mode           string // form | url
	message        string
	schema         map[string]any // form 模式的 JSON Schema 对象（对外载荷）
	url            string         // url 模式
	urlID          string         // url 模式的 ACP elicitationId（elicitation/complete 的反查锚点）
	ch             chan elicitationResolution
	timeout        time.Time
}

// ElicitationManager 管理挂起结构化提问：注册 → 发布事件 → 等待决策 → 按 id 回馈。
// 与 PermissionManager 同构（无「始终允许」类记忆：每次提问独立征询）。
type ElicitationManager struct {
	mu         sync.Mutex
	pending    map[string]*pendingElicitation
	timeout    time.Duration
	onResolved func(conversationID, id, reason string, d acl.ElicitationOutcome)
}

// NewElicitationManager 构造提问管理器。
func NewElicitationManager(timeout time.Duration) *ElicitationManager {
	if timeout <= 0 {
		timeout = DefaultElicitationTimeout
	}
	return &ElicitationManager{
		pending: make(map[string]*pendingElicitation),
		timeout: timeout,
	}
}

// SetTimeout 覆盖挂起超时（0 → 用默认）。
func (m *ElicitationManager) SetTimeout(d time.Duration) {
	if d > 0 {
		m.timeout = d
	}
}

// SetOnResolved 注入「挂起提问已决」回调（user/timeout/cancel 全路径，供发布收敛事件；可为 nil）。
func (m *ElicitationManager) SetOnResolved(fn func(conversationID, id, reason string, d acl.ElicitationOutcome)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onResolved = fn
}

// genElicitationID 生成稳定 id。
func genElicitationID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "el-" + hex.EncodeToString(b)
}

// schemaMap 把 acl.ElicitationSchema 折叠为对外 JSON Schema 对象（bus/REST 载荷）。
func schemaMap(s *acl.ElicitationSchema) map[string]any {
	if s == nil {
		return nil
	}
	out := map[string]any{"type": "object"}
	if s.Title != "" {
		out["title"] = s.Title
	}
	if s.Description != "" {
		out["description"] = s.Description
	}
	out["properties"] = s.Properties
	if len(s.Required) > 0 {
		out["required"] = s.Required
	}
	return out
}

// CancelConversation 取消某会话的全部挂起提问（conversation/cancel 后按 Cancelled 回馈 agent）。
func (m *ElicitationManager) CancelConversation(conversationID string) {
	m.mu.Lock()
	var victims []*pendingElicitation
	for _, p := range m.pending {
		if p.conversationID == conversationID {
			victims = append(victims, p)
			delete(m.pending, p.id) // 立即不再挂起；消费方后续 delete 幂等
		}
	}
	m.mu.Unlock()
	for _, p := range victims {
		// 直接向持有 channel 投递（此时条目已移出 map，不能走按 id 的 resolve）。
		select {
		case p.ch <- elicitationResolution{outcome: acl.ElicitationOutcome{Action: "cancel"}, reason: reasonCancel}:
		default:
		}
	}
}

// pendingCount 返回某会话的挂起提问数（代际迁移的忙碌探针用；与权限侧同名同义）。
func (m *ElicitationManager) pendingCount(conversationID string) int {
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

// pendingForConversation 列出某会话的全部挂起提问（按 id 稳定序）。
func (m *ElicitationManager) pendingForConversation(conversationID string) []*pendingElicitation {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*pendingElicitation
	for _, p := range m.pending {
		if p.conversationID == conversationID {
			out = append(out, p)
		}
	}
	return out
}

// RegisterAndWait 注册一条结构化提问并阻塞等待决策（SDK 派发 goroutine 中调用）。
// publish 副作用（发布 conversation.elicitation_request 事件）由调用方注入，避免 manager 依赖 bus。
// 返回最终决策；超时/上下文取消时自动按取消处理。
func (m *ElicitationManager) RegisterAndWait(ctx context.Context, conversationID string, req acl.ElicitationRequest, publish func(ElicitationItem)) acl.ElicitationOutcome {
	id := genElicitationID()
	scopeSource := req.ScopeSource
	if scopeSource == "" {
		scopeSource = acl.ScopeInferred
	}
	p := &pendingElicitation{
		id:             id,
		conversationID: conversationID,
		scopeSource:    scopeSource,
		mode:           req.Mode,
		message:        req.Message,
		schema:         schemaMap(req.Schema),
		url:            req.URL,
		urlID:          req.URLID,
		ch:             make(chan elicitationResolution, 1),
		timeout:        time.Now().Add(m.timeout),
	}
	m.mu.Lock()
	m.pending[id] = p
	m.mu.Unlock()
	glog.Infow(ctx, "elicitation: request registered", "conversation", conversationID, "id", id,
		"mode", req.Mode, "scope_source", scopeSource)

	if publish != nil {
		publish(ElicitationItem{
			ID:             id,
			SessionID:      p.conversationID,
			ConversationID: p.conversationID,
			ScopeSource:    p.scopeSource,
			Mode:           p.mode,
			Message:        p.message,
			Schema:         p.schema,
			URL:            p.url,
			TimeoutAt:      p.timeout,
		})
	}

	timer := time.NewTimer(m.timeout)
	defer timer.Stop()

	var d acl.ElicitationOutcome
	reason := reasonUser
	select {
	case r := <-p.ch:
		// 已决策：结果与终局原因（user / complete）由投递方一并给出
		d, reason = r.outcome, r.reason
	case <-ctx.Done():
		d = acl.ElicitationOutcome{Action: "cancel"} // 取消
		reason = reasonCancel
	case <-timer.C:
		glog.Warnw(ctx, "elicitation: request timeout, auto-cancel", "conversation", conversationID, "id", id)
		d = acl.ElicitationOutcome{Action: "cancel"}
		reason = reasonTimeout
	}
	m.mu.Lock()
	delete(m.pending, id)
	onResolved := m.onResolved
	m.mu.Unlock()
	if onResolved != nil {
		onResolved(conversationID, id, reason, d)
	}
	glog.Infow(ctx, "elicitation: resolved", "conversation", conversationID, "id", id, "action", d.Action, "reason", reason)
	return d
}

// resolve 按 id 投递一次终局（决策 + 终局原因），并校验**归属会话**（C3 scope 归属）：
// 只有登记该提问的会话才能决策——跨会话决策（陈旧标签页/串号调用）一律拒绝，
// 既不投递也不删除挂起项，调用方拿到 mismatch=true（对外统一表现为「不存在」）。
func (m *ElicitationManager) resolve(conversationID, id, reason string, d acl.ElicitationOutcome) (found, mismatch bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[id]
	if !ok {
		return false, false
	}
	if conversationID != "" && p.conversationID != conversationID {
		glog.Warnw(context.Background(), "elicitation: 决策会话与归属会话不符，拒绝（scope mismatch）",
			"id", id, "owner", p.conversationID, "caller", conversationID)
		return true, true
	}
	select {
	case p.ch <- elicitationResolution{outcome: d, reason: reason}:
	default:
	}
	return true, false
}

// ResolveAccept 接受（可携带用户提交的 content）；conversationID 为决策发起方会话（scope 校验）。
func (m *ElicitationManager) ResolveAccept(conversationID, id string, content map[string]any) (found, mismatch bool) {
	return m.resolve(conversationID, id, reasonUser, acl.ElicitationOutcome{Action: "accept", Content: content})
}

// ResolveDecline 用户拒绝提供信息。
func (m *ElicitationManager) ResolveDecline(conversationID, id string) (found, mismatch bool) {
	return m.resolve(conversationID, id, reasonUser, acl.ElicitationOutcome{Action: "decline"})
}

// ResolveCancel 取消本次提问。
func (m *ElicitationManager) ResolveCancel(conversationID, id string) (found, mismatch bool) {
	return m.resolve(conversationID, id, reasonUser, acl.ElicitationOutcome{Action: "cancel"})
}

// 终局原因常量（随 session.elicitation_resolved 上报，前端据此区分终局来源）。
const (
	// reasonUser 用户（或 REST/WS 决策方）给出的终局。
	reasonUser = "user"
	// reasonTimeout 无用户决策，挂起超时自动取消。
	reasonTimeout = "timeout"
	// reasonCancel 上下文取消（会话取消/连接拆除）。
	reasonCancel = "cancel"
	// reasonComplete agent 告知 URL 模式提问已在 agent 侧完成（elicitation/complete 通知）。
	reasonComplete = "complete"
)

// CompleteByURLID 处理 agent 的 elicitation/complete 通知：URL 模式提问已在 agent 侧走完
// （用户在浏览器里完成授权/登录），宿主据此收敛卡片。命中规则：mode=url 且 ACP 侧 elicitationId 相同
// （该 id 由 agent 生成、全局唯一）；命中即以 accept（无 content）+ reason=complete 唤醒等待方，
// 让 agent 的 elicitation/create 请求收敛而非挂到超时。找不到（已决/重放/非 URL 模式）返回 false，
// 仅记日志——complete 是通知，重放与迟到都不该有副作用。
// ownerConversation 非空时校验归属（同一连接的防护）；空串表示不校验。
func (m *ElicitationManager) CompleteByURLID(ownerConversation, urlID string) bool {
	if urlID == "" {
		return false
	}
	m.mu.Lock()
	var hit *pendingElicitation
	for _, p := range m.pending {
		if p.mode == "url" && p.urlID == urlID {
			hit = p
			break
		}
	}
	if hit == nil {
		m.mu.Unlock()
		glog.Debugw(context.Background(), "elicitation: complete 未命中挂起项（已决/重放）", "url_id", urlID)
		return false
	}
	if ownerConversation != "" && hit.conversationID != ownerConversation {
		owner := hit.conversationID
		m.mu.Unlock()
		glog.Warnw(context.Background(), "elicitation: complete 与归属会话不符，拒绝（scope mismatch）",
			"url_id", urlID, "owner", owner, "caller", ownerConversation)
		return false
	}
	select {
	case hit.ch <- elicitationResolution{outcome: acl.ElicitationOutcome{Action: "accept"}, reason: reasonComplete}:
	default:
	}
	m.mu.Unlock()
	glog.Infow(context.Background(), "elicitation: complete 收敛挂起项", "url_id", urlID, "conversation", hit.conversationID)
	return true
}

// Owner 返回挂起提问的归属会话与来源（诊断/测试用）。
func (m *ElicitationManager) Owner(id string) (conversationID, scopeSource string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[id]
	if !ok {
		return "", "", false
	}
	return p.conversationID, p.scopeSource, true
}

// ElicitationItem 是一条挂起结构化提问的对外表示（REST/WS 载荷）。
type ElicitationItem struct {
	ID        string
	SessionID string
	// ConversationID 归属会话（scope，C3：与 SessionID 同值，语义更明确——决策必须由它发起）。
	ConversationID string
	// ScopeSource 归属来源：wire（wire 提供）| inferred_inflight（宿主按唯一在途 turn 推断）。
	ScopeSource string
	Mode        string
	Message     string
	Schema      map[string]any // form 模式；url 模式为空
	URL         string         // url 模式
	TimeoutAt   time.Time
}
