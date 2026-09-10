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

	"github.com/google/uuid"
	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/infra/procreg"
	"github.com/morehao/ark-forge/internal/runtime"
)

// agentCancelTimeout conversation/cancel RPC 的上限等待：agent 无响应（如模型限流重试挂起）时
// 不阻塞本地解除，Prompt 的 ctx 取消总是会立刻返回。
const agentCancelTimeout = 3 * time.Second

// DefaultHandoffMaxChars bounded handoff memo 默认字符预算（§18 D-3；env FORGE_HANDOFF_MAX_CHARS 覆盖）。
const DefaultHandoffMaxChars = 6000

// acpSession 是 acl.Session 的最小接口（测试可注入桩；真实实现为 *acl.Session）。
type acpSession interface {
	Prompt(ctx context.Context, text string) (string, error)
	Cancel(ctx context.Context) error
	// ConfigOptions 会话配置项（模型目录等，唯一来源：运行时自报）。
	ConfigOptions() []acl.ConfigOption
}

// ActiveConversation 是 manager 层持久会话句柄：绑定一条 ACP 会话与其所属连接，
// 支持多轮 prompt（每轮单飞）。事件经 eventSink 入 eventbus（agg = conversation id）。
type ActiveConversation struct {
	ID      string
	AgentID string
	Runtime string

	client *acl.Client
	sess   acpSession
	// proj 会话的可执行投影（§4.2 会话边界版；design doc §16 Orchestrator 采用 Runtime）。
	// 由 startConversationOn 依 spec 构造；测试注入的假会话为 nil → 走 sess 直连路径。
	proj runtime.Runtime

	mu      sync.Mutex
	busy    bool
	turn    *Turn
	dead    bool // 连接已断开，本会话不可再用
	dropPub bool // 断连错误事件已发布（幂等）
	// memo bounded handoff memo（P3 F3）：跨 runtime / 无原生 resume 兜底建的 fresh session
	// 首条 prompt 注入（Prompt 消费后清空，仅生效一次）。
	memo string
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

// CreateResult 是 StartConversation 的结果。
type CreateResult struct {
	ConversationID string
	AgentID        string
	Runtime        string
	Status         string
	EventSeq       int64 // conversation.created 事件的 bus 水位（供 WS attach 续传）
}

// Orchestrator 编排 Agent 生命周期与会话执行（连接获取→conversation/new→多轮 prompt→cancel）。
type Orchestrator struct {
	svc *Service
	reg *runtime.Registry

	mu            sync.Mutex
	conns         map[string]*runtimeConn // key = connKey(runtime, 原生权限 env)；同配置共享暖进程
	procReg       *procreg.Registry       // runtime 子进程注册表（孤儿回收；nil = 禁用）
	conversations map[string]*ActiveConversation
	// byProvider：原生 provider session id → 平台会话 uuid（P3 双 id）。事件/权限等底层
	// 回调只携带原生 id（acl.Event.SessionID / PermissionRequest.SessionID），host 侧经此
	// 解析为平台会话 id 再发布/键控；会话失活（dropConn）时随 victim 一并清除。
	byProvider map[string]string
	reapStop   context.CancelFunc // 空闲回收 goroutine 停止（Close 时调用）

	// inflight 记录每连接上正在执行 turn 的会话（inFlightTracker：§17 E1 显式化簿记，
	// 语义同 §1.5 决策修订——连接上唯一在途才归属，多会话并发在途不猜测）。
	inflight *inFlightTracker
	allowAll bool // 调试/冒烟用：放行一切权限请求（默认拒绝）

	// perm 管理挂起权限请求（允许/拒绝 × once/always + 会话级始终允许记忆 + 超时自动拒）。
	perm *PermissionManager
	// elic 管理挂起结构化提问（elicitation/create：表单/URL + 超时自动取消）。
	elic *ElicitationManager

	// usageMu/usage 保护「每平台会话最近一次 ACP 用量快照」（M3 用量统计：turn 收口时
	// 随 runs.usage_json 落库；底座不上报时无条目）。
	usageMu sync.Mutex
	usage   map[string]acl.UsageInfo

	// turnTimeout 单轮 prompt 看门狗（0 = 关闭；见 SetTurnTimeout / FORGE_TURN_TIMEOUT）。
	turnTimeout time.Duration

	// handoffMaxChars bounded handoff memo 字符预算（§5.6/§18 D-3；见 SetHandoffMaxChars /
	// FORGE_HANDOFF_MAX_CHARS）。0 = 关闭 memo 兜底。
	handoffMaxChars int

	logger *slog.Logger
}

type runtimeConn struct {
	client  *acl.Client
	agentID string
	spec    runtime.Spec
	// key 连接缓存键（B5：runtime ID + 原生权限 env 签名）。同一 runtime 但原生权限配置不同的
	// agent 必须各自独立进程（权限开关是进程级），配置相同则共享暖进程。
	key     string
	started time.Time // 连接建立时刻（空闲回收计时起点；仅 StartAgent 类无会话暖进程可回收）
	pid     int       // 子进程 pid（进程注册表登记用；0 = 未登记）
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
	ConversationID string
	AgentID        string
	StopReason     string
}

// NewOrchestrator 构造编排器并回挂到 Service（Service 负责 bus/存储/REST 方法）。
func NewOrchestrator(svc *Service, reg *runtime.Registry) *Orchestrator {
	o := &Orchestrator{
		svc:             svc,
		reg:             reg,
		conns:           make(map[string]*runtimeConn),
		conversations:   make(map[string]*ActiveConversation),
		byProvider:      make(map[string]string),
		inflight:        newInFlightTracker(),
		perm:            NewPermissionManager(DefaultPermissionTimeout),
		elic:            NewElicitationManager(DefaultElicitationTimeout),
		usage:           make(map[string]acl.UsageInfo),
		handoffMaxChars: DefaultHandoffMaxChars,
		logger:          slog.Default(),
	}
	// 挂起权限一旦有终局（用户决策 / 超时自动拒绝 / 会话取消），发布收敛事件
	// （conversation.permission_resolved），前端据此收敛权限卡——服务端事件是唯一事实，不再依赖前端时钟。
	o.perm.SetOnResolved(func(conversationID, callID, reason string, d acl.PermissionDecision) {
		outcome := "cancelled"
		if d.Allowed || (d.Rejected && d.OptionID != "") {
			outcome = "selected"
		}
		o.svc.publish(BusPermissionResolved, conversationID, map[string]any{
			"session_id": conversationID,
			"call_id":    callID,
			"outcome":    outcome,
			"reason":     reason,
			"allowed":    d.Allowed,
			"rejected":   d.Rejected,
			"option_id":  d.OptionID,
			"always":     d.Always,
		})
	})
	// 会话级记忆持久化挂钩（sqlite 模式写 permission_memories 表；st 为 nil 时 no-op）。
	o.perm.SetMemoryHooks(o.svc.persistMemoryAdd, o.svc.persistMemoryDel, o.svc.persistMemoryDelAll)
	// 挂起提问一旦有终局（accept/decline/cancel/超时自动取消/会话取消），发布收敛事件
	// （conversation.elicitation_resolved），前端据此收敛提问卡——服务端事件是唯一事实。
	o.elic.SetOnResolved(func(conversationID, id, reason string, d acl.ElicitationOutcome) {
		o.svc.publish(BusElicitationResolved, conversationID, map[string]any{
			"session_id":     conversationID,
			"elicitation_id": id,
			"action":         d.Action,
			"reason":         reason,
		})
	})
	svc.attachOrchestrator(o)
	return o
}

// SetAllowAll 放开权限请求（仅调试/冒烟；默认一律拒绝）。
func (o *Orchestrator) SetAllowAll(v bool) { o.allowAll = v }

// RestoreMemoriesFromStore 启动恢复会话级权限记忆（sqlite 模式：
// permission_memories 全库装载进 perm.mem——allow/reject 双侧、跨重启不丢）。
func (o *Orchestrator) RestoreMemoriesFromStore(ctx context.Context) error {
	rows, err := o.svc.memoryRows(ctx)
	if err != nil {
		return err
	}
	items := make([]MemoryItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, MemoryItem{
			conversationID: r.ConversationID, Title: r.Title, Kind: r.Kind,
			Decision: r.Decision, Since: r.CreatedAt,
		})
	}
	o.perm.LoadMemories(items)
	o.logger.Info("permission memories restored", "count", len(items))
	return nil
}

