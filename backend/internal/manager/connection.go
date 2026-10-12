package manager

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
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

// generationKey 在基础连接键之上追加**代际维度**：能力指纹 + 平台生成的模型配置指纹（T5）。
//
// 两个维度缺一不可，而且它们**必须都在键里**：
//   - 指纹相同 = 同一套能力 = 复用同一个暖进程；指纹不同 = 各起一个进程（能力集与投影根都是
//     进程级的，混用等于把 A 会话的 skill 投给 B 会话）。
//   - 配置指纹（T5）：配置文件是**进程启动时**读的。不把它并进键，改完清单文件已经变了、暖进程
//     还活着，底座继续用旧清单，而界面显示"已保存"——那是本方案最坏的一种表现。
//
// **两者都为空 → 原样返回基础键**：这是 R11（缺省零回归）的落点——既未声明能力选择、
// 该 runtime 又没有平台模型清单的会话，其键必须与代际机制之前**逐字相同**。
//
// 非空时用**带长度前缀**的形态而不是简单拼后缀：`connKey("r", envA)` 是
// `connKey("r", envA, envB)` 的字符串前缀（env 组之间就是 `\x00` 分隔），拼后缀会让"某个基础键
// 恰好是另一个基础键的前缀"与"代际后缀"互相串味。长度前缀让基础键自我定界。
func generationKey(connKey, fingerprint, configDigest string) string {
	if fingerprint == "" && configDigest == "" {
		return connKey
	}
	return "gen:" + strconv.Itoa(len(connKey)) + ":" + connKey + ":" + fingerprint + ":" + configDigest
}

// baseKeyFor 该 agent 在当前配置下的**基础**连接键（runtime + 原生权限 env + provider env）。
//
// 单独成函数是因为它有第二个消费者：代际迁移要判断"这一轮的进程级能力面变了没有"
// （权限被收回 = 基础键变），而"变没变"的判据只允许有一个实现——两处各算一遍，
// 迟早会漂移成"迁移判据说没变、acquire 却换了进程"。
func (o *Orchestrator) baseKeyFor(agent *AgentRec, spec runtime.Spec) string {
	nativeEnv := o.svc.nativePermissionEnvFor(spec.ID, agent.PermissionPolicy)
	keyEnv := o.svc.providerEnvFor(spec.ID, agent.Provider)
	return connKey(spec.ID, nativeEnv, keyEnv)
}

