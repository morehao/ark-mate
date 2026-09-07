package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/runtime"
)

// agentCancelTimeout session/cancel RPC 的上限等待：agent 无响应（如模型限流重试挂起）时
// 不阻塞本地解除，Prompt 的 ctx 取消总是会立刻返回。
const agentCancelTimeout = 3 * time.Second

// acpSession 是 acl.Session 的最小接口（测试可注入桩；真实实现为 *acl.Session）。
type acpSession interface {
	Prompt(ctx context.Context, text string) (string, error)
	Cancel(ctx context.Context) error
}

// ActiveSession 是 manager 层持久会话句柄：绑定一条 ACP 会话与其所属连接，
// 支持多轮 prompt（每轮单飞）。事件经 eventSink 入 eventbus（agg = session id）。
type ActiveSession struct {
	ID      string
	AgentID string
	Runtime string

	client *acl.Client
	sess   acpSession

	mu      sync.Mutex
	busy    bool
	turn    *Turn
	dead    bool // 连接已断开，本会话不可再用
	dropPub bool // 断连错误事件已发布（幂等）
}

// Turn 一轮 prompt 的运行状态。
type Turn struct {
	done      chan struct{}
	cancel    context.CancelFunc
	cancelReq bool // 用户主动 cancel（区别于父 ctx 超时/外部取消）
	ctx       context.Context
	stop      string
	err       error
}

// CreateResult 是 StartSession 的结果。
type CreateResult struct {
	SessionID string
	AgentID   string
	Runtime   string
	Status    string
	EventSeq  int64 // session.created 事件的 bus 水位（供 WS attach 续传）
}

// Orchestrator 编排 Agent 生命周期与会话执行（连接获取→session/new→多轮 prompt→cancel）。
type Orchestrator struct {
	svc *Service
	reg *runtime.Registry

	mu       sync.Mutex
	conns    map[string]*runtimeConn // key = runtime ID；M0/M1 每 runtime 至多一条连接
	sessions map[string]*ActiveSession
	// busyConn 记录每连接上正在执行 turn 的会话（Prompt 开始 mark、runTurn 收尾 unmark）。
	// 用途：SDK v0.13.5 未建模 elicitation 的 scope 字段，宿主读不到顶层 sessionId；
	// 过渡期用「该连接上唯一在途 turn」推断 elicitation 的会话归属（见 §1.5 决策修订）。
	// 多会话并发在途时归属不唯一 → 不猜测，按取消回应。
	busyConn map[*acl.Client]map[string]struct{}
	allowAll bool // 调试/冒烟用：放行一切权限请求（默认拒绝）

	// perm 管理挂起权限请求（允许/拒绝 × once/always + 会话级始终允许记忆 + 超时自动拒）。
	perm *PermissionManager
	// elic 管理挂起结构化提问（elicitation/create：表单/URL + 超时自动取消）。
	elic *ElicitationManager

	// turnTimeout 单轮 prompt 看门狗（0 = 关闭；见 SetTurnTimeout / FORGE_TURN_TIMEOUT）。
	turnTimeout time.Duration

	logger *slog.Logger
}

type runtimeConn struct {
	client  *acl.Client
	agentID string
	spec    runtime.Spec
}

// RunOptions 一次 RunOnce（demo）的入参。
type RunOptions struct {
	RuntimeID  string // opencode / deepseekharness / echo
	Prompt     string
	Cwd        string   // 空则用当前工作目录
	Env        []string // 附加环境
	ConfigPath string   // 自定义 adapter 的 {config} 占位替换（内置 runtime 无占位，预留扩展）
}

// RunResult 一次 RunOnce（demo）的结果。
type RunResult struct {
	SessionID  string
	AgentID    string
	StopReason string
}

// NewOrchestrator 构造编排器并回挂到 Service（Service 负责 bus/存储/REST 方法）。
func NewOrchestrator(svc *Service, reg *runtime.Registry) *Orchestrator {
	o := &Orchestrator{
		svc:      svc,
		reg:      reg,
		conns:    make(map[string]*runtimeConn),
		sessions: make(map[string]*ActiveSession),
		busyConn: make(map[*acl.Client]map[string]struct{}),
		perm:     NewPermissionManager(DefaultPermissionTimeout),
		elic:     NewElicitationManager(DefaultElicitationTimeout),
		logger:   slog.Default(),
	}
	svc.attachOrchestrator(o)
	return o
}

