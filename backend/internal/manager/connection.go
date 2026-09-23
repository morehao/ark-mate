package manager

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// connKey 计算**基础**连接缓存键：runtime ID + 各 env 组签名（每组内按序 join、组间 \x00 分隔）。
// 同 runtime 且影响线程行为的 env 组合相同 → 共享暖进程；任一不同 → 各自独立进程。
// 传入组约定：nativeEnv（原生权限开关）、keyEnv（provider 密钥）、modelEnv（仅进程级模型）。
//
// **这不是代际键**：代际键 = 本键 + 能力指纹，见 generationKey。保留本函数原样是因为
// "配置相同则共享暖进程"这条既有规则要**逐字**保留（R11）。
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

// generationKey 在基础连接键之上追加**代际维度**（能力指纹）：指纹相同 = 同一套能力 = 同一个
// 代际 = 复用同一个暖进程；指纹不同 = 各起一个进程（能力集与投影根都是进程级的，混用等于把
// A 会话的 skill 投给 B 会话）。
//
// **指纹为空 → 原样返回基础键**：这是 R11（缺省零回归）的落点——未声明能力选择的会话产生的键
// 必须与代际机制之前**逐字相同**，同配置的两个会话才仍共享一个暖进程。
//
// 非空时用**带长度前缀**的形态而不是简单拼后缀：`connKey("r", envA)` 是
// `connKey("r", envA, envB)` 的字符串前缀（env 组之间就是 `\x00` 分隔），拼后缀会让"某个基础键
// 恰好是另一个基础键的前缀"与"代际后缀"互相串味。长度前缀让基础键自我定界。
func generationKey(connKey, fingerprint string) string {
	if fingerprint == "" {
		return connKey
	}
	return "gen:" + strconv.Itoa(len(connKey)) + ":" + connKey + ":" + fingerprint
}

// generationKeysFor 某**基础键**对应的全部代际缓存键（含基础键本身，可能为空）。
//
// 停机/停 agent 要关掉该 agent 的**所有**代际，而不是只关"当前算出来的那一个键"——代际是内容
// 寻址的，同一 agent 的旧代际可能仍然活着。匹配用 `rc.baseKey`（连接自己记着它由哪个基础键
// 派生），不做键字符串的切分：键怎么构造、怎么匹配只在一个地方说话。
func (o *Orchestrator) generationKeysFor(base string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, 2)
	for k, rc := range o.conns {
		if rc.baseKey == base {
			out = append(out, k)
		}
	}
	sort.Strings(out) // 遍历顺序稳定（关闭顺序与日志可复现）
	return out
}