// dedupeStrings 保序去重（小切片，线性扫足够）。
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// generationKeysHoldingAgent 那些**装着该 agent 会话**、且没有别的 agent 会话的代际键。
//
// 存在的理由：代际键里含进程级能力面（原生权限 / provider），所以"agent 的会话"与
// "agent 当前配置的代际键"会随配置漂移而分家——`stopAgent` 只按当前键扫会漏掉旧键下的代际。
// 判据只看**会话归属**（句柄上的 AgentID），与配置无关，因此不受漂移影响。
// 与别的 agent 共用的代际不返回（停一个 agent 不该关掉另一个 agent 正在用的进程）。
func (o *Orchestrator) generationKeysHoldingAgent(agentID string) []string {
	if agentID == "" {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, rc := range o.conns {
		mine, theirs := false, false
		for _, as := range o.sessions {
			if as.client != rc.client {
				continue
			}
			if as.AgentID == agentID {
				mine = true
			} else {
				theirs = true
			}
		}
		if mine && !theirs && !seen[rc.key] {
			seen[rc.key] = true
			out = append(out, rc.key)
		}
	}
	return out
}

// generationKeysWarmOwnedBy 该 agent 创建、且**没有任何会话引用**的代际键（暖进程池）。
//
// 归属用 `runtimeConn.agentID`（创建者）：无人引用的进程没有别的归属线索，创建者是唯一可用的
// 那份事实。安全性来自"无人引用"这个前置条件——只要还有任何会话挂在上面（哪怕是别的 agent 的），
// 这条代际就不在返回集里，停 agent 不会把别人正在用的进程关掉。
func (o *Orchestrator) generationKeysWarmOwnedBy(agentID string) []string {
	if agentID == "" {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for _, rc := range o.conns {
		if rc.agentID != agentID {
			continue
		}
		referenced := false
		for _, as := range o.sessions {
			if as.client == rc.client {
				referenced = true
				break
			}
		}
		if !referenced {
			out = append(out, rc.key)
		}
	}
	return out
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

// shortGenerationKeys 把代际键压成"指纹前 8 位"的可读形态（拒绝文案与日志用；键本身太长）。
func shortGenerationKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if i := strings.LastIndex(k, ":"); i >= 0 && i+1 < len(k) {
			k = k[i+1:]
		}
		if k == "" {
			k = "缺省"
		} else if len(k) > 8 {
			k = k[:8]
		}
		out = append(out, k)
	}
	return out
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
func (o *Orchestrator) acquire(ctx context.Context, agent *AgentRec, spec runtime.Spec, fingerprint string) (*runtimeConn, error) {
	// 凭据面先校验（P2）：agent 绑定了 provider 档案而档案缺失/无 key 属配置错误 → 显式
	// credential_missing（文案含出路），不静默以「无密钥」姿态起进程。放 acquire 内可一处
	// 覆盖三条入口：新建会话、按需恢复、StartAgent 暖启动。
	if err := o.svc.CredentialProblem(agent); err != nil {
		return nil, err
	}
	// 基础键（进程级能力面：原生权限开关 + provider 注入面）——先算 env 再定缓存键：
	// 权限开关是进程级，配置不同的 agent 不能复用同一子进程。模型不进 env/缓存键：
	// 模型选择是**会话级**语义（session/set_config_option），同一连接可服务多个模型。
	baseKey := o.baseKeyFor(agent, spec)
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
	nativeEnv := o.svc.nativePermissionEnvFor(spec.ID, agent.PermissionPolicy)
	keyEnv := o.svc.providerEnvFor(spec.ID, agent.Provider)

	args := resolveArgs(spec.Launch.Args, RunOptions{ConfigPath: ""})
	var env []string
	env = append(env, keyEnv...)
	env = append(env, nativeEnv...)

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
			o.svc.noteScopeAmbiguous(agent.ID, note)
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
		OnEvent:             o.eventSink(agent.ID, spec.ID),
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

	o.svc.setAgentState(agent.ID, AgentStateRunning)
	rc = &runtimeConn{client: client, agentID: agent.ID, spec: spec, key: key, baseKey: baseKey,
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

// DetachAgent 摘除某 agent 的全部活跃会话（agent 被删除时由 Service.DeleteAgent 调用）。
//
// 语义（P2 删除面）：归属不可变——平台不改写会话的 agent_id，也不静默改绑到别的 agent；
// 但删除必须**立刻**生效：把内存里仍活着的会话摘出登记，使后续 prompt 走
// ensureSession → agent_not_found（并带上「同 id 重建 / 导出历史」的出路），而不是让它在
// 半死连接上一直跑到进程重启。
//
// 有在途 run 时整体拒绝（agent_busy，先 cancel/等收尾）：不删到一半的轮次，此时不动任何登记。
// 返回被摘除的会话数；底层 ACP 会话不在此关闭（连接可能被同 runtime 的其他 agent 复用，
// 会话生命周期归底座自身与空闲回收）。
func (o *Orchestrator) DetachAgent(agentID string) (int, error) {
	if agentID == "" {
		return 0, nil
	}
	o.mu.Lock()
	victims := make([]*ActiveSession, 0, 4)
	for _, as := range o.sessions {
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
		busy := as.busy || as.run != nil
		as.mu.Unlock()
		if busy {
			return 0, acl.NewError(acl.ClassLimit, "agent_busy",
				fmt.Sprintf("agent %q 的会话 %q 有在途 run，先取消/等待收尾再删除", agentID, as.ID), nil)
		}
	}
	o.mu.Lock()
	detached := 0
	for _, as := range victims {
		if o.sessions[as.ID] == as {
			delete(o.sessions, as.ID)
			detached++
		}
	}
	o.mu.Unlock()
	for _, as := range victims {
		// 原生 → 平台映射一并清除：迟到事件不再路由回已摘除的会话。
		o.unregisterSessionProvider(as.ID)
	}
	glog.Infow(context.Background(), "agent 已删除：活跃会话已摘除（后续 prompt 报 agent_not_found）",
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
	// 空闲回收后立刻被 StartAgent/会话按需重拨的场景）。
	if ownerBefore {
		o.svc.setAgentState(rc.agentID, AgentStateStopped)
	}
	if len(victims) > 0 {
		glog.Infow(context.Background(), "连接拆除：会话句柄作废（会话保留，下次 prompt 按需重建）",
			"runtime", rc.spec.ID, "agent", rc.agentID,
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
			"agent 连接断开（会话句柄已作废；下次 prompt 按需重建）", nil)
		var tail []string
		if as.client != nil {
			tail = acl.TailLines(as.client.StderrTail(), maxDiagStderrLines)
		}
		ae.StderrTail = tail
		o.svc.recordAgentError(as.AgentID, as.ID, ae, tail)
		glog.Warnw(context.Background(), "连接意外断开：空闲会话保持原状态（会话可续聊）",
			"session", as.ID, "agent", as.AgentID, "runtime", as.Runtime)
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

// connCauseLabel 连接拆除原因（日志/诊断用）。
func connCauseLabel(intentional bool) string {
	if intentional {
		return "intentional" // 主动：daemon 停机 / 显式停 agent / 空闲回收
	}
	return "unexpected" // 意外：对端或子进程断开
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

// stopAgent 停止某 agent 对应 runtime 的进程（P2 启停）：
// 无连接 → 幂等置 stopped；有在途 run → agent_busy 拒绝（先 cancel/等收尾）；
// 空闲 → 关连接（显式停机：会话句柄作废但状态如实保留；下次会话自动按需重拨）。
func (o *Orchestrator) stopAgent(ctx context.Context, agent *AgentRec, spec runtime.Spec) error {
	base := o.baseKeyFor(agent, spec)
	keys := o.generationKeysFor(base)
	// 基础键漂移（权限被收窄过 / provider 档案换过）时，旧基础键下的代际不在 `keys` 里，
	// 但它们装的仍是**这个 agent 的会话**——只按"当前配置"扫会漏掉它们，表现是
	// "agent 已停，旧权限面的进程还活着，直到空闲回收才走"（代际机制引入后新出现的一类漏）。
	// 补扫规则刻意保守：只收「这条代际上没有别的 agent 的会话」的那些，
	// 绝不因为停 A 而关掉 A、B 共用的暖进程（那是 B 正在用的东西）。
	keys = append(keys, o.generationKeysHoldingAgent(agent.ID)...)
	// 还有第三类：本 agent 创建、当前**无人引用**的代际（StartAgent 暖进程）。基础键漂移后
	// 它们落在旧键下，前两类都扫不到（没有会话可归属）——不补这一扫，表现是"agent 已停，
	// 旧权限面的暖进程还在跑到空闲回收"。判据（创建者 + 无人引用）保守：有会话挂着的都不动。
	keys = append(keys, o.generationKeysWarmOwnedBy(agent.ID)...)
	keys = dedupeStrings(keys) // 三个来源可能重叠；重复项会让下面同一连接被关两次
	if len(keys) == 0 {
		// 没有可关的代际 ≠ 没有事要做：本 agent 的会话可能正挂在**与别的 agent 共用**的代际上
		// （同 runtime + 同权限 + 同 provider），那些进程要留给别人，但"停"对本 agent 的句柄同样生效。
		o.evictAgentHandles(agent.ID, nil)
		o.svc.setAgentState(agent.ID, AgentStateStopped)
		return nil
	}
	// 该配置下**所有代际**都要关（代际是按能力指纹内容寻址的，同一 agent 的旧代际可能仍活着）；
	// 任一代际上有在途 run 就整体拒绝——显式停机不该把跑着的一轮打断，也不该"关一半留一半"。
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
			fmt.Sprintf("agent %q 的 runtime %q 有 %d 个在途 run，先取消/等待收尾", agent.ID, spec.ID, busy), nil)
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
	// 共用代际上的本 agent 句柄：那些进程没关（留给别的 agent），但句柄不能继续挂在上面——
	// 否则"停 agent"对它们形同没停（下一次 prompt 会以为会话还活着，继续用旧权限面的进程）。
	o.evictAgentHandles(agent.ID, rcs)
	o.svc.setAgentState(agent.ID, AgentStateStopped)
	return nil
}

// evictIdleCapabilityHandles 作废该 agent **空闲**会话的句柄，让下一次 prompt 按**当前**定义
// 重建会话（M6-8 / P-13，判据说明见 `agent_capability_notify.go` 文件头）。
//
// 与 `stopAgent`/`DetachAgent` 的分工：那两条是"用户显式停了 agent/删了 agent"，要关进程或摘登记；
// 这条只是"定义换了，让活着的句柄在下一次重建时取到新值"，**不关进程、不删登记、不动会话记录**
// （会话可续聊是 ADR-4 的承诺）。
//
// 返回（作废数, 顺延数）。顺延的判据与代际迁移纪律③**逐条相同**（有在途轮次 / 有未应答的权限或
// 提问 → 不动）：同一条会话在两个机制下必须给出同样的裁决，否则"这一轮到底换没换面"会变成
// 两处各说一套。探针读不到（句柄 `dead`）时不算顺延也不算作废（`disconnectSession` 自己幂等）。
func (o *Orchestrator) evictIdleCapabilityHandles(agentID, field string) (evicted, deferred int) {
	if agentID == "" {
		return 0, 0
	}
	o.mu.Lock()
	victims := make([]*ActiveSession, 0, 4)
	for _, as := range o.sessions {
		if as.AgentID == agentID {
			victims = append(victims, as)
		}
	}
	o.mu.Unlock()
	for _, as := range victims {
		// 在途判定在 o.mu 之外做（as.mu 与 o.mu 不同级，避免交错持锁）。
		as.mu.Lock()
		busy := as.busy || as.run != nil
		as.mu.Unlock()
		if busy || o.perm.pendingCount(as.ID) > 0 || o.elic.pendingCount(as.ID) > 0 {
			deferred++
			continue
		}
		// 主动拆除：句柄作废、挂起的权限/提问一并收敛，会话记录与状态原样保留。
		o.disconnectSession(as, true)
		evicted++
	}
	if evicted > 0 || deferred > 0 {
		glog.Infow(context.Background(), "能力面变更：会话句柄将按当前定义重建",
			"agent", agentID, "field", field, "evicted", evicted, "deferred", deferred)
	}
	return evicted, deferred
}

// evictAgentHandles 作废该 agent 在**未关闭**连接上的会话句柄（`closing` 里的连接由 dropConn 收尾，
// 这里跳过以免重复处理）。刻意不关进程、不删会话记录：会话可续聊（下次 prompt 重建），
// 共用该进程的别的 agent 不受影响。
func (o *Orchestrator) evictAgentHandles(agentID string, closing []*runtimeConn) {
	if agentID == "" {
		return
	}
	skip := make(map[*acl.Client]bool, len(closing))
	for _, rc := range closing {
		if rc.client != nil {
			skip[rc.client] = true
		}
	}
	o.mu.Lock()
	var victims []*ActiveSession
	for _, as := range o.sessions {
		if as.AgentID != agentID || skip[as.client] {
			continue
		}
		victims = append(victims, as)
	}
	o.mu.Unlock()
	for _, as := range victims {
		o.disconnectSession(as, true) // 主动停机：句柄作废，会话与状态保留
	}
}
