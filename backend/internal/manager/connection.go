package manager

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/runtimeconn"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// ——— 连接池的**编排侧**（P4a 正文第五十四刀） ———
//
// 连接表、连接键/代际键、代际准入与回收、子进程登记、空闲回收都随本刀进了
// `internal/runtimeconn`（`Pool` / `Conn`）。本文件只剩两件事，而它们都属于这一侧：
//
//	① **拨号**（`acquire`）：ACP 的三个回调是会话/编排的事实（事件出口、权限、提问），
//	   拨好之后交给池落位（`Pool.Add`：登记进程 + 状态置 running + 入表 + 起断连监听）；
//	② **拆除的应用**（`DropSink`）：一条连接被摘除之后，会话状态怎么收尾、在途登记怎么作废、
//	   诊断怎么留痕——那是会话域的不变量，池不该认识（它也放不进 ≤4 方法的端口）。
//
// **锁**：本文件里 `o.mu` 只保护 `sessions` / `byProvider`；连接表在池自己的锁下。
// 两条边都不许互指——池在**池锁之外**回调这里（见 `Pool.Drop` 的注释）。

// acquire 取（或拨）一条 runtime 连接：活着复用，断开则作废重拨；并挂断连监听。
//
// fingerprint = 该会话钉住的能力指纹（M1 恒为空：显式能力选择是 M3 的面）。空值走的是
// GenerationKey 的原样返回分支，所以缺省路径与"代际机制之前"完全等价。
func (o *Orchestrator) acquire(ctx context.Context, spec runtime.Spec, fingerprint string) (*runtimeconn.Conn, error) {
	// 凭据面先校验（P2）：**默认模型所在的那条档案**缺失/无 key 属配置错误 → 显式
	// credential_missing（文案含出路），不静默以「无密钥」姿态起进程。放 acquire 内可一处
	// 覆盖两条入口：新建会话、按需恢复。
	if err := o.svc.CredentialProblem(ctx); err != nil {
		return nil, err
	}
	// 键由池一次算完：基础键（进程级能力面）、下发给子进程的 env、配置指纹与代际键
	// **同源**——分开取会得到"键说共享、注入的却是另一套 env"这种静默分叉。
	// 模型不进 env/缓存键：模型选择是**会话级**语义（session/set_config_option），
	// 同一连接可服务多个模型。
	keys := o.pool.KeyFor(spec, fingerprint)

	if rc := o.pool.Get(keys.Key); rc != nil {
		if rc.Client == nil {
			return rc, nil // 测试桩：没有句柄可验活，按登记事实返回
		}
		select {
		case <-rc.Client.Done():
			// 旧连接已断开（Drop 已/将清理）：落到下方重拨
		default:
			return rc, nil
		}
	}
	// 代际上限准入（M3-7）：**新建之前**判定。超限就带码拒绝，绝不"复用一个差不多"的能力集。
	if err := o.pool.AdmitGeneration(keys.BaseKey); err != nil {
		return nil, err
	}

	args := resolveArgs(spec.Launch.Args, RunOptions{ConfigPath: ""})
	env := keys.Env

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
	// 默认握手限时仍保证拨号有界；连接生命周期归执行面（watch/Close），与任一请求无关。
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

	rc := &runtimeconn.Conn{Client: client, Spec: spec, Key: keys.Key, BaseKey: keys.BaseKey,
		ConfigDigest: keys.ConfigDigest, Started: time.Now()}
	o.pool.Add(rc) // 登记子进程 + 状态置 running + 入表 + 起断连监听
	return rc, nil
}

// ——— `runtimeconn.DropSink` 的实现：连接拆除的**应用** ———

// OnConnDropped 一条连接刚被池摘除（池锁已释放）：作废其上的会话句柄、清在途登记与原生 id 映射。
//
// 拆除原因决定会话状态怎么收尾（`disconnectSession` 的注释是本仓对这条语义的唯一说明）：
// 把停机当成故障，会让上一轮已 completed 的会话在每次重启后被改写成 failed。
func (o *Orchestrator) OnConnDropped(d runtimeconn.ConnDrop) {
	// 连接断开：在途登记一并作废（executeRun 的 unmark 对已清键为 no-op）。
	o.inflight.Clear(d.Client)
	o.mu.Lock()
	victims := make([]*ActiveSession, 0, 1)
	for _, as := range o.sessions {
		if as.client == d.Client {
			victims = append(victims, as)
		}
	}
	o.mu.Unlock()
	for _, as := range victims {
		o.unregisterSessionProvider(as.ID) // 连接断开：原生→平台映射一并清除（迟到事件丢弃）
	}
	for _, as := range victims {
		o.disconnectSession(as, d.Intentional)
	}
	if len(victims) > 0 {
		glog.Infow(context.Background(), "连接拆除：会话句柄作废（会话保留，下次 prompt 按需重建）",
			"runtime", d.Runtime, "cause", d.Cause, "sessions", len(victims))
	}
}

// InUseOn 这条连接还有人用吗：有在途轮次、或仍挂着会话（含已完成态）。
//
// 这是空闲回收与代际回收的**唯一判据**，判定留在这一侧是因为"谁挂在这条连接上"是会话侧
// 的登记（`ActiveSession.client`）。池在**池锁之外**问它（见 `Pool.reclaim`），
// 所以这里取 `o.mu` 不会与池锁互指。
func (o *Orchestrator) InUseOn(c *runtimeconn.Conn) bool {
	if c == nil {
		return true
	}
	if o.inflight.Len(c.Client) > 0 {
		// 有在途 run：绝不打断。（挂起权限/提问不必另判：它们只在某一轮的执行期间存在，
		// 而那一轮的角色就是"在途 run"——`RegisterAndWait` 由该轮的 RPC 派发调用。）
		return true
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, as := range o.sessions {
		if as.client == c.Client {
			return true // 仍有会话引用（含已完成态），不回收
		}
	}
	return false
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

// **刻意不留**那五个键派生函数（`connKey`/`generationKey`/`shortGenerationKeys`/
// `connCauseLabel`/`dedupeStrings`）的转发：调用点已随本刀改成 `runtimeconn.X`，
// 一行转发只会让「实现在哪一层」重新不可见（P4a 一路在做的事）。