// SetAllowAll 放开权限请求（仅调试/冒烟；默认一律拒绝）。
func (o *Orchestrator) SetAllowAll(v bool) { o.allowAll = v }

// SetPermissionTimeout 覆盖挂起权限请求的自动拒绝超时（0 → 默认 30min）。
func (o *Orchestrator) SetPermissionTimeout(d time.Duration) { o.perm.SetTimeout(d) }

// SetElicitationTimeout 覆盖挂起结构化提问的自动取消超时（0 → 默认 30min）。
func (o *Orchestrator) SetElicitationTimeout(d time.Duration) { o.elic.SetTimeout(d) }

// SetTurnTimeout 设置单轮 turn 看门狗超时（0 = 关闭）。见 watchdogTurn。
func (o *Orchestrator) SetTurnTimeout(d time.Duration) { o.turnTimeout = d }

// SetLogger 设置编排日志。
func (o *Orchestrator) SetLogger(l *slog.Logger) {
	if l != nil {
		o.logger = l
	}
}

// StartSession 在 agent 对应 runtime 上建立持久 ACP 会话（session/new，绝对 cwd），
// 并发布 session.created。不发起 prompt；Prompt 负责每轮。
func (o *Orchestrator) StartSession(ctx context.Context, agentID, cwd string) (*CreateResult, error) {
	agent, ok := o.svc.AgentByID(agentID)
	if !ok {
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", agentID), nil)
	}
	spec, ok := o.reg.Get(agent.Runtime)
	if !ok {
		return nil, acl.NewError(acl.ClassRequest, "runtime_not_found", fmt.Sprintf("runtime %q 未注册", agent.Runtime), nil)
	}
	rc, err := o.acquire(ctx, agent, spec)
	if err != nil {
		o.svc.setAgentState(agent.ID, AgentStateStopped)
		return nil, err
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	sess, err := rc.client.NewSession(ctx, acl.SessionOptions{Cwd: cwd})
	if err != nil {
		return nil, err
	}

	as := &ActiveSession{ID: sess.ID(), AgentID: agent.ID, Runtime: spec.ID, client: rc.client, sess: sess}
	o.mu.Lock()
	o.sessions[as.ID] = as
	o.mu.Unlock()

	o.svc.registerSession(&SessionRec{ID: as.ID, AgentID: agent.ID, Runtime: spec.ID, Status: SessionStatusInProgress})
	o.svc.beginTranscript(as.ID, agent.ID, spec.ID, cwd) // wire transcript header（未启用时 no-op）
	evt := o.svc.publish(BusSessionCreated, as.ID, map[string]any{
		"session_id": as.ID, "agent_id": agent.ID, "runtime": spec.ID,
	})
	return &CreateResult{SessionID: as.ID, AgentID: agent.ID, Runtime: spec.ID, Status: SessionStatusInProgress, EventSeq: evt.Seq}, nil
}

// Prompt 向会话追加一轮 prompt（异步；事件经 bus/WS 出流）。
// 返回 user_message 事件后的 bus 水位（客户端 attach 续传用）。会话正忙时返回 turn_in_progress。
func (o *Orchestrator) Prompt(ctx context.Context, sessionID, text string) (int64, error) {
	as := o.session(sessionID)
	if as == nil {
		return 0, acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", sessionID), nil)
	}
	as.mu.Lock()
	if as.dead {
		as.mu.Unlock()
		return 0, acl.NewError(acl.ClassConnection, "connection_lost", "会话所属连接已断开", nil)
	}
	if as.busy {
		as.mu.Unlock()
		return 0, acl.NewError(acl.ClassLimit, "turn_in_progress", "该会话正有 turn 在执行，请等待结束", nil)
	}
	tctx, cancel := context.WithCancel(ctx)
	t := &Turn{done: make(chan struct{}), cancel: cancel, ctx: tctx}
	as.turn = t
	as.busy = true
	client := as.client
	as.mu.Unlock()
	o.markBusy(client, as.ID)

	o.svc.markSession(as.ID, SessionStatusInProgress)
	ue := o.svc.publish(BusUserMessage, as.ID, map[string]any{
		"session_id": as.ID, "agent_id": as.AgentID, "text": text,
	})
	go o.runTurn(as, t, text)
	// 看门狗：turnTimeout 内未收尾则自动取消（防 runtime 吞错/挂起导致 busy 永久占用）。
	if o.turnTimeout > 0 {
		go o.watchdogTurn(as, sessionID, t)
	}
	return ue.Seq, nil
}

// watchdogTurn 单轮超时兜底：超时后走 CancelTurn 的有界取消路径（agent 无响应也本地解除），
// 终态事件由 runTurn 依取消语义发布（run.cancelled）。与用户手动取消幂等共存：turn 已结束时
// select 到 t.done 直接退出；已取消则 CancelTurn 对 nil turn 为 no-op。
func (o *Orchestrator) watchdogTurn(as *ActiveSession, sessionID string, t *Turn) {
	timer := time.NewTimer(o.turnTimeout)
	defer timer.Stop()
	select {
	case <-t.done:
	case <-timer.C:
		o.logger.Warn("turn 超过看门狗时限，自动取消", "session", sessionID, "timeout", o.turnTimeout.String())
		_ = o.CancelTurn(context.Background(), sessionID)
	}
}

// runTurn 执行一轮 prompt 并发布终态事件（单写者：仅本 goroutine 改动 turn 终态）。
func (o *Orchestrator) runTurn(as *ActiveSession, t *Turn, text string) {
	stop, err := as.sess.Prompt(t.ctx, text)

	as.mu.Lock()
	cancelled := t.cancelReq || (err == nil && strings.EqualFold(stop, "cancelled"))
	// agent 侧返回「取消类」错误（如 dsh 的 "Request cancelled"、SDK ctx 取消）也归一为 cancelled。
	if !cancelled && err != nil && errLooksCancelled(err) {
		cancelled = true
	}
	as.busy = false
	as.turn = nil
	client := as.client
	as.mu.Unlock()
	o.unmarkBusy(client, as.ID)
	t.stop = stop
	t.err = err

	switch {
	case cancelled:
		o.svc.markSession(as.ID, SessionStatusCancelled)
		o.svc.publish(BusRunCancelled, as.ID, map[string]any{"session_id": as.ID, "stop_reason": stop})
	case err != nil:
		o.svc.markSession(as.ID, SessionStatusFailed)
		o.publishError(as.ID, err)
	default:
		o.svc.markSession(as.ID, SessionStatusCompleted)
		o.svc.publish(BusTurnEnded, as.ID, map[string]any{
			"session_id": as.ID, "agent_id": as.AgentID, "stop_reason": stop,
		})
	}
	close(t.done)
}

// errLooksCancelled 判断错误是否为「取消」语义（ctx 取消或 agent 取消类错误文本）。
func errLooksCancelled(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	var ae *acl.Error
	if errors.As(err, &ae) {
		s := strings.ToLower(ae.Code + " " + ae.Details)
		return strings.Contains(s, "cancel") || strings.Contains(s, "abort")
	}
	return false
}

// CancelTurn 取消会话进行中的 turn（无 turn 时为幂等 no-op）。
func (o *Orchestrator) CancelTurn(ctx context.Context, sessionID string) error {
	as := o.session(sessionID)
	if as == nil {
		return acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", sessionID), nil)
	}
	as.mu.Lock()
	t := as.turn
	if t != nil {
		t.cancelReq = true
	}
	as.mu.Unlock()
	if t == nil {
		return nil
	}
	// agent 侧取消（session/cancel）有界执行：runtime 无响应时不阻塞本地解除。
	// 无论 RPC 是否送达，随后都 t.cancel() 让 Prompt 返回（runTurn 收尾不依赖 RPC）。
	cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), agentCancelTimeout)
	defer ccancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = as.sess.Cancel(cctx)
	}()
	select {
	case <-done:
	case <-cctx.Done():
		o.logger.Debug("session/cancel RPC 未在限期内响应，本地强制解除",
			"session", sessionID, "timeout", agentCancelTimeout.String())
	}
	t.cancel()                      // 解除 SDK 阻塞
	o.perm.CancelSession(sessionID) // session/cancel 后按 ACP 规范回 Cancelled 全部挂起权限
	o.elic.CancelSession(sessionID) // 挂起的结构化提问一并按取消回馈
	return nil
}