// SetPermissionTimeout 覆盖挂起权限请求的自动拒绝超时（0 → 默认 30min）。
func (o *Orchestrator) SetPermissionTimeout(d time.Duration) { o.perm.SetTimeout(d) }

// SetElicitationTimeout 覆盖挂起结构化提问的自动取消超时（0 → 默认 30min）。
func (o *Orchestrator) SetElicitationTimeout(d time.Duration) { o.elic.SetTimeout(d) }

// SetTurnTimeout 设置单轮 turn 看门狗超时（0 = 关闭）。见 watchdogTurn。
func (o *Orchestrator) SetTurnTimeout(d time.Duration) { o.turnTimeout = d }

// SetHandoffMaxChars 设置 bounded handoff memo 字符预算（0 = 关闭 memo 兜底）。见 ensureConversation。
func (o *Orchestrator) SetHandoffMaxChars(n int) { o.handoffMaxChars = n }

// SetLogger 设置编排日志。
func (o *Orchestrator) SetLogger(l *slog.Logger) {
	if l != nil {
		o.logger = l
	}
}

// ApplyConversationModel 把模型变更应用到**已存在的会话**（会话内改模型）。
//
//   - 会话不在活跃集合（未启动/已断连）→ (false, nil)：调用方只更新元数据，等下次恢复会话时生效；
//   - 活跃 → 经 session/set_config_option 应用（运行时是唯一判据），失败原样上抛（调用方不得吞）。
func (o *Orchestrator) ApplyConversationModel(ctx context.Context, conversationID string, model runtime.ModelConfig) (bool, error) {
	o.mu.Lock()
	as := o.conversations[conversationID]
	o.mu.Unlock()
	if as == nil || as.dead || as.sess == nil {
		return false, nil
	}
	if _, ok := o.svc.AgentByID(as.AgentID); !ok {
		return false, nil
	}
	spec, ok := o.reg.Get(as.Runtime)
	if !ok {
		return false, nil
	}
	if err := o.applyModelOnSession(ctx, as, as.AgentID, spec, as.sess, model); err != nil {
		return true, err
	}
	return true, nil
}

// StartConversation 在 agent 路由选定的 runtime 上建立持久 ACP 会话（conversation/new，绝对 cwd），
// 并发布 conversation.created。不发起 prompt；Prompt 负责每轮。
// 路由 = ResolveRuntime（§7 FirstAvailable：候选顺序 × 已注册 × 命令可执行）。
func (o *Orchestrator) StartConversation(ctx context.Context, agentID, cwd string, model runtime.ModelConfig) (*CreateResult, error) {
	agent, ok := o.svc.AgentByID(agentID)
	if !ok {
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", agentID), nil)
	}
	runtimeID, err := o.svc.ResolveRuntime(agent)
	if err != nil {
		return nil, err
	}
	return o.startConversationOn(ctx, agent, runtimeID, cwd, model)
}