// acquire 取（或拨）一条 runtime 连接：活着复用，断开则作废重拨；并挂断连监听。
//
// fingerprint = 该会话钉住的能力指纹（M1 恒为空：显式能力选择是 M3 的面）。空值走的是
// generationKey 的原样返回分支，所以缺省路径与"代际机制之前"完全等价。
func (o *Orchestrator) acquire(ctx context.Context, agent *AgentRec, spec runtime.Spec, fingerprint string) (*runtimeConn, error) {
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
	baseKey := connKey(spec.ID, nativeEnv, keyEnv)
	key := generationKey(baseKey, fingerprint)

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
		sid, source := o.resolveElicitationScope(cli, req)
		if sid == "" {
			// 既无 `_meta` scope 也无唯一在途 turn（空闲 / 会话外 request-scope / 多会话并发）→ 不猜。
			// 仍按取消回应（不能把 agent 挂死），但置 ScopeAmbiguous 并留痕（agent 诊断 + 日志），
			// 让「提问卡为什么没出现」可查，而不是无痕消失。
			o.logger.Warn("elicitation: 无法归属会话（无 _meta scope 且连接上无唯一在途 turn），按取消处理",
				"scope_source", source)
			o.svc.noteScopeAmbiguous(agent.ID,
				"elicitation 无法归属会话（wire `_meta` 未给 sessionId，且连接上无唯一在途 turn）")
			return acl.ElicitationOutcome{Action: "cancel"}
		}
		req.SessionID, req.ScopeSource = sid, source
		return o.requestElicitation(ctx, req)
	}
	// elicitation/complete（URL 模式收尾通知）：agent 告知提问已在 agent 侧完成 → 宿主按 ACP 侧
	// elicitationId 收敛挂起项。归属用「同连接的唯一在途 turn」校验（可判定时）；不可判定时不阻断
	// ——该 id 由 agent 生成、不可猜，且 complete 只是收敛通知（重复/迟到都不该有副作用）。
	completeElic := func(_ context.Context, elicitationID string) {
		o.elic.CompleteByURLID(o.busyConversationForClient(cli), elicitationID)
	}
	// 拨号用「脱离调用方 ctx」的上下文：子进程以 exec.CommandContext 启动，若绑定请求 ctx，
	// 请求返回即被杀（如 P2 StartAgent 暖启动后立刻断连）。WithoutCancel 后 Dial 自带的
	// 默认握手限时仍保证拨号有界；连接生命周期归编排器（watchConn/Stop/Close），与任一请求无关。
	// spawn + 握手由 runtime.Connect 承载（可执行投影的拨号底座，语义与此前内联实现一致）。
	dialCtx := context.WithoutCancel(ctx)
	client, err := runtime.Connect(dialCtx, spec, args, env, acl.ClientOptions{
		OnEvent:             o.eventSink(agent.ID, spec.ID),
		Logger:              logger,
		RequestPermission:   reqPerm,
		CreateElicitation:   reqElic,
		CompleteElicitation: completeElic,
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
	rc = &runtimeConn{client: client, agentID: agent.ID, spec: spec, key: key, baseKey: baseKey, started: time.Now()}
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
			return 0, acl.NewError(acl.ClassLimit, "agent_busy",
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
	// 拆除原因由拆除方在关闭连接前置位（Close/stopAgent/reapIdleOnce = 主动；watchConn 观察到
	// 对端结束 = 意外）。它决定会话状态怎么收尾——见 disconnectConversation。
	intentional := rc.intentional.Load()
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
		o.disconnectConversation(as, intentional)
	}
	// 状态回落仅当仍是该 runtime 的当前连接（旧连接收尾不得覆盖新连接的 running——
	// 空闲回收后立刻被 StartAgent/会话按需重拨的场景）。
	if ownerBefore {
		o.svc.setAgentState(rc.agentID, AgentStateStopped)
	}
	if len(victims) > 0 {
		o.logger.Info("连接拆除：会话句柄作废（会话保留，下次 prompt 按需重建）",
			"runtime", rc.spec.ID, "agent", rc.agentID,
			"cause", connCauseLabel(intentional), "conversations", len(victims))
	}
}

// disconnectConversation 把某断连连接上的会话句柄作废并摘出活跃集合。
//
// 本函数是「连接生命周期」写会话状态的**唯一入口**，因此必须区分拆除原因——把停机当成故障，
// 会让上一轮已 completed 的会话在每次重启后被改写成 failed（并往历史里追加假的 turn.error）：
//
//   - 在途 turn：主动拆除（daemon 停机 / 显式停 agent）→ 收尾归 interrupted（无终态、可续聊
//     重发，与崩溃恢复同义，不发错误事件）；意外断开 → 解除阻塞，由 runTurn 按错误收尾。
//   - 空闲会话：**不改写状态**。上一轮的终态是轮次结果的事实，连接掉了并不改变它；意外断开
//     只落 agent 级诊断（last_error + stderr 尾巴）与日志，不伪造会话失败。
//
// 会话记录本身（svc/conversations 与 store）不动：执行可丢弃、会话平台拥有（ADR-4），
// 下次 prompt 经 ensureConversation 走 native resume / handoff memo 重建句柄。
func (o *Orchestrator) disconnectConversation(as *ActiveConversation, intentional bool) {
	as.mu.Lock()
	if as.dead {
		as.mu.Unlock()
		return
	}
	as.dead = true
	t := as.turn
	if t != nil && intentional {
		t.intentional = true // 主动拆除：收尾归 interrupted，不按失败/用户取消处理
	}
	as.mu.Unlock()
	o.evictConversation(as)

	// 连接没了，挂起的权限/提问永远等不到决策：随拆除一并收敛，不留「待确认」幽灵卡。
	o.perm.CancelConversation(as.ID)
	o.elic.CancelConversation(as.ID)

	if t != nil {
		t.cancel()
		return
	}
	if !intentional {
		ae := acl.NewError(acl.ClassConnection, "connection_lost",
			"agent 连接断开（会话句柄已作废；下次 prompt 按需重建）", nil)
		var tail []string
		if as.client != nil {
			tail = acl.TailLines(as.client.StderrTail(), maxDiagStderrLines)
		}
		ae.StderrTail = tail
		o.svc.recordAgentError(as.AgentID, as.ID, ae, tail)
		o.logger.Warn("连接意外断开：空闲会话保持原状态（会话可续聊）",
			"conversation", as.ID, "agent", as.AgentID, "runtime", as.Runtime)
	}
}

// evictConversation 把句柄从活跃集合摘除（仅当仍是当前句柄；已被新句柄顶替时不动）。
// 只为让下一次 prompt 能重建：会话记录（平台侧）不受影响。
func (o *Orchestrator) evictConversation(as *ActiveConversation) {
	o.mu.Lock()
	if o.conversations[as.ID] == as {
		delete(o.conversations, as.ID)
	}
	o.mu.Unlock()
}

// connCauseLabel 连接拆除原因（日志/诊断用）。
func connCauseLabel(intentional bool) string {
	if intentional {
		return "intentional" // 主动：daemon 停机 / 显式停 agent / 空闲回收
	}
	return "unexpected" // 意外：对端或子进程断开
}

func (o *Orchestrator) disconnectConversationAll(rc *runtimeConn) {
	intentional := rc.intentional.Load()
	o.mu.Lock()
	var victims []*ActiveConversation
	for _, as := range o.conversations {
		if as.client == rc.client {
			victims = append(victims, as)
		}
	}
	o.mu.Unlock()
	for _, as := range victims {
		o.disconnectConversation(as, intentional)
	}
	if len(victims) > 0 {
		// 可观测性：停机静默摘句柄最容易让人误判「重启把会话弄丢了」，日志里留一条事实——
		// 作废的只是 runtime 执行句柄，会话与状态都在（重启后按需续聊）。
		o.logger.Info("停机：会话句柄作废（会话与状态保留，重启后按需续聊）",
			"runtime", rc.spec.ID, "cause", connCauseLabel(intentional), "conversations", len(victims))
	}
}

// stopAgent 停止某 agent 对应 runtime 的进程（P2 启停）：
// 无连接 → 幂等置 stopped；有在途 turn → agent_busy 拒绝（先 cancel/等收尾）；
// 空闲 → 关连接（显式停机：会话句柄作废但状态如实保留；下次会话自动按需重拨）。
func (o *Orchestrator) stopAgent(ctx context.Context, agent *AgentRec, spec runtime.Spec) error {
	base := connKey(spec.ID, o.svc.nativePermissionEnvFor(spec.ID, agent.PermissionPolicy), o.svc.providerEnvFor(agent.Provider))
	keys := o.generationKeysFor(base)
	if len(keys) == 0 {
		o.svc.setAgentState(agent.ID, AgentStateStopped)
		return nil
	}
	// 该配置下**所有代际**都要关（代际是按能力指纹内容寻址的，同一 agent 的旧代际可能仍活着）；
	// 任一代际上有在途 turn 就整体拒绝——显式停机不该把跑着的一轮打断，也不该"关一半留一半"。
	o.mu.Lock()
	rcs := make([]*runtimeConn, 0, len(keys))
	busy := 0
	for _, k := range keys {
		rc := o.conns[k]
		if rc == nil {
			continue
		}
		rcs = append(rcs, rc)
		busy += o.inflight.Len(rc.client)
	}
	o.mu.Unlock()
	if len(rcs) == 0 {
		o.svc.setAgentState(agent.ID, AgentStateStopped)
		return nil
	}
	if busy > 0 {
		return acl.NewError(acl.ClassLimit, "agent_busy",
			fmt.Sprintf("agent %q 的 runtime %q 有 %d 个在途 turn，先取消/等待收尾", agent.ID, spec.ID, busy), nil)
	}
	for _, rc := range rcs {
		// 用户显式停 agent：先置原因再关（watchConn→dropConn 据此判定，不把会话写成失败态）。
		rc.intentional.Store(true)
		if err := rc.client.Close(); err != nil {
			return err
		}
	}
	// watchConn → dropConn 会作废会话句柄并置 stopped；这里等它们跑完再返回。
	for _, rc := range rcs {
		select {
		case <-rc.client.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	o.svc.setAgentState(agent.ID, AgentStateStopped)
	return nil
}