// WaitTurn 阻塞至当前 turn 结束（demo/同步路径用）；无 turn 立即返回。
func (o *Orchestrator) WaitTurn(ctx context.Context, sessionID string) (string, error) {
	as := o.session(sessionID)
	if as == nil {
		return "", acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", sessionID), nil)
	}
	as.mu.Lock()
	t := as.turn
	as.mu.Unlock()
	if t == nil {
		return "", nil
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-t.done:
	}
	return t.stop, t.err
}

// RunOnce 在指定 runtime 上跑一次 fresh session + prompt 并等待结束（forge demo 用）。
func (o *Orchestrator) RunOnce(ctx context.Context, opt RunOptions) (*RunResult, error) {
	agent, err := o.svc.requireAgent(opt.RuntimeID)
	if err != nil {
		return nil, err
	}
	res, err := o.StartSession(ctx, agent.ID, opt.Cwd)
	if err != nil {
		return nil, err
	}
	if _, err := o.Prompt(ctx, res.SessionID, opt.Prompt); err != nil {
		return nil, err
	}
	stop, err := o.WaitTurn(ctx, res.SessionID)
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		return &RunResult{SessionID: res.SessionID, AgentID: agent.ID}, err
	}
	if err != nil {
		// 父 ctx 超时/取消：runTurn 已按其 err 归为 failed/cancelled
		return &RunResult{SessionID: res.SessionID, AgentID: agent.ID, StopReason: stop}, err
	}
	return &RunResult{SessionID: res.SessionID, AgentID: agent.ID, StopReason: stop}, nil
}