// startConversationOn 在指定 runtime 上建立持久 ACP 会话（StartConversation 的路由首候选、
// RunOnce 的显式 --runtime 都汇聚于此）。返回的 CreateResult.Runtime 为实际选中的 runtime。
func (o *Orchestrator) startConversationOn(ctx context.Context, agent *AgentRec, runtimeID, cwd string, model runtime.ModelConfig) (*CreateResult, error) {
	spec, ok := o.reg.Get(runtimeID)
	if !ok {
		return nil, acl.NewError(acl.ClassRequest, "runtime_not_found", fmt.Sprintf("runtime %q 未注册", runtimeID), nil)
	}
	rc, err := o.acquire(ctx, agent, spec)
	if err != nil {
		o.svc.setAgentState(agent.ID, AgentStateStopped)
		return nil, err
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	// P2 MCP 注入：平台注册表 MCP 子集按 agent 白名单 + runtime 传输能力过滤后随 session/new 下发（§5.2/§5.3）。
	sess, err := rc.client.NewSession(ctx, acl.SessionOptions{
		Cwd:        cwd,
		McpServers: o.svc.McpServersFor(agent, spec.ID),
	})
	if err != nil {
		return nil, err
	}

	// P3 双 id（D-1，用户已授权破坏性变更）：会话对外 id = 平台 uuid（隐藏底座 id 形状、
	// 跨重启稳定定位）；原生 provider id 存 sess/ProviderSessionID 列，供恢复承载（load/resume）。
	convID := uuid.NewString()
	as := &ActiveConversation{ID: convID, AgentID: agent.ID, Runtime: spec.ID, client: rc.client, sess: sess,
		proj: runtime.NewProjection(spec, sess)}
	// 会话级模型应用（运行时派生模型面的落地，决策记录）：
	//   - 值必须来自运行时自报目录（category=model），set 的成败即「可用性」判据；
	//   - 运行时**拒绝**该值 → 建会话失败（错误上抛给调用方，绝不静默回落到底座默认模型）；
	//   - unsupported_*（echo 无模型、运行时未广告模型配置项）→ 忽略（无模型语义）；
	//   - 成功 → 用返回的**全量**配置项刷新平台目录缓存（顺带清掉曾失败值的标记）。
	if err := o.applyModelOnSession(ctx, as, agent.ID, spec, sess, model); err != nil {
		_ = sess.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	o.mu.Lock()
	o.conversations[as.ID] = as
	o.byProvider[sess.ID()] = as.ID
	o.mu.Unlock()

	o.svc.registerConversation(&ConversationRec{ID: as.ID, AgentID: agent.ID, Cwd: cwd, Provider: agent.Provider,
		Model: model.Model, RuntimeHint: spec.ID,
		ProviderSessionID: sess.ID(), Status: ConversationStatusInProgress})
	o.svc.beginTranscript(as.ID, agent.ID, spec.ID, cwd) // wire transcript header（未启用时 no-op）
	evt := o.svc.publish(BusSessionCreated, as.ID, map[string]any{
		"session_id": as.ID, "agent_id": agent.ID, "runtime": spec.ID,
	})
	return &CreateResult{ConversationID: as.ID, AgentID: agent.ID, Runtime: spec.ID, Status: ConversationStatusInProgress, EventSeq: evt.Seq}, nil
}

// Prompt 向会话追加一轮 prompt（异步；事件经 bus/WS 出流）。
// 返回 user_message 事件后的 bus 水位（客户端 attach 续传用）。会话正忙时返回 turn_in_progress。
// 会话不在活跃集合：P3 F2 起按需恢复（持久层有可恢复记录 → ensureConversation 重建活会话）；
// 无记录/终态 → conversation_not_found（对齐 P0 语义）。
func (o *Orchestrator) Prompt(ctx context.Context, conversationID, text string) (int64, error) {
	as := o.conversation(conversationID)
	if as == nil {
		var err error
		as, err = o.ensureConversation(ctx, conversationID)
		if err != nil {
			return 0, err
		}
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
	memo := as.memo
	as.memo = "" // 一次性消费：仅 fresh+handoff 会话首条 prompt 注入 memo
	tctx, cancel := context.WithCancel(ctx)
	t := &Turn{done: make(chan struct{}), cancel: cancel, ctx: tctx}
	as.turn = t
	as.busy = true
	client := as.client
	as.mu.Unlock()
	o.markBusy(client, as.ID)

	o.svc.markConversation(as.ID, ConversationStatusInProgress)
	ue := o.svc.publish(BusUserMessage, as.ID, map[string]any{
		"session_id": as.ID, "agent_id": as.AgentID, "text": text,
	})
	// P3 F3 memo 注入：摘要仅作为发给 runtime 的首条 prompt 引导（user_message 事件保持用户
	// 原文，不含摘要）。SDK v0.13.5 无 provider handoff 通道 → 文本前缀承载；provider 侧拒绝
	// 重试语义随 SDK 能力后置，不臆造。
	if memo != "" {
		text = memo + "\n\n" + text
	}
	go o.runTurn(as, t, text)
	// 看门狗：turnTimeout 内未收尾则自动取消（防 runtime 吞错/挂起导致 busy 永久占用）。
	if o.turnTimeout > 0 {
		go o.watchdogTurn(as, conversationID, t)
	}
	return ue.Seq, nil
}

// watchdogTurn 单轮超时兜底：超时后走 CancelTurn 的有界取消路径（agent 无响应也本地解除），
// 终态事件由 runTurn 依取消语义发布（run.cancelled）。与用户手动取消幂等共存：turn 已结束时
// select 到 t.done 直接退出；已取消则 CancelTurn 对 nil turn 为 no-op。
func (o *Orchestrator) watchdogTurn(as *ActiveConversation, conversationID string, t *Turn) {
	timer := time.NewTimer(o.turnTimeout)
	defer timer.Stop()
	select {
	case <-t.done:
	case <-timer.C:
		o.logger.Warn("turn 超过看门狗时限，自动取消", "conversation", conversationID, "timeout", o.turnTimeout.String())
		_ = o.CancelTurn(context.Background(), conversationID)
	}
}

// runTurn 执行一轮 prompt 并发布终态事件（单写者：仅本 goroutine 改动 turn 终态）。
// Prompt 走 ActiveConversation.proj（Runtime 接口；§16 采用），proj 为 nil（测试假会话/
// 未投影 runtime）时回退底层会话直连——两路径语义一致。
func (o *Orchestrator) runTurn(as *ActiveConversation, t *Turn, text string) {
	var (
		stop string
		err  error
	)
	if as.proj != nil {
		stop, err = as.proj.Prompt(t.ctx, runtime.PromptRequest{Text: text})
	} else {
		stop, err = as.sess.Prompt(t.ctx, text)
	}

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
		o.svc.markConversation(as.ID, ConversationStatusCancelled)
		o.svc.publish(BusRunCancelled, as.ID, map[string]any{"session_id": as.ID, "stop_reason": stop})
	case err != nil:
		o.svc.markConversation(as.ID, ConversationStatusFailed)
		o.publishError(as.ID, err, client)
	default:
		o.svc.markConversation(as.ID, ConversationStatusCompleted)
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
func (o *Orchestrator) CancelTurn(ctx context.Context, conversationID string) error {
	as := o.conversation(conversationID)
	if as == nil {
		return acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", conversationID), nil)
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
	// agent 侧取消（conversation/cancel）有界执行：runtime 无响应时不阻塞本地解除。
	// 无论 RPC 是否送达，随后都 t.cancel() 让 Prompt 返回（runTurn 收尾不依赖 RPC）。
	cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), agentCancelTimeout)
	defer ccancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// §17 E2：会话级取消走投影（Cancel 已入 Runtime 接口）；proj nil（测试注入假会话）
		// 回退直连 sess.Cancel，语义一致。
		if as.proj != nil {
			_ = as.proj.Cancel(cctx)
			return
		}
		_ = as.sess.Cancel(cctx)
	}()
	select {
	case <-done:
	case <-cctx.Done():
		o.logger.Debug("conversation/cancel RPC 未在限期内响应，本地强制解除",
			"conversation", conversationID, "timeout", agentCancelTimeout.String())
	}
	t.cancel()                           // 解除 SDK 阻塞
	o.perm.CancelSession(conversationID) // conversation/cancel 后按 ACP 规范回 Cancelled 全部挂起权限
	o.elic.CancelSession(conversationID) // 挂起的结构化提问一并按取消回馈
	return nil
}

// WaitTurn 阻塞至当前 turn 结束（demo/同步路径用）；无 turn 立即返回。
func (o *Orchestrator) WaitTurn(ctx context.Context, conversationID string) (string, error) {
	as := o.conversation(conversationID)
	if as == nil {
		return "", acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", conversationID), nil)
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

// RunOnce 在指定 runtime 上跑一次 fresh conversation + prompt 并等待结束（forge demo 用）。
func (o *Orchestrator) RunOnce(ctx context.Context, opt RunOptions) (*RunResult, error) {
	agent, err := o.svc.requireDefaultAgent()
	if err != nil {
		return nil, err
	}
	res, err := o.startConversationOn(ctx, agent, opt.RuntimeID, opt.Cwd, runtime.ModelConfig{})
	if err != nil {
		return nil, err
	}
	if _, err := o.Prompt(ctx, res.ConversationID, opt.Prompt); err != nil {
		return nil, err
	}
	stop, err := o.WaitTurn(ctx, res.ConversationID)
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		return &RunResult{ConversationID: res.ConversationID, AgentID: agent.ID}, err
	}
	if err != nil {
		// 父 ctx 超时/取消：runTurn 已按其 err 归为 failed/cancelled
		return &RunResult{ConversationID: res.ConversationID, AgentID: agent.ID, StopReason: stop}, err
	}
	return &RunResult{ConversationID: res.ConversationID, AgentID: agent.ID, StopReason: stop}, nil
}

// conversation 按 ID 取活跃会话（连接存活期内）。查找不存在（如进程重启后）按未找到处理。
func (o *Orchestrator) conversation(id string) *ActiveConversation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.conversations[id]
}

// resumableStatus 报告会话状态是否允许按需恢复（P3 F2：in_progress / interrupted 可续聊；
// 终态会话沿用旧语义不可恢复，按未找到处理）。
func resumableStatus(status string) bool {
	return status == ConversationStatusInProgress || status == ConversationStatusInterrupted
}

// ensureConversation 返回活跃会话；不在活跃集合但持久层有可恢复记录时按需重建。
// 恢复次序（P3 F2/F3，§5.6）：
//  1. 原生 resume（首选）：acquire conn → 按能力 LoadSession（opencode load=true）或
//     ResumeSession（dsh resume=true）恢复同一 provider session → reason=restart。
//  2. handoff memo 兜底（原生不可用时）：无 load/resume 能力（echo）、无 provider session
//     （旧数据）、agent 现路由 runtime ≠ 会话原 runtime（跨 runtime 续聊）、或原生恢复失败 →
//     在该 runtime 建 fresh session + 把历史压成 bounded memo 挂到首条 prompt（reason=handoff），
//     并更新 rec 的 provider_session_id / runtime_hint（记忆与 sqlite 同步）。
//
// 终态/未知会话 → conversation_not_found（不改写旧语义）。memo 预算由 SetHandoffMaxChars 控制
// （0 = 关闭 memo 兜底 → 原生不可用时回退明确错误）。
func (o *Orchestrator) ensureConversation(ctx context.Context, conversationID string) (*ActiveConversation, error) {
	if as := o.conversation(conversationID); as != nil {
		return as, nil
	}
	rec := o.svc.conversationRec(conversationID)
	if rec == nil || !resumableStatus(rec.Status) {
		return nil, acl.NewError(acl.ClassRequest, "conversation_not_found",
			fmt.Sprintf("未知或不可恢复会话 %q", conversationID), nil)
	}
	agent, ok := o.svc.AgentByID(rec.AgentID)
	if !ok {
		// agent 已被删除/未注册（归属不可变：平台不自动改绑到别的 agent，也不静默换权限面），
		// 但错误必须给出路——同一个 id 重新注册即可复活绑定（会话记的是 agent id），
		// 或导出历史另开新会话。
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found",
			fmt.Sprintf("会话 %q 绑定的 agent %q 已不存在：用同一个 id 重新注册该 agent 后可继续，或导出本会话历史另开新会话",
				conversationID, rec.AgentID), nil)
	}
	// runtime 选择：会话原 runtime 未注册 / agent 现路由首选已迁移（跨 runtime 续聊，§18 F3）
	// → 取 agent 现路由 runtime；否则沿用 rec.RuntimeHint。
	target := rec.RuntimeHint
	if _, ok := o.reg.Get(target); !ok {
		target = ""
	}
	if resolved, err := o.svc.ResolveRuntime(agent); err == nil && resolved != "" && resolved != target {
		// agent 定义已更新/原 runtime 失联：切到现首选 runtime（fresh + memo 重灌）。
		target = resolved
	}
	spec, ok := o.reg.Get(target)
	if !ok || target == "" {
		return nil, acl.NewError(acl.ClassRequest, "runtime_not_found",
			fmt.Sprintf("runtime %q 未注册（agent %q 亦无可路由 runtime）", target, rec.AgentID), nil)
	}
	cwd := rec.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	rc, err := o.acquire(ctx, agent, spec)
	if err != nil {
		return nil, err
	}
	opts := acl.SessionOptions{Cwd: cwd, McpServers: o.svc.McpServersFor(agent, spec.ID)}
	caps := rc.client.Capabilities()
	sameRuntime := target == rec.RuntimeHint && rec.RuntimeHint != ""
	nativeID := rec.ProviderSessionID
	resumeOK := sameRuntime && nativeID != "" &&
		(caps.Raw.LoadSession || caps.Raw.SessionCapabilities.Resume != nil)

	var sess *acl.Session
	var memo string
	reason := "restart"
	switch {
	case resumeOK:
		if caps.Raw.LoadSession {
			sess, err = rc.client.LoadSession(ctx, nativeID, opts)
		} else {
			sess, err = rc.client.ResumeSession(ctx, nativeID, opts)
		}
		if err != nil {
			o.logger.Warn("原生恢复失败，转 handoff memo 兜底", "conversation", conversationID,
				"runtime", spec.ID, "provider_session", nativeID, "err", err)
			resumeOK = false
		}
	}
	if !resumeOK {
		if o.handoffMaxChars <= 0 {
			return nil, acl.NewError(acl.ClassRequest, "resume_unavailable",
				fmt.Sprintf("会话 %q 无法原生恢复（runtime %q 无 load/resume 或 provider 会话无效），且 memo 兜底关闭",
					conversationID, spec.ID), nil)
		}
		sess, err = rc.client.NewSession(ctx, opts)
		if err != nil {
			return nil, err
		}
		memo = o.svc.HandoffMemo(conversationID, o.handoffMaxChars)
		reason = "handoff"
		// 迁移登记：fresh session 顶替原 provider id；跨 runtime 时同步 runtime_hint。
		if nativeID != sess.ID() || target != rec.RuntimeHint {
			rec.ProviderSessionID = sess.ID()
			rec.RuntimeHint = spec.ID
			o.svc.registerConversation(rec)
		}
	}
	as := &ActiveConversation{ID: rec.ID, AgentID: agent.ID, Runtime: spec.ID, client: rc.client, sess: sess,
		proj: runtime.NewProjection(spec, sess), memo: memo}
	// 会话级模型在恢复后重新断言一次（平台记录是权威意图；原生恢复未必带回该状态）：
	// **best-effort** —— 恢复不是用户在选模型，运行时若已不再提供该值（换了 provider/档案），
	// 只留痕 + 标灰，不让老会话因为一个失效取值而打不开。
	if rec.Model != "" {
		if _, err := as.proj.ApplyModel(ctx, runtime.ModelConfig{Model: rec.Model}); err != nil {
			if ae, ok := acl.As(err); !ok || !strings.HasPrefix(ae.Code, "unsupported_") {
				o.svc.catalog.markRejected(agent.ID, rec.Model, modelApplyReason(err))
			}
			o.logger.Warn("恢复后重放会话模型失败（忽略，继续用运行时当前值）",
				"conversation", rec.ID, "runtime", spec.ID, "model", rec.Model, "err", err)
		}
	} else if len(as.proj.ConfigOptions()) > 0 {
		o.svc.catalog.put(agent.ID, spec.ID, as.proj.ConfigOptions())
	}
	o.mu.Lock()
	o.conversations[as.ID] = as
	o.byProvider[sess.ID()] = as.ID
	o.mu.Unlock()
	o.svc.markConversation(as.ID, ConversationStatusInProgress)
	o.svc.publish(BusSessionResumed, as.ID, map[string]any{
		"session_id": as.ID, "agent_id": as.AgentID, "runtime": spec.ID, "reason": reason,
	})
	return as, nil
}

// SetProcRegistry 注入进程注册表（serve 装配；nil = 不登记）。
func (o *Orchestrator) SetProcRegistry(r *procreg.Registry) {
	o.mu.Lock()
	o.procReg = r
	o.mu.Unlock()
}

// ProcRegistry 返回当前注册表（测试/诊断用）。
func (o *Orchestrator) ProcRegistry() *procreg.Registry {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.procReg
}

// ReapOrphans 回收上一 daemon 实例遗留的孤儿 runtime 进程（启动时调用一次）。
func (o *Orchestrator) ReapOrphans(grace time.Duration) (procreg.Report, error) {
	o.mu.Lock()
	reg := o.procReg
	o.mu.Unlock()
	if reg == nil {
		return procreg.Report{Disabled: true}, nil
	}
	return reg.Reap(grace)
}

// registerProc 登记刚拉起的子进程（失败只告警，不影响会话可用性）。
func (o *Orchestrator) registerProc(rc *runtimeConn) {
	if rc == nil || rc.client == nil {
		return
	}
	// pid 先落到连接上：诊断面（A3 /diagnostics）不依赖进程注册表是否启用。
	info := rc.client.PidInfo()
	if info.PID > 0 {
		rc.pid = info.PID
	}
	o.mu.Lock()
	reg := o.procReg
	o.mu.Unlock()
	if reg == nil || info.PID <= 0 {
		return
	}
	if err := reg.Add(procreg.Entry{
		PID: info.PID, PGID: info.PGID, Command: info.Command,
		Runtime: rc.spec.ID, AgentID: rc.agentID,
	}); err != nil {
		o.logger.Warn("procreg: 登记子进程失败", "pid", info.PID, "err", err)
		return
	}
	o.logger.Info("procreg: 登记 runtime 子进程",
		"runtime", rc.spec.ID, "agent", rc.agentID, "pid", info.PID, "pgid", info.PGID)
}

// unregisterProc 摘除登记（正常回收路径）。
func (o *Orchestrator) unregisterProc(rc *runtimeConn) {
	if rc == nil || rc.pid <= 0 {
		return
	}
	o.mu.Lock()
	reg := o.procReg
	o.mu.Unlock()
	if reg == nil {
		return
	}
	if err := reg.Remove(rc.pid); err != nil {
		o.logger.Warn("procreg: 摘除子进程登记失败", "pid", rc.pid, "err", err)
	}
}

// connKey 计算连接缓存键：runtime ID + 各 env 组签名（每组内按序 join、组间 \x00 分隔）。
// 同 runtime 且影响线程行为的 env 组合相同 → 共享暖进程；任一不同 → 各自独立进程。
// 传入组约定：nativeEnv（原生权限开关）、keyEnv（provider 密钥）、modelEnv（仅进程级模型）。
func connKey(runtimeID string, envs ...[]string) string {
	var parts []string
	for _, e := range envs {
		if len(e) == 0 {
			continue
		}
		parts = append(parts, strings.Join(e, "\x00"))
	}
	if len(parts) == 0 {
		return runtimeID
	}
	return runtimeID + "\x00" + strings.Join(parts, "\x00")
}

// acquire 取（或拨）一条 runtime 连接：活着复用，断开则作废重拨；并挂断连监听。
func (o *Orchestrator) acquire(ctx context.Context, agent *AgentRec, spec runtime.Spec) (*runtimeConn, error) {
	// 凭据面先校验（P2）：agent 绑定了 provider 档案而档案缺失/无 key 属配置错误 → 显式
	// credential_missing（文案含出路），不静默以「无密钥」姿态起进程。放 acquire 内可一处
	// 覆盖三条入口：新建会话、按需恢复、StartAgent 暖启动。
	if err := o.svc.CredentialProblem(agent); err != nil {
		return nil, err
	}
	// B5 原生权限映射：把 agent 的 permission_policy 投影为底座原生权限开关
	// （dsh: DSH_PERMISSION_MODE；opencode: OPENCODE_CONFIG_CONTENT 的 permission 表）。
	// 先算 env 再定缓存键——权限开关是进程级，配置不同的 agent 不能复用同一子进程。
	nativeEnv := o.svc.nativePermissionEnvFor(spec.ID, agent.PermissionPolicy)
	// provider 注入面（P2）：provider 档案密钥 env。并入缓存键，修复「同 runtime+权限、
	// 不同 provider 共享子进程」的潜在串键（B5 修复）。
	keyEnv := o.svc.providerEnvFor(agent.Provider)
	// 模型不进 env/缓存键：模型选择是**会话级**语义（session/set_config_option），
	// 同一连接可服务多个模型；进程级模型配置归底座自身的配置文件/环境（子进程继承 daemon 环境）。
	key := connKey(spec.ID, nativeEnv, keyEnv)

	o.mu.Lock()
	rc := o.conns[key]
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
	var env []string
	env = append(env, keyEnv...)
	env = append(env, nativeEnv...)
	logger := o.logger.With("runtime", spec.ID, "cmd", spec.Launch.Command)

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
		sid := o.busyConversationForClient(cli)
		if sid == "" {
			// SDK 未建模 elicitation scope → 无法读顶层 conversationId；连接上无唯一在途 turn
			// （空闲 / 会话外 request-scope / 多会话并发）时不做猜测，按取消回应。
			o.logger.Warn("elicitation: 连接上无唯一在途 turn，无法归属会话，按取消处理")
			return acl.ElicitationOutcome{Action: "cancel"}
		}
		req.SessionID = sid
		// C3：SDK v0.13.5 未建模 elicitation scope → 归属只能推断，来源显式标注（REST/WS 可观测）。
		if req.ScopeSource == "" {
			req.ScopeSource = acl.ScopeInferred
		}
		return o.requestElicitation(ctx, req)
	}
	// 拨号用「脱离调用方 ctx」的上下文：子进程以 exec.CommandContext 启动，若绑定请求 ctx，
	// 请求返回即被杀（如 P2 StartAgent 暖启动后立刻断连）。WithoutCancel 后 Dial 自带的
	// 默认握手限时仍保证拨号有界；连接生命周期归编排器（watchConn/Stop/Close），与任一请求无关。
	// spawn + 握手由 runtime.Connect 承载（可执行投影的拨号底座，语义与此前内联实现一致）。
	dialCtx := context.WithoutCancel(ctx)
	client, err := runtime.Connect(dialCtx, spec, args, env, acl.ClientOptions{
		OnEvent:           o.eventSink(agent.ID, spec.ID),
		Logger:            logger,
		RequestPermission: reqPerm,
		CreateElicitation: reqElic,
	})
	if err != nil {
		return nil, err
	}
	cli = client
	// §16.6/§17.4 开放项实测：握手能力断言留档日志（观测用，无行为变化）——
	// 底座调研/诊断依赖（sessionCapabilities resume/list/fork/close、loadSession 声明）。
	// INFO 级：每 runtime 连接仅一次，serve 默认级别可见。
	caps := client.Capabilities()
	sc := caps.Raw.SessionCapabilities
	o.logger.Info("runtime 握手能力",
		"runtime", spec.ID,
		"sessions_persistent", caps.SessionsPersistent,
		"multi_session_concurrent", caps.MultiSessionConcurrent,
		"load_session", caps.Raw.LoadSession,
		"resume", sc.Resume != nil,
		"list", sc.List != nil,
		"fork", sc.Fork != nil,
		"close", sc.Close != nil)

	o.svc.setAgentState(agent.ID, AgentStateRunning)
	rc = &runtimeConn{client: client, agentID: agent.ID, spec: spec, key: key, started: time.Now()}
	o.registerProc(rc)
	o.mu.Lock()
	o.conns[key] = rc
	o.mu.Unlock()
	go o.watchConn(rc)
	return rc, nil
}

