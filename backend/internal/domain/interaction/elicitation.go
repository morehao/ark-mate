package interaction

import (
	"context"

	"crypto/rand"
	"encoding/hex"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"sync"
	"time"

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
	outcome protocol.ElicitationOutcome
	reason  string
}

// pendingElicitation 是一条挂起中的结构化提问：持有一个「待用户决策」的通道与超时。
type pendingElicitation struct {
	id          string // 稳定 id（决策回传锚点）
	sessionID   string // 归属会话（scope；C3：决策必须由该会话发起，见 resolve）
	scopeSource string // 归属来源：wire（`_meta`）| inferred_inflight（宿主推断）| ambiguous（不归属）
	mode        string // form | url
	message     string
	schema      map[string]any // form 模式的 JSON Schema 对象（对外载荷）
	url         string         // url 模式
	urlID       string         // url 模式的 ACP elicitationId（elicitation/complete 的反查锚点）
	ch          chan elicitationResolution
	timeout     time.Time
	// late 已经**超时转 pending**（ADR-11）：卡片仍可回答，但那一轮的 ACP 调用已经回给底座了，
	// 所以晚到的回答不能再塞回旧轮——它要作为**新一轮**的用户消息进去（见 `ResolveLate`）。
	late bool
}

// ElicitationManager 管理挂起结构化提问：注册 → 发布事件 → 等待决策 → 按 id 回馈。
// 与 PermissionManager 同构（无「始终允许」类记忆：每次提问独立征询）。
type ElicitationManager struct {
	mu      sync.Mutex
	pending map[string]*pendingElicitation
	timeout time.Duration
	// onResolved 挂起提问「对事件说的话」：action 可能 **不是** 回给底座的那个 outcome
	// （超时那一支要发 `pending`，而回给底座的仍是 `cancel`——ADR-11 的关键一处不对称）。
	onResolved func(sessionID, id, action, reason string)
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
func (m *ElicitationManager) SetOnResolved(fn func(sessionID, id, action, reason string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onResolved = fn
}

// actionPending 挂起提问的超时形态：**问题仍可回答**（不是空答案、拒绝、确认或授权）。
//
// 它只出现在 `session.elicitation_resolved` 的 `action` 上，**永远不会**回给底座——
// 回给底座的仍是 `cancel`（那一轮的 ACP 调用必须有人应答）。这个不对称是 ADR-11 的核心：
// 「对用户说的」与「对底座说的」是两件事。
// 与 `domain/session` 的 `ElicitationActionPending` **逐字同值**（两个包不能互相 import：
// 事件常量的家在 session，管理面的家在 interaction）。这条边由 archtest 的
// `TestElicitationTimeoutBecomesPending` 钉着。
const actionPending = "pending"

// ResolveLate 回答一条**已经超时转 pending** 的提问：抹掉挂起项并回问题正文（供调用方把它
// 作为**新一轮**的用户消息注入）。`late=false` 的条目（还在阻塞等决策）→ `found=false`：
// 那种情况走 `ResolveAccept/Decline/Cancel`，两条路刻意不混。
//
// 为什么晚到的回答不能塞回旧轮：那一轮的 ACP 调用在超时那一刻就已经拿到 `cancel` 返回了，
// 底座那边的 agent 早已继续（或被收尾）。把回答投进一个已经关闭的通道，表现是"点了发送、
// 什么都没发生"——而这是本仓最忌的那类静默。
//
// **比参照产品弱一档（如实写）**：dsh 的 pending 跨 Host 重启仍可回答（回复作为新一轮进入）。
// 本仓选择「重启即作废」，理由是 elicitation 的归属在 D1 里只能**推断**（wire /
// inferred_inflight / ambiguous），跨重启保留 pending 会把一个推断出来的归属当成权威。
// 这条偏离登记在 `docs/unimplemented.md`。
func (m *ElicitationManager) ResolveLate(sessionID, id string) (message string, found, mismatch bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[id]
	if !ok || !p.late {
		return "", false, false
	}
	if sessionID != "" && p.sessionID != sessionID {
		glog.Warnw(context.Background(), "elicitation: 迟到回答的会话与归属不符，拒绝（scope mismatch）",
			"id", id, "owner", p.sessionID, "caller", sessionID)
		return "", true, true
	}
	delete(m.pending, id)
	return p.message, true, false
}

// genElicitationID 生成稳定 id。
func genElicitationID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "el-" + hex.EncodeToString(b)
}

// schemaMap 把 protocol.ElicitationSchema 折叠为对外 JSON Schema 对象（bus/REST 载荷）。
func schemaMap(s *protocol.ElicitationSchema) map[string]any {
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

// CancelSession 取消某会话的全部挂起提问（session/cancel 后按 Cancelled 回馈 agent）。
func (m *ElicitationManager) CancelSession(sessionID string) {
	m.CancelSessionReason(sessionID, reasonCancel)
}

// CancelSessionReason 同上，但由调用方给出**终局原因**（`cancel` / `archive` / `restart`）。
//
// 为什么要分开：收敛事件里的 `reason` 是给用户看的——「这一轮被取消」与「会话归档了」
// 是两件不同的事，用同一个值就只能靠猜。
func (m *ElicitationManager) CancelSessionReason(sessionID, reason string) {
	m.mu.Lock()
	var victims []*pendingElicitation
	for _, p := range m.pending {
		if p.sessionID == sessionID {
			victims = append(victims, p)
			delete(m.pending, p.id) // 立即不再挂起；消费方后续 delete 幂等
		}
	}
	m.mu.Unlock()
	for _, p := range victims {
		// 直接向持有 channel 投递（此时条目已移出 map，不能走按 id 的 resolve）。
		select {
		case p.ch <- elicitationResolution{outcome: protocol.ElicitationOutcome{Action: "cancel"}, reason: reason}:
		default:
		}
	}
}

// PendingCount 返回某会话的挂起提问数（代际迁移的忙碌探针用；与权限侧同名同义）。
func (m *ElicitationManager) PendingCount(sessionID string) int {
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

// PendingForSession 列出某会话的全部挂起提问（按 id 稳定序）。
func (m *ElicitationManager) pendingForSession(sessionID string) []*pendingElicitation {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*pendingElicitation
	for _, p := range m.pending {
		if p.sessionID == sessionID {
			out = append(out, p)
		}
	}
	return out
}

// RegisterAndWait 注册一条结构化提问并阻塞等待决策（SDK 派发 goroutine 中调用）。
// publish 副作用（发布 session.elicitation_request 事件）由调用方注入，避免 manager 依赖 bus。
// 返回最终决策；超时/上下文取消时自动按取消处理。
// PendingItems 某会话的挂起结构化提问（已装配成对外形态）。装配放域内的理由同 PermissionManager。
func (m *ElicitationManager) PendingItems(sessionID string) []ElicitationItem {
	ps := m.pendingForSession(sessionID)
	out := make([]ElicitationItem, 0, len(ps))
	for _, p := range ps {
		out = append(out, ElicitationItem{
			ID: p.id, SessionID: p.sessionID, ScopeSource: p.scopeSource, Mode: p.mode,
			Message: p.message, Schema: p.schema, URL: p.url, TimeoutAt: p.timeout,
		})
	}
	return out
}

func (m *ElicitationManager) RegisterAndWait(ctx context.Context, sessionID string, req protocol.ElicitationRequest, publish func(ElicitationItem)) protocol.ElicitationOutcome {
	id := genElicitationID()
	scopeSource := req.ScopeSource
	if scopeSource == "" {
		scopeSource = protocol.ScopeInferred
	}
	p := &pendingElicitation{
		id:          id,
		sessionID:   sessionID,
		scopeSource: scopeSource,
		mode:        req.Mode,
		message:     req.Message,
		schema:      schemaMap(req.Schema),
		url:         req.URL,
		urlID:       req.URLID,
		ch:          make(chan elicitationResolution, 1),
		timeout:     time.Now().Add(m.timeout),
	}
	m.mu.Lock()
	m.pending[id] = p
	m.mu.Unlock()
	glog.Infow(ctx, "elicitation: request registered", "session", sessionID, "id", id,
		"mode", req.Mode, "scope_source", scopeSource)

	if publish != nil {
		publish(ElicitationItem{
			ID:          id,
			SessionID:   p.sessionID,
			ScopeSource: p.scopeSource,
			Mode:        p.mode,
			Message:     p.message,
			Schema:      p.schema,
			URL:         p.url,
			TimeoutAt:   p.timeout,
		})
	}

	timer := time.NewTimer(m.timeout)
	defer timer.Stop()

	var (
		d protocol.ElicitationOutcome
		// eventAction 是对**事件**说的话，可能与回给底座的 `d.Action` 不同：超时那一支回给底座的是
		// `cancel`（那一轮的 ACP 调用必须有人应答），而事件说 `pending`（问题仍可回答）。
		eventAction = ""
		reason      = reasonUser
		pendingLate = false
	)
	select {
	case r := <-p.ch:
		// 已决策：结果与终局原因（user / complete）由投递方一并给出
		d, reason = r.outcome, r.reason
	case <-ctx.Done():
		d = protocol.ElicitationOutcome{Action: "cancel"} // 取消
		reason = reasonCancel
	case <-timer.C:
		// **超时不等于取消**（ADR-11，对齐参照产品的 open|continued|settled 状态机）：
		// 30 分钟无人回答只说明"现在没人看着"，不说明"用户不想答"。今天这一支铸的是 `cancel`，
		// 于是卡片消失、用户去倒杯水回来就得把整轮重来一遍——那是真实的可用性损失，而修法
		// 不引入任何新的不变量。
		//
		// 两件事同时发生：
		//   ① 回给底座 `cancel`（那一轮不能永远挂着——ACP 调用必须有人应答）；
		//   ② 挂起项**留在** map 里并标 `late`、清掉 `timeout_at`（卡片保留、无期限可答），
		//      事件发 `pending`。迟到的回答走 `ResolveLate` → **作为新一轮**的用户消息进去。
		glog.Warnw(ctx, "elicitation: request timeout, 转 pending（卡片保留可回答）",
			"session", sessionID, "id", id)
		d = protocol.ElicitationOutcome{Action: "cancel"}
		eventAction, reason = actionPending, reasonTimeout
		pendingLate = true
	}
	m.mu.Lock()
	if pendingLate {
		p.late = true
		p.timeout = time.Time{} // 无期限：`timeout_at` 不再下发，界面也不再起倒计时
	} else {
		delete(m.pending, id)
	}
	fn := m.onResolved
	m.mu.Unlock()
	if fn != nil {
		action := d.Action
		if eventAction != "" {
			action = eventAction
		}
		fn(sessionID, id, action, reason)
	}
	glog.Infow(ctx, "elicitation: resolved", "session", sessionID, "id", id,
		"action", d.Action, "event_action", eventAction, "reason", reason)
	return d
}

// resolve 按 id 投递一次终局（决策 + 终局原因），并校验**归属会话**（C3 scope 归属）：
// 只有登记该提问的会话才能决策——跨会话决策（陈旧标签页/串号调用）一律拒绝，
// 既不投递也不删除挂起项，调用方拿到 mismatch=true（对外统一表现为「不存在」）。
func (m *ElicitationManager) resolve(sessionID, id, reason string, d protocol.ElicitationOutcome) (found, mismatch bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[id]
	if !ok {
		return false, false
	}
	if sessionID != "" && p.sessionID != sessionID {
		glog.Warnw(context.Background(), "elicitation: 决策会话与归属会话不符，拒绝（scope mismatch）",
			"id", id, "owner", p.sessionID, "caller", sessionID)
		return true, true
	}
	select {
	case p.ch <- elicitationResolution{outcome: d, reason: reason}:
	default:
	}
	return true, false
}

// ResolveAccept 接受（可携带用户提交的 content）；sessionID 为决策发起方会话（scope 校验）。
func (m *ElicitationManager) ResolveAccept(sessionID, id string, content map[string]any) (found, mismatch bool) {
	return m.resolve(sessionID, id, reasonUser, protocol.ElicitationOutcome{Action: "accept", Content: content})
}

// ResolveDecline 用户拒绝提供信息。
func (m *ElicitationManager) ResolveDecline(sessionID, id string) (found, mismatch bool) {
	return m.resolve(sessionID, id, reasonUser, protocol.ElicitationOutcome{Action: "decline"})
}

// ResolveCancel 取消本次提问。
func (m *ElicitationManager) ResolveCancel(sessionID, id string) (found, mismatch bool) {
	return m.resolve(sessionID, id, reasonUser, protocol.ElicitationOutcome{Action: "cancel"})
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
// ownerSession 非空时校验归属（同一连接的防护）；空串表示不校验。
func (m *ElicitationManager) CompleteByURLID(ownerSession, urlID string) bool {
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
	if ownerSession != "" && hit.sessionID != ownerSession {
		owner := hit.sessionID
		m.mu.Unlock()
		glog.Warnw(context.Background(), "elicitation: complete 与归属会话不符，拒绝（scope mismatch）",
			"url_id", urlID, "owner", owner, "caller", ownerSession)
		return false
	}
	select {
	case hit.ch <- elicitationResolution{outcome: protocol.ElicitationOutcome{Action: "accept"}, reason: reasonComplete}:
	default:
	}
	m.mu.Unlock()
	glog.Infow(context.Background(), "elicitation: complete 收敛挂起项", "url_id", urlID, "session", hit.sessionID)
	return true
}

// Owner 返回挂起提问的归属会话与来源（诊断/测试用）。
func (m *ElicitationManager) Owner(id string) (sessionID, scopeSource string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[id]
	if !ok {
		return "", "", false
	}
	return p.sessionID, p.scopeSource, true
}

// ElicitationItem 是一条挂起结构化提问的对外表示（REST/WS 载荷）。
type ElicitationItem struct {
	ID string
	// SessionID 归属会话（scope，C3；决策必须由它发起）。
	//
	// v15 之前这里有两个**同值**字段（`SessionID` + `ConversationID`），是"一个东西两个词"
	// 的遗迹；改名后收敛成一个。
	SessionID string
	// ScopeSource 归属来源：wire（wire 提供）| inferred_inflight（宿主按唯一在途 run 推断）。
	ScopeSource string
	Mode        string
	Message     string
	Schema      map[string]any // form 模式；url 模式为空
	URL         string         // url 模式
	TimeoutAt   time.Time
}