// session 按 ID 取活跃会话（连接存活期内）。查找不存在（如进程重启后）按未找到处理。
func (o *Orchestrator) session(id string) *ActiveSession {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sessions[id]
}

// acquire 取（或拨）一条 runtime 连接：活着复用，断开则作废重拨；并挂断连监听。
func (o *Orchestrator) acquire(ctx context.Context, agent *AgentRec, spec runtime.Spec) (*runtimeConn, error) {
	o.mu.Lock()
	rc := o.conns[spec.ID]
	o.mu.Unlock()
	if rc != nil {
		select {
		case <-rc.client.Done():
			// 旧连接已断开（dropConn 已清理）：落到下方重拨
		default:
			return rc, nil
		}
	}

	args := resolveArgs(spec.Launch.Args, RunOptions{ConfigPath: ""})
	env := append([]string(nil), spec.Launch.Env...)
	t := acl.NewStdioTransport(spec.Launch.Command, args, env)
	logger := o.logger.With("runtime", spec.ID, "cmd", spec.Launch.Command)
	t.SetLogger(logger)

	reqPerm := func(ctx context.Context, req acl.PermissionRequest) acl.PermissionDecision {
		if o.allowAll {
			return acl.PermissionDecision{Allowed: true}
		}
		return o.requestPermission(ctx, req)
	}
	var cli *acl.Client // Dial 成功后回填；hook 都在连接读循环里被调用，晚于 Dial 返回
	reqElic := func(ctx context.Context, req acl.ElicitationRequest) acl.ElicitationOutcome {
		if o.allowAll {
			// 调试/冒烟（--allow-all）：不征询，直接按空内容接受（agent 自行处理空内容）。
			return acl.ElicitationOutcome{Action: "accept"}
		}
		sid := o.busySessionForClient(cli)
		if sid == "" {
			// SDK 未建模 elicitation scope → 无法读顶层 sessionId；连接上无唯一在途 turn
			// （空闲 / 会话外 request-scope / 多会话并发）时不做猜测，按取消回应。
			o.logger.Warn("elicitation: 连接上无唯一在途 turn，无法归属会话，按取消处理")
			return acl.ElicitationOutcome{Action: "cancel"}
		}
		req.SessionID = sid
		return o.requestElicitation(ctx, req)
	}
	client, err := acl.Dial(ctx, t, acl.ClientOptions{
		OnEvent:           o.eventSink(agent.ID, spec.ID),
		Logger:            logger,
		RequestPermission: reqPerm,
		CreateElicitation: reqElic,
	})
	if err != nil {
		_ = t.Close()
		return nil, err
	}
	cli = client

	o.svc.setAgentState(agent.ID, AgentStateRunning)
	rc = &runtimeConn{client: client, agentID: agent.ID, spec: spec}
	o.mu.Lock()
	o.conns[spec.ID] = rc
	o.mu.Unlock()
	go o.watchConn(rc)
	return rc, nil
}

// markBusy / unmarkBusy 维护 busyConn：会话进入 turn 时登记、收尾时撤销。
// client 为 nil（理论上不应出现）或未登记时均安全 no-op。
func (o *Orchestrator) markBusy(client *acl.Client, sessionID string) {
	if client == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	set := o.busyConn[client]
	if set == nil {
		set = make(map[string]struct{})
		o.busyConn[client] = set
	}
	set[sessionID] = struct{}{}
}