// markBusy / unmarkBusy 委托 inFlightTracker（§17 E1 显式化簿记；语义不变）。
// client 为 nil（理论上不应出现）或未登记时均安全 no-op。
func (o *Orchestrator) markBusy(client *acl.Client, conversationID string) {
	o.inflight.Mark(client, conversationID)
}

func (o *Orchestrator) unmarkBusy(client *acl.Client, conversationID string) {
	o.inflight.Unmark(client, conversationID)
}

// busyConversationForClient 返回该连接上「唯一在途」的会话 id；无在途或多会话并发在途返回空串。
func (o *Orchestrator) busyConversationForClient(client *acl.Client) string {
	return o.inflight.SoleOwner(client)
}

// applyModelOnSession 把模型应用到一条会话上（建会话与「会话内改模型」共用同一路径）。
//
// 语义（决策记录）：**用户显式指定的取值，运行时要么接受、要么报错**——绝不静默回落默认模型：
//   - 运行时未广告模型项（echo / 未配置 provider 的 opencode）→ model_not_supported；
//   - 运行时拒绝该取值（协议要求值必须来自广告列表）→ 原样上抛并记入「不可用」反馈；
//   - 成功 → 用返回的全量状态刷新平台目录缓存。
//
// model.Model 为空 = 不下发设置（跟随运行时默认），永远成功。
// 思考档位不在平台侧下发（见 runtime.ModelConfig 说明）。
func (o *Orchestrator) applyModelOnSession(ctx context.Context, as *ActiveConversation,
	agentID string, spec runtime.Spec, sess acpSession, model runtime.ModelConfig) error {
	if as.proj == nil { // 测试注入的假会话（无投影）：无模型语义
		return nil
	}
	if model.Model == "" {
		return nil // 未指定模型：不下发任何设置（跟随运行时默认）
	}
	// 不预判、但留痕：取值不在会话当前广告列表里（缓存滞后/跨会话选择）时记一条，仍然下发由
	// 运行时裁决——协议保证运行时只接受已广告的值，平台侧预判会因快照滞后造成假阴性。
	if !acl.HasConfigValue(as.proj.ConfigOptions(), acl.CategoryModel, model.Model) {
		o.logger.Debug("模型取值不在当前广告列表中，交由运行时裁决",
			"conversation", as.ID, "model", model.Model)
	}
	applied, err := as.proj.ApplyModel(ctx, model)
	if err == nil {
		o.svc.catalog.clearRejection(agentID, model.Model)
		if len(applied) > 0 {
			o.svc.catalog.put(agentID, spec.ID, applied)
		}
		return nil
	}
	ae, _ := acl.As(err)
	if ae != nil && ae.Code == "unsupported_model" {
		return acl.NewError(acl.ClassRequest, "model_not_supported",
			"[model_not_supported] runtime "+spec.ID+" 未提供模型配置项，无法应用所选模型", nil)
	}
	// 运行时明确拒绝：记录该取值不可用（目录里标灰）后原样上抛。
	o.svc.catalog.markRejected(agentID, model.Model, modelApplyReason(err))
	slog.Default().Warn("apply model rejected by runtime",
		"conversation", as.ID, "runtime", spec.ID, "model", model.Model, "err", err)
	return err
}

