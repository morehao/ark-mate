package manager

import (
	"context"
	"fmt"
	"github.com/morehao/ark-mate/internal/domain/session"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/runtimeconn"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// ——— P3a-2 第三刀：连接键 / 代际键的派生搬进 `internal/runtimeconn` ———
//
// 它们是**纯函数**（无锁、无 I/O、不碰 Orchestrator 字段），而"代际维度必须都在键里"这件事
// 只允许有一份实现：两处各算一遍的后果是"迁移判据说不用换进程、acquire 却换了"这种静默分叉。
// 这里只留转发，好让本包既有调用点（含六个测试文件）零改动。

// TODO(P4b): 删转发（调用点直接依赖 runtimeconn）
func connKey(runtimeID string, envs ...[]string) string {
	return runtimeconn.ConnKey(runtimeID, envs...)
}

// TODO(P4b): 删转发（同上）
func generationKey(connKey, fingerprint, configDigest string) string {
	return runtimeconn.GenerationKey(connKey, fingerprint, configDigest)
}

// TODO(P4b): 删转发（同上）

// TODO(P4b): 删转发（同上）
func shortGenerationKeys(keys []string) []string { return runtimeconn.ShortGenerationKeys(keys) }

// TODO(P4b): 删转发（同上）
func connCauseLabel(intentional bool) string { return runtimeconn.ConnCauseLabel(intentional) }

// baseKeyFor 当前配置下的**基础**连接键（runtime + provider env）。
//
// 单独成函数是因为它有第二个消费者：代际迁移要判断"这一轮的进程级能力面变了没有"
// （权限面变了 = 基础键变），而"变没变"的判据只允许有一个实现——两处各算一遍，
// 迟早会漂移成"迁移判据说没变、acquire 却换了进程"。
//
// v19 起不再有 per-agent 的权限策略与 provider 绑定：注入面的事实源是**声明的默认模型**的
// 供应商（见 providerEnvFor / CredentialProblem），所以这个键与"哪个会话在跑"无关——
// 单底座下它就是"这台机器上的这一份配置"。
func (o *Orchestrator) baseKeyFor(spec runtime.Spec) string {
	keyEnv := o.svc.providerPlane.ProviderEnvFor(spec.ID, o.svc.providerPlane.InjectedProviderID(context.Background()))
	return connKey(spec.ID, keyEnv)
}

// connOfClient 反查某客户端所属的连接（无 → nil）。连接数是代际上限量级（个位数），线性扫足够；
// 代际迁移要拿它读"这条句柄此刻挂在哪个代际键上"（那是判"要不要迁"的唯一权威来源）。
func (o *Orchestrator) connOfClient(client *acl.Client) *runtimeConn {
	if client == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, rc := range o.conns {
		if rc.client == client {
			return rc
		}
	}
	return nil
}

// admitGeneration 代际上限准入：在**拨号之前**判定"能不能再起一个代际"。
//
// 语义（风险表第 12 行）：上限是"猜粗了只是多花内存"这一侧的护栏，所以达到上限时**两步走**——
//  1. 先显式淘汰「无在途 run 且无任何会话引用」的代际（= 计划里的"显式淘汰空闲代际"，
//     不等空闲阈值：那些进程此刻已经没有任何人在用，收掉它们不损失任何东西）；
//  2. 淘汰后仍达上限 → **带码拒绝**（40901 `generation_limit_reached`），并在文案里给出出路
//     （复用现有能力集 / 等空闲代际回收 / 调大 MATE_GENERATION_LIMIT）。
//
// 绝不"找个差不多的代际塞进去"：能力集是进程级的，塞错了底座会给错的那一套且**不报错**
// ——那正是本仓 archtest 第 10 条守卫要防的事（猜细了是安全 bug）。
//
// 上限按**基础键**分组（同一 runtime + 同权限 + 同 provider 的进程池）：不同权重的进程池
// 本来就互不共享，拿一个全局数字去卡它们只会让"合法的多 agent"互相挤掉。
func (o *Orchestrator) admitGeneration(baseKey string) error {
	o.mu.Lock()
	limit := o.generationLimit
	o.mu.Unlock()
	if limit <= 0 {
		return nil // 0 = 不限制（显式关掉护栏）
	}
	if n := len(o.generationKeysFor(baseKey)); n < limit {
		return nil
	}
	// 达到上限：先淘汰该基础键下无人引用的代际，再复查。
	if n := o.reclaimUnreferenced(baseKey); n > 0 {
		glog.Infow(context.Background(), "代际上限：淘汰空闲代际", "base", baseKey, "reclaimed", n, "limit", limit)
	}
	keys := o.generationKeysFor(baseKey)
	if len(keys) < limit {
		return nil
	}
	return acl.NewError(acl.ClassLimit, "generation_limit_reached",
		fmt.Sprintf("该配置下的能力代际已达上限 %d（%s）：请复用现有能力集（用同一套技能/MCP 组合）、或等空闲代际被回收后重试；确需更多并发能力集时调大 MATE_GENERATION_LIMIT",
			limit, strings.Join(shortGenerationKeys(keys), ", ")), nil)
}

// reclaimUnreferenced 回收该基础键下「无在途 run 且无任何会话引用」的代际（`onlyBase` 空 = 全部），
// **不等空闲阈值**——判据是"此刻没有任何人在用"，不是"闲了多久"。用于代际上限准入时腾位置。
//
// 与 reapIdleOnce 的分工：那个是**周期性**的暖进程回收（判据含 idle 阈值，服务于"StartAgent
// 常驻但没人用"）；本函数是**事件驱动**的立即回收（正是计划里的"显式淘汰空闲代际"）。
// 回收动作都走 dropConn + client.Close，不另写回收器。返回回收数量。
func (o *Orchestrator) reclaimUnreferenced(onlyBase string) int {
	return o.reclaimConns(func(rc *runtimeConn) bool {
		return onlyBase == "" || rc.baseKey == onlyBase
	})
}

// reclaimGeneration 回收**指定的那一条**代际（若它已无人引用且空闲）。迁移离开旧代际后调用：
// 只动"自己刚离开的那一代"，不波及该基础键下别的无人引用进程（比如 StartAgent 的暖进程）。
// 返回是否真的回收了。
func (o *Orchestrator) reclaimGeneration(target *runtimeConn) bool {
	if target == nil {
		return false
	}
	return o.reclaimConns(func(rc *runtimeConn) bool { return rc == target }) > 0
}

// reclaimConns 回收满足 pick 且「无在途 run + 无任何会话引用」的连接。
func (o *Orchestrator) reclaimConns(pick func(*runtimeConn) bool) int {
	o.mu.Lock()
	var victims []*runtimeConn
	for _, rc := range o.conns {
		if !pick(rc) {
			continue
		}
		if o.inflight.Len(rc.client) > 0 {
			// 有在途 run：绝不打断。（挂起权限/提问不必另判：它们只在某一轮的执行期间存在，
			// 而那一轮的角色就是"在途 run"——`RegisterAndWait` 由该轮的 RPC 派发调用。）
			continue
		}
		hasSession := false
		for _, as := range o.sessions {
			if as.client == rc.client {
				hasSession = true
				break
			}
		}
		if hasSession {
			continue // 仍有会话引用（含已完成态），不回收
		}
		victims = append(victims, rc)
	}
	o.mu.Unlock()
	for _, rc := range victims {
		rc.intentional.Store(true) // 主动回收，不是连接故障
		o.dropConn(rc)
		if rc.client != nil {
			_ = rc.client.Close() // watchConn 收尾（dropConn 幂等）
		}
	}
	return len(victims)
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
func (o *Orchestrator) acquire(ctx context.Context, spec runtime.Spec, fingerprint string) (*runtimeConn, error) {
	// 凭据面先校验（P2）：**默认模型所在的那条档案**缺失/无 key 属配置错误 → 显式
	// credential_missing（文案含出路），不静默以「无密钥」姿态起进程。放 acquire 内可一处
	// 覆盖两条入口：新建会话、按需恢复。
	if err := o.svc.CredentialProblem(ctx); err != nil {
		return nil, err
	}
	// 基础键（进程级能力面：provider 注入面）——先算 env 再定缓存键：注入面变了就不能复用
	// 同一子进程。模型不进 env/缓存键：模型选择是**会话级**语义
	//（session/set_config_option），同一连接可服务多个模型。
	baseKey := o.baseKeyFor(spec)
	cfgDigest := o.svc.runtimeConfigDigest(spec.ID)
	key := generationKey(baseKey, fingerprint, cfgDigest)

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
	// 代际上限准入（M3-7）：**新建之前**判定。超限就带码拒绝，绝不"复用一个差不多"的能力集。
	if err := o.admitGeneration(baseKey); err != nil {
		return nil, err
	}
	keyEnv := o.svc.providerPlane.ProviderEnvFor(spec.ID, o.svc.providerPlane.InjectedProviderID(ctx))

	args := resolveArgs(spec.Launch.Args, RunOptions{ConfigPath: ""})
	var env []string
	env = append(env, keyEnv...)

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
		sid, source, note := o.resolveElicitationScope(cli, req)
		if sid == "" {
			// 既无 `_meta` scope 也无唯一在途 run（空闲 / 会话外 request-scope / 多会话并发）、
			// 或 `_meta` 给了一个**查不到平台会话**的底座 id（D-4）→ 不猜。
			// 仍按取消回应（不能把 agent 挂死），但置 ScopeAmbiguous 并留痕（agent 诊断 + 日志），
			// 让「提问卡为什么没出现」可查，而不是无痕消失。
			glog.Warnw(ctx, "elicitation: 无法归属会话，按取消处理", "scope_source", source, "note", note)
			o.svc.noteScopeAmbiguous(note)
			return acl.ElicitationOutcome{Action: "cancel"}
		}
		req.SessionID, req.ScopeSource = sid, source
		return o.requestElicitation(ctx, req)
	}
	// elicitation/complete（URL 模式收尾通知）：agent 告知提问已在 agent 侧完成 → 宿主按 ACP 侧
	// elicitationId 收敛挂起项。归属用「同连接的唯一在途 run」校验（可判定时）；不可判定时不阻断
	// ——该 id 由 agent 生成、不可猜，且 complete 只是收敛通知（重复/迟到都不该有副作用）。
	completeElic := func(_ context.Context, elicitationID string) {
		o.elic.CompleteByURLID(o.busySessionForClient(cli), elicitationID)
	}
	// 拨号用「脱离调用方 ctx」的上下文：子进程以 exec.CommandContext 启动，若绑定请求 ctx，
	// 请求返回即被杀（如 P2 StartAgent 暖启动后立刻断连）。WithoutCancel 后 Dial 自带的
	// 默认握手限时仍保证拨号有界；连接生命周期归编排器（watchConn/Stop/Close），与任一请求无关。
	// spawn + 握手由 runtime.Connect 承载（可执行投影的拨号底座，语义与此前内联实现一致）。
	dialCtx := context.WithoutCancel(ctx)
	client, err := runtime.Connect(dialCtx, spec, args, env, acl.ClientOptions{
		OnEvent:             o.eventSink(spec.ID),
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
	glog.Infow(ctx, "runtime 握手能力",
		"runtime", spec.ID,
		"sessions_persistent", caps.SessionsPersistent,
		"multi_session_concurrent", caps.MultiSessionConcurrent,
		"load_session", caps.Raw.LoadSession,
		"resume", sc.Resume != nil,
		"list", sc.List != nil,
		"fork", sc.Fork != nil,
		"close", sc.Close != nil)

	o.svc.runtimePlane.SetState(session.AgentStateRunning)
	rc = &runtimeConn{client: client, spec: spec, key: key, baseKey: baseKey,
		configDigest: cfgDigest, started: time.Now()}
	o.registerProc(rc)
	o.mu.Lock()
	o.conns[key] = rc
	o.mu.Unlock()
	go o.watchConn(rc)
	return rc, nil
}

// markBusy / unmarkBusy 委托 inFlightTracker（§17 E1 显式化簿记；语义不变）。
// client 为 nil（理论上不应出现）或未登记时均安全 no-op。
func (o *Orchestrator) markBusy(client *acl.Client, sessionID string) {
	o.inflight.Mark(client, sessionID)
}

func (o *Orchestrator) unmarkBusy(client *acl.Client, sessionID string) {
	o.inflight.Unmark(client, sessionID)
}

// busySessionForClient 返回该连接上「唯一在途」的会话 id；无在途或多会话并发在途返回空串。
func (o *Orchestrator) busySessionForClient(client *acl.Client) string {
	return o.inflight.SoleOwner(client)
}

// connForRuntime 返回该 runtime 已建立的连接（无 → nil；runtimeID 空 = 任一条）。
// 用于读取握手上冻结的能力断言（如鉴权引导）：不主动拨号，避免「查一次目录就起一个进程」。
func (o *Orchestrator) connForRuntime(runtimeID string) *runtimeConn {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, rc := range o.conns {
		if runtimeID != "" && rc.spec.ID != runtimeID {
			continue
		}
		select {
		case <-rc.client.Done():
			continue // 已断开：视为无连接
		default:
			return rc
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

// unregisterSessionProvider 移除某会话的原生→平台映射（连接失活/会话清理时调用）。
func (o *Orchestrator) unregisterSessionProvider(sessionID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for k, v := range o.byProvider {
		if v == sessionID {
			delete(o.byProvider, k)
		}
	}
}

// watchConn 监听连接断开：清理注册、失活其上会话、标记 runtime stopped。
func (o *Orchestrator) watchConn(rc *runtimeConn) {
	<-rc.client.Done()
	o.dropConn(rc)
}

func (o *Orchestrator) dropConn(rc *runtimeConn) {
	o.unregisterProc(rc)
	// 拆除原因由拆除方在关闭连接前置位（Close/reapIdleOnce = 主动；watchConn 观察到
	// 对端结束 = 意外）。它决定会话状态怎么收尾——见 disconnectSession。
	intentional := rc.intentional.Load()
	o.mu.Lock()
	ownerBefore := o.conns[rc.key] == rc
	if ownerBefore {
		delete(o.conns, rc.key)
	}
	o.inflight.Clear(rc.client) // 连接断开：在途登记一并作废（executeRun 的 unmark 对已清键为 no-op）
	var victims []*ActiveSession
	for _, as := range o.sessions {
		if as.client == rc.client {
			victims = append(victims, as)
		}
	}
	o.mu.Unlock()
	for _, as := range victims {
		o.unregisterSessionProvider(as.ID) // 连接断开：原生→平台映射一并清除（迟到事件丢弃）
	}
	for _, as := range victims {
		o.disconnectSession(as, intentional)
	}
	// 状态回落仅当仍是该 runtime 的当前连接（旧连接收尾不得覆盖新连接的 running——
	// 空闲回收后立刻被会话按需重拨的场景）。
	if ownerBefore {
		o.svc.runtimePlane.SetState(session.AgentStateStopped)
	}
	if len(victims) > 0 {
		glog.Infow(context.Background(), "连接拆除：会话句柄作废（会话保留，下次 prompt 按需重建）",
			"runtime", rc.spec.ID,
			"cause", connCauseLabel(intentional), "sessions", len(victims))
	}
}

// disconnectSession 把某断连连接上的会话句柄作废并摘出活跃集合。
//
// 本函数是「连接生命周期」写会话状态的**唯一入口**，因此必须区分拆除原因——把停机当成故障，
// 会让上一轮已 completed 的会话在每次重启后被改写成 failed（并往历史里追加假的 run.error）：
//
//   - 在途 run：主动拆除（daemon 停机 / 显式停 agent）→ 收尾归 interrupted（无终态、可续聊
//     重发，与崩溃恢复同义，不发错误事件）；意外断开 → 解除阻塞，由 executeRun 按错误收尾。
//   - 空闲会话：**不改写状态**。上一轮的终态是轮次结果的事实，连接掉了并不改变它；意外断开
//     只落 agent 级诊断（last_error + stderr 尾巴）与日志，不伪造会话失败。
//
// 会话记录本身（svc/sessions 与 store）不动：执行可丢弃、会话平台拥有（ADR-4），
// 下次 prompt 经 ensureSession 走 native resume / handoff memo 重建句柄。
func (o *Orchestrator) disconnectSession(as *ActiveSession, intentional bool) {
	as.mu.Lock()
	if as.dead {
		as.mu.Unlock()
		return
	}
	as.dead = true
	t := as.run
	if t != nil && intentional {
		t.intentional = true // 主动拆除：收尾归 interrupted，不按失败/用户取消处理
	}
	as.mu.Unlock()
	o.evictSession(as)

	// 连接没了，挂起的权限/提问永远等不到决策：随拆除一并收敛，不留「待确认」幽灵卡。
	o.perm.CancelSession(as.ID)
	o.elic.CancelSession(as.ID)

	if t != nil {
		t.cancel()
		return
	}
	if !intentional {
		ae := acl.NewError(acl.ClassConnection, "connection_lost",
			"runtime 连接断开（会话句柄已作废；下次 prompt 按需重建）", nil)
		var tail []string
		if as.client != nil {
			tail = acl.TailLines(as.client.StderrTail(), runtimeconn.MaxDiagStderrLines)
		}
		ae.StderrTail = tail
		o.svc.recordRuntimeError(as.ID, ae, tail)
		glog.Warnw(context.Background(), "连接意外断开：空闲会话保持原状态（会话可续聊）",
			"session", as.ID, "runtime", as.Runtime)
	}
}

// evictSession 把句柄从活跃集合摘除（仅当仍是当前句柄；已被新句柄顶替时不动）。
// 只为让下一次 prompt 能重建：会话记录（平台侧）不受影响。
func (o *Orchestrator) evictSession(as *ActiveSession) {
	o.mu.Lock()
	if o.sessions[as.ID] == as {
		delete(o.sessions, as.ID)
	}
	o.mu.Unlock()
}

func (o *Orchestrator) disconnectSessionAll(rc *runtimeConn) {
	intentional := rc.intentional.Load()
	o.mu.Lock()
	var victims []*ActiveSession
	for _, as := range o.sessions {
		if as.client == rc.client {
			victims = append(victims, as)
		}
	}
	o.mu.Unlock()
	for _, as := range victims {
		o.disconnectSession(as, intentional)
	}
	if len(victims) > 0 {
		// 可观测性：停机静默摘句柄最容易让人误判「重启把会话弄丢了」，日志里留一条事实——
		// 作废的只是 runtime 执行句柄，会话与状态都在（重启后按需续聊）。
		glog.Infow(context.Background(), "停机：会话句柄作废（会话与状态保留，重启后按需续聊）",
			"runtime", rc.spec.ID, "cause", connCauseLabel(intentional), "sessions", len(victims))
	}
}