func (o *Orchestrator) unmarkBusy(client *acl.Client, sessionID string) {
	if client == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	set := o.busyConn[client]
	if set == nil {
		return
	}
	delete(set, sessionID)
	if len(set) == 0 {
		delete(o.busyConn, client)
	}
}

// busySessionForClient 返回该连接上「唯一在途」的会话 id；无在途或多会话并发在途返回空串。
func (o *Orchestrator) busySessionForClient(client *acl.Client) string {
	if client == nil {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	set := o.busyConn[client]
	if len(set) != 1 {
		return ""
	}
	for sid := range set {
		return sid
	}
	return ""
}

// watchConn 监听连接断开：清理注册、失活其上会话、标记 agent stopped。
func (o *Orchestrator) watchConn(rc *runtimeConn) {
	<-rc.client.Done()
	o.dropConn(rc)
}

func (o *Orchestrator) dropConn(rc *runtimeConn) {
	o.mu.Lock()
	if o.conns[rc.spec.ID] == rc {
		delete(o.conns, rc.spec.ID)
	}
	delete(o.busyConn, rc.client) // 连接断开：在途登记一并作废（runTurn 的 unmark 对已删键为 no-op）
	var victims []*ActiveSession
	for _, as := range o.sessions {
		if as.client == rc.client {
			victims = append(victims, as)
		}
	}
	o.mu.Unlock()
	for _, as := range victims {
		o.disconnectSession(as)
	}
	o.svc.setAgentState(rc.agentID, AgentStateStopped)
}

// disconnectSession 把某断连连接上的会话失活：进行中 turn 解除阻塞（runTurn 依 err 收尾），
// 空闲会话直接标记 failed 并发布 connection_lost（幂等）。
func (o *Orchestrator) disconnectSession(as *ActiveSession) {
	as.mu.Lock()
	if as.dead {
		as.mu.Unlock()
		return
	}
	as.dead = true
	t := as.turn
	idle := t == nil
	as.mu.Unlock()

	if t != nil {
		t.cancel()
		return
	}
	if idle {
		as.mu.Lock()
		pub := as.dropPub
		as.dropPub = true
		as.mu.Unlock()
		if !pub {
			o.svc.markSession(as.ID, SessionStatusFailed)
			o.svc.publish(BusTurnError, as.ID, map[string]any{
				"session_id": as.ID, "code": "connection_lost", "class": "connection",
				"details": "agent 连接断开（会话失活；是否可 resume 依 runtime 能力）",
			})
		}
	}
}

// eventSink 把 acl 归一化事件转为域事件进 eventbus（agg = session id）。
func (o *Orchestrator) eventSink(agentID, runtimeID string) func(acl.Event) {
	return func(ev acl.Event) {
		if ev.SessionID == "" {
			return
		}
		typ := ""
		var payload any
		switch ev.Kind {
		case acl.KindMessage:
			typ = BusMessage
			if d, ok := ev.Payload.(acl.MessageDelta); ok {
				payload = map[string]any{"session_id": ev.SessionID, "agent_id": agentID, "text": d.Text}
				if d.MessageID != "" {
					payload.(map[string]any)["msg_id"] = d.MessageID
				}
			}
		case acl.KindThought:
			typ = BusThought
			if d, ok := ev.Payload.(acl.ThoughtDelta); ok {
				payload = map[string]any{"session_id": ev.SessionID, "agent_id": agentID, "text": d.Text}
				if d.MessageID != "" {
					payload.(map[string]any)["msg_id"] = d.MessageID
				}
			}
		case acl.KindToolCall:
			typ = BusToolCall
			if d, ok := ev.Payload.(acl.ToolCallInfo); ok {
				payload = toolPayload(ev.SessionID, agentID, d)
			}
		case acl.KindToolUpdate:
			typ = BusToolUpdate
			if d, ok := ev.Payload.(acl.ToolCallInfo); ok {
				payload = toolPayload(ev.SessionID, agentID, d)
			}
		case acl.KindUserMessage:
			typ = BusUserMessage
			if d, ok := ev.Payload.(acl.MessageDelta); ok {
				payload = map[string]any{"session_id": ev.SessionID, "agent_id": agentID, "text": d.Text}
				if d.MessageID != "" {
					payload.(map[string]any)["msg_id"] = d.MessageID
				}
			}
		case acl.KindUsage:
			typ = BusUsage
			payload = map[string]any{"session_id": ev.SessionID, "agent_id": agentID}
		case acl.KindPermission:
			typ = BusPermission
			payload = ev.Payload
		}
		if typ != "" && payload != nil {
			o.svc.publish(typ, ev.SessionID, payload)
		}
	}
}

// toolPayload 构造工具事件载荷：稳定键 tool_call_id/title/status，可选透传 kind/input/output
// （ACP ToolKind/rawInput/rawOutput，决策 v5②；字段为空/缺省时不出现在载荷中）。
func toolPayload(sessionID, agentID string, d acl.ToolCallInfo) map[string]any {
	p := map[string]any{
		"session_id": sessionID, "agent_id": agentID,
		"tool_call_id": d.ID, "title": d.Title, "status": d.Status,
	}
	if d.Kind != "" {
		p["kind"] = d.Kind
	}
	if d.Input != nil {
		p["input"] = d.Input
	}
	if d.Output != nil {
		p["output"] = d.Output
	}
	return p
}

// requestPermission 处理一次 ACP 权限请求（在 SDK 派发的 goroutine 中阻塞等待用户决策）：
//   - 会话级始终允许记忆命中 → 直接放行（快速路径）；
//   - 否则发布 session.permission_request 事件（含 call_id/options）并阻塞等待
//     PermissionsResolve（allow/reject × once/always）；超时或 ctx 取消自动拒绝。
//
// 事件发布（publish）注入的给 PermissionManager，避免 manager 依赖 bus。
func (o *Orchestrator) requestPermission(ctx context.Context, req acl.PermissionRequest) acl.PermissionDecision {
	if d, ok := o.perm.AutoDecision(req.SessionID, req); ok {
		return d
	}
	publish := func(callID, title string, tool acl.ToolCallInfo, opts []acl.PermissionOption, timeout time.Time) {
		o.svc.publish(BusPermissionRequest, req.SessionID, map[string]any{
			"session_id":   req.SessionID,
			"call_id":      callID,
			"title":        title,
			"tool_call_id": tool.ID,
			"kind":         tool.Kind,
			"input":        tool.Input,
			"options":      opts,
			"timeout_at":   timeout.UTC().Format(time.RFC3339), // UI 端超时收敛（无用户决策时自动拒绝）
		})
	}
	return o.perm.RegisterAndWait(ctx, req.SessionID, req, publish)
}

// PendingPermissions 列出某会话的挂起权限请求（attach/snapshot 供 UI 重建）。
func (o *Orchestrator) PendingPermissions(sessionID string) []PermissionItem {
	ps := o.perm.pendingForSession(sessionID)
	out := make([]PermissionItem, 0, len(ps))
	for _, p := range ps {
		out = append(out, PermissionItem{
			CallID:    p.callID,
			SessionID: p.sessionID,
			Title:     p.title,
			ToolCall:  p.toolCall,
			Options:   p.options,
			TimeoutAt: p.timeout,
		})
	}
	return out
}

// ResolvePermission 按 call_id 投递决策（allow/reject × once/always）。
func (o *Orchestrator) ResolvePermission(callID, optionID string, always, allowed bool) (bool, error) {
	if callID == "" {
		return false, acl.NewError(acl.ClassRequest, "call_id_required", "call_id 必填", nil)
	}
	if allowed {
		return o.perm.ResolveAllow(callID, optionID, always), nil
	}
	return o.perm.ResolveReject(callID), nil
}

// AlwaysAllowed 列出某会话的会话级「始终允许」记忆（决策 v4① 的 allow_always 侧）。
func (o *Orchestrator) AlwaysAllowed(sessionID string) []AlwaysAllowItem {
	return o.perm.AlwaysAllowed(sessionID)
}

// RevokeAlways 撤销会话级「始终允许」记忆。
//   - title、kind 均非空 → 撤销单条（精确匹配 action+identifier）；
//   - 两者均为空 → 一键撤销整会话全部记忆（= ForgetSession）。
func (o *Orchestrator) RevokeAlways(sessionID, title, kind string) (bool, error) {
	switch {
	case title == "" && kind == "":
		o.perm.ForgetSession(sessionID)
		return true, nil
	case title == "" || kind == "":
		return false, acl.NewError(acl.ClassRequest, "always_revoke_params", "撤销单条需 title 与 kind 同时提供；两者都空则撤销全部", nil)
	}
	return o.perm.RevokeAlways(sessionID, title, kind), nil
}

// requestElicitation 处理一次 ACP 结构化提问请求（在 SDK 派发的 goroutine 中阻塞等待用户决策）：
//   - 发布 session.elicitation_request 事件（含 elicitation_id/mode/message/schema|url/timeout_at）
//     并阻塞等待 ElicitationResolve（accept/decline/cancel）；超时或 ctx 取消自动按取消处理。
//
// 事件发布（publish）注入给 ElicitationManager，避免 manager 依赖 bus。
func (o *Orchestrator) requestElicitation(ctx context.Context, req acl.ElicitationRequest) acl.ElicitationOutcome {
	publish := func(it ElicitationItem) {
		payload := map[string]any{
			"session_id":     it.SessionID,
			"elicitation_id": it.ID,
			"mode":           it.Mode,
			"message":        it.Message,
			"timeout_at":     it.TimeoutAt.UTC().Format(time.RFC3339),
		}
		if it.Mode == "form" {
			payload["schema"] = it.Schema
		} else {
			payload["url"] = it.URL
		}
		o.svc.publish(BusElicitationRequest, it.SessionID, payload)
	}
	return o.elic.RegisterAndWait(ctx, req.SessionID, req, publish)
}

// PendingElicitations 列出某会话的挂起结构化提问（attach/snapshot 供 UI 重建）。
func (o *Orchestrator) PendingElicitations(sessionID string) []ElicitationItem {
	ps := o.elic.pendingForSession(sessionID)
	out := make([]ElicitationItem, 0, len(ps))
	for _, p := range ps {
		out = append(out, ElicitationItem{
			ID:        p.id,
			SessionID: p.sessionID,
			Mode:      p.mode,
			Message:   p.message,
			Schema:    p.schema,
			URL:       p.url,
			TimeoutAt: p.timeout,
		})
	}
	return out
}

// ResolveElicitation 按 elicitation_id 投递决策（accept/decline/cancel；accept 可带 content）。
func (o *Orchestrator) ResolveElicitation(elicitationID, action string, content map[string]any) (bool, error) {
	if elicitationID == "" {
		return false, acl.NewError(acl.ClassRequest, "elicitation_id_required", "elicitation_id 必填", nil)
	}
	var ok bool
	switch action {
	case "accept":
		ok = o.elic.ResolveAccept(elicitationID, content)
	case "decline":
		ok = o.elic.ResolveDecline(elicitationID)
	case "cancel":
		ok = o.elic.ResolveCancel(elicitationID)
	default:
		return false, acl.NewError(acl.ClassRequest, "elicitation_action", "action 需为 accept/decline/cancel 之一", nil)
	}
	return ok, nil
}

// publishError 把错误按 acl 分类发布为 turn.error。
func (o *Orchestrator) publishError(sessionID string, err error) {
	ae := acl.Classify(err)
	o.svc.publish(BusTurnError, sessionID, map[string]any{
		"session_id": sessionID,
		"code":       ae.Code,
		"class":      string(ae.Class),
		"details":    ae.Details,
	})
}

// Close 关闭全部连接并清理（daemon 退出/测试收尾）。
func (o *Orchestrator) Close() {
	o.mu.Lock()
	conns := make([]*runtimeConn, 0, len(o.conns))
	for _, rc := range o.conns {
		conns = append(conns, rc)
	}
	o.conns = make(map[string]*runtimeConn)
	o.mu.Unlock()
	for _, rc := range conns {
		o.disconnectSessionAll(rc)
		o.svc.setAgentState(rc.agentID, AgentStateStopped)
		_ = rc.client.Close()
	}
	o.svc.closeTranscripts()
}

func (o *Orchestrator) disconnectSessionAll(rc *runtimeConn) {
	o.mu.Lock()
	var victims []*ActiveSession
	for _, as := range o.sessions {
		if as.client == rc.client {
			victims = append(victims, as)
		}
	}
	o.mu.Unlock()
	for _, as := range victims {
		o.disconnectSession(as)
	}
}

// resolveArgs 替换 Launch.Args 中的 {config} 占位（保留给自定义 adapter 模板；
// 当前内置 runtime 的 Args 均已不含占位，此函数为幂等空操作）。
func resolveArgs(args []string, opt RunOptions) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, "{config}", opt.ConfigPath)
	}
	return out
}