// liveConfigOptions 返回该 agent 任一活跃会话的配置项（模型目录的**首选来源**：运行时此刻
// 的真实状态，零额外开销）。无活跃会话返回 false。
//
// 注：有活跃会话但配置项为空时同样返回 ok=true（空即运行时的真实答案：未配 provider/未登录），
// 不走 resume 兜底去「凑一份」目录。
func (o *Orchestrator) liveConfigOptions(agentID string) ([]acl.ConfigOption, bool) {
	o.mu.Lock()
	var sess acpSession
	for _, as := range o.conversations {
		if as.AgentID != agentID || as.dead || as.sess == nil {
			continue
		}
		sess = as.sess
		break
	}
	o.mu.Unlock()
	if sess == nil {
		return nil, false
	}
	return sess.ConfigOptions(), true
}

// connForAgent 返回该 agent 已建立的 runtime 连接（无 → nil）。用于读取握手上冻结的能力
// 断言（如鉴权引导）：不主动拨号，避免「查一次目录就起一个进程」。
func (o *Orchestrator) connForAgent(agentID string) *runtimeConn {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, rc := range o.conns {
		if rc.agentID == agentID {
			select {
			case <-rc.client.Done():
				continue // 已断开：视为无连接
			default:
				return rc
			}
		}
	}
	return nil
}

// resolveProvider 把原生 provider session id 解析为平台会话 uuid（P3 双 id）。
// 底层回调（事件/权限请求）只携带原生 id；未登记（如断连后迟到事件）返回空串。
func (o *Orchestrator) resolveProvider(providerID string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.byProvider[providerID]
}

// unregisterConversationProvider 移除某会话的原生→平台映射（连接失活/会话清理时调用）。
func (o *Orchestrator) unregisterConversationProvider(conversationID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for k, v := range o.byProvider {
		if v == conversationID {
			delete(o.byProvider, k)
		}
	}
}

// DetachAgent 摘除某 agent 的全部活跃会话（agent 被删除时由 Service.DeleteAgent 调用）。
//
// 语义（P2 删除面）：归属不可变——平台不改写会话的 agent_id，也不静默改绑到别的 agent；
// 但删除必须**立刻**生效：把内存里仍活着的会话摘出登记，使后续 prompt 走
// ensureConversation → agent_not_found（并带上「同 id 重建 / 导出历史」的出路），而不是让它在
// 半死连接上一直跑到进程重启。
//
// 有在途 turn 时整体拒绝（agent_busy，先 cancel/等收尾）：不删到一半的轮次，此时不动任何登记。
// 返回被摘除的会话数；底层 ACP 会话不在此关闭（连接可能被同 runtime 的其他 agent 复用，
// 会话生命周期归底座自身与空闲回收）。
func (o *Orchestrator) DetachAgent(agentID string) (int, error) {
	if agentID == "" {
		return 0, nil
	}
	o.mu.Lock()
	victims := make([]*ActiveConversation, 0, 4)
	for _, as := range o.conversations {
		if as.AgentID == agentID {
			victims = append(victims, as)
		}
	}
	o.mu.Unlock()
	if len(victims) == 0 {
		return 0, nil
	}
	// 在途判定在 o.mu 之外做（as.mu 与 o.mu 不同级，避免交错持锁）。
	for _, as := range victims {
		as.mu.Lock()
		busy := as.busy || as.turn != nil
		as.mu.Unlock()
		if busy {
			return 0, acl.NewError(acl.ClassRequest, "agent_busy",
				fmt.Sprintf("agent %q 的会话 %q 有在途 turn，先取消/等待收尾再删除", agentID, as.ID), nil)
		}
	}
	o.mu.Lock()
	detached := 0
	for _, as := range victims {
		if o.conversations[as.ID] == as {
			delete(o.conversations, as.ID)
			detached++
		}
	}
	o.mu.Unlock()
	for _, as := range victims {
		// 原生 → 平台映射一并清除：迟到事件不再路由回已摘除的会话。
		o.unregisterConversationProvider(as.ID)
	}
	o.logger.Info("agent 已删除：活跃会话已摘除（后续 prompt 报 agent_not_found）",
		"agent", agentID, "detached", detached)
	return detached, nil
}

// watchConn 监听连接断开：清理注册、失活其上会话、标记 agent stopped。
func (o *Orchestrator) watchConn(rc *runtimeConn) {
	<-rc.client.Done()
	o.dropConn(rc)
}

func (o *Orchestrator) dropConn(rc *runtimeConn) {
	o.unregisterProc(rc)
	o.mu.Lock()
	ownerBefore := o.conns[rc.key] == rc
	if ownerBefore {
		delete(o.conns, rc.key)
	}
	o.inflight.Clear(rc.client) // 连接断开：在途登记一并作废（runTurn 的 unmark 对已清键为 no-op）
	var victims []*ActiveConversation
	for _, as := range o.conversations {
		if as.client == rc.client {
			victims = append(victims, as)
		}
	}
	o.mu.Unlock()
	for _, as := range victims {
		o.unregisterConversationProvider(as.ID) // 连接断开：原生→平台映射一并清除（迟到事件丢弃）
	}
	for _, as := range victims {
		o.disconnectConversation(as)
	}
	// 状态回落仅当仍是该 runtime 的当前连接（旧连接收尾不得覆盖新连接的 running——
	// 空闲回收后立刻被 StartAgent/会话按需重拨的场景）。
	if ownerBefore {
		o.svc.setAgentState(rc.agentID, AgentStateStopped)
	}
}

// disconnectConversation 把某断连连接上的会话失活：进行中 turn 解除阻塞（runTurn 依 err 收尾），
// 空闲会话直接标记 failed 并发布 connection_lost（幂等）。
func (o *Orchestrator) disconnectConversation(as *ActiveConversation) {
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
			o.svc.markConversation(as.ID, ConversationStatusFailed)
			ae := acl.NewError(acl.ClassConnection, "connection_lost",
				"agent 连接断开（会话失活；是否可 resume 依 runtime 能力）", nil)
			var tail []string
			if as.client != nil {
				tail = acl.TailLines(as.client.StderrTail(), maxDiagStderrLines)
			}
			ae.StderrTail = tail
			o.svc.recordAgentError(as.AgentID, as.ID, ae, tail)
			payload := map[string]any{
				"session_id": as.ID, "code": ae.Code, "class": string(ae.Class), "details": ae.Details,
			}
			if len(tail) > 0 {
				payload["stderr_tail"] = tail
			}
			o.svc.publish(BusTurnError, as.ID, payload)
		}
	}
}

// eventSink 把 acl 归一化事件转为域事件进 eventbus（agg = 平台会话 uuid）。
func (o *Orchestrator) eventSink(agentID, runtimeID string) func(acl.Event) {
	return func(ev acl.Event) {
		if ev.SessionID == "" {
			return
		}
		// P3 双 id：底层事件只携带原生 session id，host 侧解析为平台会话 uuid 再发布；
		// 断连后迟到事件（映射已清）不再落入孤儿聚合，丢弃并留 debug。
		cid := o.resolveProvider(ev.SessionID)
		if cid == "" {
			o.logger.Debug("event 无对应平台会话（映射已清，丢弃迟到事件）", "provider_session", ev.SessionID)
			return
		}
		ev.SessionID = cid
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
			// 用户消息以宿主侧 Prompt 提交时发布的 BusUserMessage（orchestrator.Prompt）为唯一来源；
			// agent 回传的 user_message 只是会话镜像（normalizeUpdate 归一），若再转发会造成
			// 同一条用户输入在流/历史里双入。这里丢弃镜像，不再发布。
			o.logger.Debug("skip agent-echoed user_message (host publishes user input)", "conversation", ev.SessionID)
		case acl.KindUsage:
			typ = BusUsage
			// ev.SessionID 已在上方解析为平台会话 uuid（cid）。
			p := map[string]any{"session_id": ev.SessionID, "agent_id": agentID}
			if d, ok := ev.Payload.(acl.UsageInfo); ok {
				p["used"], p["size"] = d.Used, d.Size
				if d.CostAmount > 0 {
					p["cost_amount"], p["cost_currency"] = d.CostAmount, d.CostCurrency
				}
				o.recordUsage(ev.SessionID, d)
			}
			payload = p
		case acl.KindPermission:
			typ = BusPermission
			payload = ev.Payload
		case acl.KindConfigOptions:
			// 运行时主动推送全量配置项（dsh 在 provider 拓扑变化时推）：
			// 刷新平台目录缓存并转发给前端，模型下拉与「当前模型」无需轮询即可保持一致。
			if d, ok := ev.Payload.(acl.ConfigOptionsInfo); ok {
				if d.SessionID != "" {
					o.svc.catalog.put(agentID, runtimeID, d.Options)
				}
				typ = BusConfigOptions
				payload = map[string]any{
					"session_id": ev.SessionID,
					"agent_id":   agentID,
					"runtime":    runtimeID,
					"options":    d.Options,
				}
			}
		}
		if typ != "" && payload != nil {
			o.svc.publish(typ, ev.SessionID, payload)
		}
	}
}

// toolPayload 构造工具事件载荷：稳定键 tool_call_id/title/status，可选透传 kind/input/output
// （ACP ToolKind/rawInput/rawOutput，决策 v5②；字段为空/缺省时不出现在载荷中）。
func toolPayload(conversationID, agentID string, d acl.ToolCallInfo) map[string]any {
	p := map[string]any{
		"session_id": conversationID, "agent_id": agentID,
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
//   - 否则发布 conversation.permission_request 事件（含 call_id/options）并阻塞等待
//     PermissionsResolve（allow/reject × once/always）；超时或 ctx 取消自动拒绝。
//
// 事件发布（publish）注入的给 PermissionManager，避免 manager 依赖 bus。
func (o *Orchestrator) requestPermission(ctx context.Context, req acl.PermissionRequest) acl.PermissionDecision {
	// P3 双 id：权限请求只携带原生 session id → 解析为平台会话 uuid 键控（记忆/挂起/事件
	// 均以平台会话 id 为键，与 conversations/events/permission_memories 一致）。
	sid := o.resolveProvider(req.SessionID)
	if sid == "" {
		sid = req.SessionID // 兜底回落原生（理论上不应出现；保留事件不丢）
	}
	// M2 #7 per-agent 默认权限策略：运维护栏优先于会话级记忆（策略 allow/deny 直接决策，
	// 不经用户；审计事件 session.permission_policy 留痕）。ask/未声明 → 回落既有路径。
	if action, agentID, ok := o.policyAction(sid, req); ok {
		if d, dok := autoPermissionDecision(action, req.Options); dok {
			o.svc.publish(BusPermissionPolicy, sid, map[string]any{
				"session_id": sid, "agent_id": agentID, "action": action,
				"title": req.ToolCall.Title, "kind": req.ToolCall.Kind,
				"allowed": d.Allowed, "rejected": d.Rejected, "option_id": d.OptionID,
			})
			o.logger.Info("permission: auto-decided by agent policy", "conversation", sid,
				"agent", agentID, "action", action, "title", req.ToolCall.Title, "kind", req.ToolCall.Kind)
			return d
		}
		o.logger.Warn("permission: agent policy action has no matching option, fallback to ask",
			"conversation", sid, "agent", agentID, "action", action, "options", len(req.Options))
	}
	if d, ok := o.perm.AutoDecision(sid, req); ok {
		return d
	}
	publish := func(callID, title string, tool acl.ToolCallInfo, opts []acl.PermissionOption, timeout time.Time) {
		o.svc.publish(BusPermissionRequest, sid, map[string]any{
			"session_id":   sid,
			"call_id":      callID,
			"title":        title,
			"tool_call_id": tool.ID,
			"kind":         tool.Kind,
			"input":        tool.Input,
			"options":      opts,
			"timeout_at":   timeout.UTC().Format(time.RFC3339), // UI 端超时收敛（无用户决策时自动拒绝）
		})
	}
	return o.perm.RegisterAndWait(ctx, sid, req, publish)
}

// recordUsage 记录平台会话最近一次用量快照（同会话后到覆盖先到；空快照忽略）。
func (o *Orchestrator) recordUsage(conversationID string, u acl.UsageInfo) {
	if conversationID == "" || !u.Reported() {
		return
	}
	o.usageMu.Lock()
	o.usage[conversationID] = u
	o.usageMu.Unlock()
}

// LatestUsage 取会话最近一次用量快照（无上报 → false）。turn 收口落库用。
func (o *Orchestrator) LatestUsage(conversationID string) (acl.UsageInfo, bool) {
	o.usageMu.Lock()
	defer o.usageMu.Unlock()
	u, ok := o.usage[conversationID]
	return u, ok
}

// PendingPermissions 列出某会话的挂起权限请求（attach/snapshot 供 UI 重建）。
func (o *Orchestrator) PendingPermissions(conversationID string) []PermissionItem {
	ps := o.perm.pendingForSession(conversationID)
	out := make([]PermissionItem, 0, len(ps))
	for _, p := range ps {
		out = append(out, PermissionItem{
			CallID:    p.callID,
			SessionID: p.conversationID,
			Title:     p.title,
			ToolCall:  p.toolCall,
			Options:   p.options,
			TimeoutAt: p.timeout,
		})
	}
	return out
}

// ResolvePermission 按 call_id 投递决策（allow/reject × once/always），并返回**实际回执**
// （ACP 形态 selected/cancelled + 有效 option_id；规则见 acl.EffectivePermissionOutcome）。
// 拒绝分支不再折叠：optionID（所选 deny 选项）与 always 标记透传给 PermissionManager，
// ACP 层对显式拒绝回 Selected{deny 选项}，agent 可区分「拒绝」与「取消/超时」。
func (o *Orchestrator) ResolvePermission(callID, optionID string, always, allowed bool) (PermissionDecisionOutcome, bool, error) {
	if callID == "" {
		return PermissionDecisionOutcome{}, false, acl.NewError(acl.ClassRequest, "call_id_required", "call_id 必填", nil)
	}
	d := acl.PermissionDecision{Allowed: allowed, Rejected: !allowed, OptionID: optionID, Always: always}
	found, out := o.perm.ResolveDecision(callID, d)
	if !found {
		return PermissionDecisionOutcome{}, false, nil
	}
	return PermissionDecisionOutcome{
		Allowed:  d.Allowed,
		Rejected: d.Rejected,
		Always:   d.Always,
		OptionID: optionID,
		Kind:     out.Kind,
		Selected: out.OptionID,
	}, true, nil
}

// PermissionDecisionOutcome 是权限决策的对外回执（REST 响应字段来源；不含任何请求参数回显猜测）。
type PermissionDecisionOutcome struct {
	Allowed  bool   // 决策是否放行
	Rejected bool   // 决策是否显式拒绝
	Always   bool   // 是否要求写入会话级记忆
	OptionID string // 请求里指定的 option_id（原样回带，便于前端核对）
	Kind     string // 实际 ACP 回执形态：selected | cancelled
	Selected string // 实际选中的 option_id（Kind=cancelled 时为空）
}

// PermissionMemories 列出某会话的会话级权限记忆（allow/reject 双侧；供详情/撤销 UI）。
func (o *Orchestrator) PermissionMemories(conversationID string) []MemoryItem {
	return o.perm.List(conversationID)
}

// RevokeMemory 撤销会话级权限记忆。
//   - title、kind 均非空 → 撤销单条（精确匹配 action+identifier）；
//   - 两者均为空 → 一键撤销整会话全部记忆（= ForgetConversation）。
func (o *Orchestrator) RevokeMemory(conversationID, title, kind string) (bool, error) {
	switch {
	case title == "" && kind == "":
		o.perm.ForgetSession(conversationID)
		return true, nil
	case title == "" || kind == "":
		return false, acl.NewError(acl.ClassRequest, "memory_revoke_params", "撤销单条需 title 与 kind 同时提供；两者都空则撤销全部", nil)
	}
	return o.perm.Revoke(conversationID, title, kind), nil
}

// requestElicitation 处理一次 ACP 结构化提问请求（在 SDK 派发的 goroutine 中阻塞等待用户决策）：
//   - 发布 conversation.elicitation_request 事件（含 elicitation_id/mode/message/schema|url/timeout_at）
//     并阻塞等待 ElicitationResolve（accept/decline/cancel）；超时或 ctx 取消自动按取消处理。
//
// 事件发布（publish）注入给 ElicitationManager，避免 manager 依赖 bus。
func (o *Orchestrator) requestElicitation(ctx context.Context, req acl.ElicitationRequest) acl.ElicitationOutcome {
	sid := req.SessionID // 断点恢复别名归一
	publish := func(it ElicitationItem) {
		payload := map[string]any{
			"session_id":      sid,
			"conversation_id": sid,
			"scope_source":    it.ScopeSource,
			"elicitation_id":  it.ID,
			"mode":            it.Mode,
			"message":         it.Message,
			"timeout_at":      it.TimeoutAt.UTC().Format(time.RFC3339),
		}
		if it.Mode == "form" {
			payload["schema"] = it.Schema
		} else {
			payload["url"] = it.URL
		}
		o.svc.publish(BusElicitationRequest, sid, payload)
	}
	return o.elic.RegisterAndWait(ctx, sid, req, publish)
}

// PendingElicitations 列出某会话的挂起结构化提问（attach/snapshot 供 UI 重建）。
func (o *Orchestrator) PendingElicitations(conversationID string) []ElicitationItem {
	ps := o.elic.pendingForSession(conversationID)
	out := make([]ElicitationItem, 0, len(ps))
	for _, p := range ps {
		out = append(out, ElicitationItem{
			ID:             p.id,
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
	return out
}

// ResolveElicitation 按 elicitation_id 投递决策（accept/decline/cancel；accept 可带 content）。
// conversationID 是决策发起方会话：C3 起做 scope 校验，只有归属会话能决策（跨会话 → not found）。
func (o *Orchestrator) ResolveElicitation(conversationID, elicitationID, action string, content map[string]any) (bool, error) {
	if elicitationID == "" {
		return false, acl.NewError(acl.ClassRequest, "elicitation_id_required", "elicitation_id 必填", nil)
	}
	var found, mismatch bool
	switch action {
	case "accept":
		found, mismatch = o.elic.ResolveAccept(conversationID, elicitationID, content)
	case "decline":
		found, mismatch = o.elic.ResolveDecline(conversationID, elicitationID)
	case "cancel":
		found, mismatch = o.elic.ResolveCancel(conversationID, elicitationID)
	default:
		return false, acl.NewError(acl.ClassRequest, "elicitation_action", "action 需为 accept/decline/cancel 之一", nil)
	}
	if mismatch {
		// 归属不符：对外与「不存在」同表现（不泄露其它会话的挂起项），细节已在 manager 侧告警。
		return false, acl.NewError(acl.ClassRequest, "elicitation_not_found",
			fmt.Sprintf("结构化提问 %q 不存在或已过期", elicitationID), nil)
	}
	return found, nil
}

// publishError 把错误按 acl 分类发布为 turn.error（A3：附上 stderr 诊断尾巴，前端错误卡可展开）。
func (o *Orchestrator) publishError(conversationID string, err error, cli *acl.Client) {
	ae := acl.Classify(err)
	tail := ae.StderrTail
	if len(tail) == 0 && cli != nil {
		tail = acl.TailLines(cli.StderrTail(), maxDiagStderrLines)
	}
	o.svc.recordAgentError("", conversationID, ae, tail)
	payload := map[string]any{
		"session_id": conversationID,
		"code":       ae.Code,
		"class":      string(ae.Class),
		"details":    ae.Details,
	}
	if len(tail) > 0 {
		payload["stderr_tail"] = tail
	}
	o.svc.publish(BusTurnError, conversationID, payload)
}

// Close 关闭全部连接并清理（daemon 退出/测试收尾）。
func (o *Orchestrator) Close() {
	o.mu.Lock()
	if o.reapStop != nil {
		o.reapStop()
		o.reapStop = nil
	}
	o.mu.Unlock()
	o.mu.Lock()
	conns := make([]*runtimeConn, 0, len(o.conns))
	for _, rc := range o.conns {
		conns = append(conns, rc)
	}
	o.conns = make(map[string]*runtimeConn)
	o.mu.Unlock()
	for _, rc := range conns {
		o.disconnectConversationAll(rc)
		o.svc.setAgentState(rc.agentID, AgentStateStopped)
		o.unregisterProc(rc)
		if rc.client != nil {
			_ = rc.client.Close()
		}
	}
	o.svc.closeTranscripts()
}

func (o *Orchestrator) disconnectConversationAll(rc *runtimeConn) {
	o.mu.Lock()
	var victims []*ActiveConversation
	for _, as := range o.conversations {
		if as.client == rc.client {
			victims = append(victims, as)
		}
	}
	o.mu.Unlock()
	for _, as := range victims {
		o.disconnectConversation(as)
	}
}

// reapIdleOnce 空闲回收单轮：扫全部连接，回收「无在途 turn、无任何会话引用、且自建立起空闲
// 超过 idle」的暖进程（P2 StartAgent 常驻类）；锁内判定 + dropConn 原子摘除 → 锁外停进程，
// 下次会话/StartAgent 经 acquire 按需拉起（与 P1 断点恢复合流：连接丢失后 prompt 自动重建）。
// 返回回收数量。
func (o *Orchestrator) reapIdleOnce(now time.Time, idle time.Duration) int {
	o.mu.Lock()
	var victims []*runtimeConn
	for _, rc := range o.conns {
		if rc.started.IsZero() || now.Sub(rc.started) < idle {
			continue
		}
		if o.inflight.Len(rc.client) > 0 {
			continue // 有在途 turn，不回收
		}
		hasConversation := false
		for _, as := range o.conversations {
			if as.client == rc.client {
				hasConversation = true
				break
			}
		}
		if hasConversation {
			continue // 仍有会话引用（含已完成态），不回收（回收语义只覆盖暖进程）
		}
		victims = append(victims, rc)
	}
	o.mu.Unlock()
	for _, rc := range victims {
		o.dropConn(rc) // 原子摘除 + 状态回落（ownerBefore 保护新连接 running）
		if rc.client != nil {
			_ = rc.client.Close() // watchConn 收尾（dropConn 幂等）
		}
	}
	return len(victims)
}

// StartIdleReaper 启动空闲回收 goroutine（每 interval 扫一次，idle 为闲置阈值；
// <=0 任一 → 不启动）。Close 时停止。
func (o *Orchestrator) StartIdleReaper(interval, idle time.Duration) {
	if interval <= 0 || idle <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.mu.Lock()
	if o.reapStop != nil {
		o.mu.Unlock()
		cancel()
		return // 已启动（幂等）
	}
	o.reapStop = cancel
	o.mu.Unlock()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if n := o.reapIdleOnce(now, idle); n > 0 {
					slog.Info("orchestrator: 空闲回收", "reaped", n, "idle", idle.String())
				}
			}
		}
	}()
}

// stopAgent 停止某 agent 对应 runtime 的进程（P2 启停）：
// 无连接 → 幂等置 stopped；有在途 turn → agent_busy 拒绝（先 cancel/等收尾）；
// 空闲 → 关连接（watchConn/dropConn 清 conversations 与状态；下次会话自动按需重拨）。
func (o *Orchestrator) stopAgent(ctx context.Context, agent *AgentRec, spec runtime.Spec) error {
	key := connKey(spec.ID, o.svc.nativePermissionEnvFor(spec.ID, agent.PermissionPolicy), o.svc.providerEnvFor(agent.Provider))
	o.mu.Lock()
	rc := o.conns[key]
	busy := 0
	if rc != nil {
		busy = o.inflight.Len(rc.client)
	}
	o.mu.Unlock()
	if rc == nil {
		o.svc.setAgentState(agent.ID, AgentStateStopped)
		return nil
	}
	if busy > 0 {
		return acl.NewError(acl.ClassRequest, "agent_busy",
			fmt.Sprintf("agent %q 的 runtime %q 有 %d 个在途 turn，先取消/等待收尾", agent.ID, spec.ID, busy), nil)
	}
	if err := rc.client.Close(); err != nil {
		return err
	}
	// watchConn → dropConn 会清 conversations 并置 stopped；这里等它跑完再返回。
	select {
	case <-rc.client.Done():
	case <-ctx.Done():
		return ctx.Err()
	}
	o.svc.setAgentState(agent.ID, AgentStateStopped)
	return nil
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
