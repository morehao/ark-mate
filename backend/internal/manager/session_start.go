package manager

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// ApplySessionModel 把模型变更应用到**已存在的会话**（会话内改模型）。
//
//   - 会话不在活跃集合（未启动/已断连）→ (false, nil)：调用方只更新元数据，等下次恢复会话时生效；
//   - 活跃 → 经 session/set_config_option 应用（运行时是唯一判据），失败原样上抛（调用方不得吞）。
func (o *Orchestrator) ApplySessionModel(ctx context.Context, sessionID string, model runtime.ModelConfig) (bool, error) {
	o.mu.Lock()
	as := o.sessions[sessionID]
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

// StartSession 在**构建期选定的** runtime 上建立持久 ACP 会话（session/new，绝对 cwd），
// 并发布 session.created。不发起 prompt；Prompt 负责每轮。
//
// 底座不是这次调用的入参：它是二进制的属性（pkg/runtime/build.go）。这里只做"取那一个 + 校验
// 已装配"（`ResolveRuntime`）。
func (o *Orchestrator) StartSession(ctx context.Context, agentID string, opt StartOptions) (*CreateResult, error) {
	agent, ok := o.svc.AgentByID(agentID)
	if !ok {
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", agentID), nil)
	}
	return o.startSessionOn(ctx, agent, opt)
}

// startSessionOn 在**构建期选定的** runtime 上建立持久 ACP 会话（Service.CreateSession
// 与 RunOnce 都汇聚于此）。返回的 CreateResult.Runtime 为实际选中的 runtime。
func (o *Orchestrator) startSessionOn(ctx context.Context, agent *AgentRec, opt StartOptions) (*CreateResult, error) {
	runtimeID, err := o.svc.ResolveRuntime()
	if err != nil {
		return nil, err
	}
	spec, ok := o.reg.Get(runtimeID)
	if !ok {
		return nil, acl.NewError(acl.ClassRequest, "runtime_not_found", fmt.Sprintf("runtime %q 未注册", runtimeID), nil)
	}
	// 能力选定校验（M3-3）：**在建连之前**。位次很关键——它既要在已知 runtime 之后
	// （可达性是 runtime 相关的），又要在任何副作用之前（不建会话/不拉进程/不写库）。
	// 不可达就报错而非静默丢弃：静默丢弃等于让用户以为"选了就生效了"。
	if err := o.svc.validateSelection(opt.Selection, spec.ID); err != nil {
		return nil, err
	}
	// 能力指纹由**选定**现算（M3）：缺省路径 opt.Selection == nil → 空指纹 →
	// 代际键与引入代际之前逐字相同（R11）。指纹与下面注入用的 opt.Selection 同源，
	// 不存在"按 A 算指纹、按 B 注入"的可能。
	fingerprint := o.svc.capabilityFingerprint(agent, spec.ID, opt.Selection)
	rc, err := o.acquire(ctx, agent, spec, fingerprint)
	if err != nil {
		o.svc.setAgentState(agent.ID, AgentStateStopped)
		return nil, err
	}
	cwd := opt.Cwd
	if cwd == "" {
		// demo/RunOnce 语义（会话创建路径已在 Service 侧定出工作空间，不会走到这里）。
		cwd, _ = os.Getwd()
	}
	// 平台会话 id 在 session/new **之前**定号：代建工作空间的目录名以它为准
	// （目录先于 session/new 存在，底座对 cwd 做存在性校验）。
	convID := opt.SessionID
	if convID == "" {
		convID = objid.New(objid.Session)
	}
	// P2 MCP 注入：平台注册表 MCP 子集按「会话选定 > agent 白名单」+ runtime 传输能力过滤后
	// 随 session/new 下发（§5.2/§5.3；会话级选定见 M3）。**必须传 opt.Selection**：
	// 指纹就是按它算出来的，注入若用别的口径，会话拿到的东西会与代际台账不符。
	sess, err := rc.client.NewSession(ctx, acl.SessionOptions{
		Cwd:        cwd,
		McpServers: o.svc.McpServersFor(agent, spec.ID, opt.Selection),
	})
	if err != nil {
		return nil, err
	}

	// P3 双 id（D-1，用户已授权破坏性变更）：会话对外 id = 平台铸造的 `ses_<ULID>`（隐藏底座 id 形状、
	// 跨重启稳定定位）；原生 provider id 存 sess/ProviderSessionID 列，供恢复承载（load/resume）。
	as := &ActiveSession{ID: convID, AgentID: agent.ID, Runtime: spec.ID, Fingerprint: fingerprint,
		client: rc.client, sess: sess, providerID: sess.ID(), proj: runtime.NewProjection(spec, sess)}
	// 模型回落链（E5/T2 快路径）：**会话显式 model > models.is_default[该 runtime] > 不下发**。
	//
	// 三条纪律：
	//   1. 只读 `models` 表，**不读 agents**——agent 上是同一个值的派生投影，绕它走会多一个
	//      可能不一致的源；
	//   2. runtime 取「本次会话实际选中的 runtime」（就是这里的 spec.ID），不是 agent 的第一个；
	//   3. 查不到默认就**不下发**（`opt.Model.Model` 保持空）——把"没有默认"与"默认是某个值"
	//      分开，并允许底座自身的默认继续生效（零回归的那一支）。
	//
	// 为什么这条路径不可省：T5（写配置文件）要**新进程**才生效，且首版 dsh 侧 Tested=false
	// 只读——只有 T5 的话，dsh 上"设为默认"点了等于没点。
	if opt.Model.Model == "" {
		if v, err := o.svc.DefaultModelValueFor(ctx, spec.ID); err != nil {
			glog.Warnw(ctx, "读取平台默认模型失败（本次不下发，交给底座自己的默认）",
				"runtime", spec.ID, "err", err)
		} else if v != "" {
			opt.Model = runtime.ModelConfig{Model: v}
		}
	}
	// 会话级模型应用（运行时派生模型面的落地，决策记录）：
	//   - 值必须来自运行时自报目录（category=model），set 的成败即「可用性」判据；
	//   - 运行时**拒绝**该值 → 建会话失败（错误上抛给调用方，绝不静默回落到底座默认模型）；
	//   - unsupported_*（运行时未广告模型配置项）→ 忽略（无模型语义）；
	//   - 成功 → 用返回的**全量**配置项刷新平台目录缓存（顺带清掉曾失败值的标记）。
	if err := o.applyModelOnSession(ctx, as, agent.ID, spec, sess, opt.Model); err != nil {
		_ = sess.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	// W1：底座原文 → 平台侧的值（`rt_` 壳）。**失败即返回错误、不建 rec**——把裸 id 落库会让
	// "平台 id 与底座 id 形状可分"这条不变式当场失效，而那是静默的（守卫② 事后才发现）。
	// `as.providerID` 刻意保留**原文**：它是进程内映射 `byProvider` 的键，与 ACP 回调同源。
	providerSessID, err := objid.WrapProvider(sess.ID())
	if err != nil {
		_ = sess.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	o.mu.Lock()
	o.sessions[as.ID] = as
	o.byProvider[sess.ID()] = as.ID
	o.mu.Unlock()

	o.svc.registerSession(&SessionRec{ID: as.ID, AgentID: agent.ID, Cwd: cwd,
		WorkspaceID: opt.WorkspaceID, WorkspaceTemporary: opt.WorkspaceTemporary, Provider: agent.Provider,
		Model: opt.Model.Model, RuntimeHint: spec.ID, Fingerprint: fingerprint, Origin: opt.Origin,
		ProviderSessionID: providerSessID, Status: SessionStatusInProgress,
		// 钉住的选定**与指纹一起**落库（M3-3 起）：两者必须同源，缺一则恢复路径
		// 要么注入集不对、要么代际挂错。
		CapabilityJSON: MarshalCapabilitySelection(opt.Selection)})
	o.svc.beginTranscript(as.ID, agent.ID, spec.ID, cwd, opt.WorkspaceTemporary) // wire transcript header（未启用时 no-op）
	evt := o.svc.publish(BusSessionCreated, as.ID, map[string]any{
		"session_id": as.ID, "agent_id": agent.ID, "runtime": spec.ID,
	})
	return &CreateResult{SessionID: as.ID, AgentID: agent.ID, Runtime: spec.ID, Status: SessionStatusInProgress, EventSeq: evt.Seq}, nil
}

// ensureSession 返回活跃会话；不在活跃集合但平台有记录时按需重建。
// 恢复次序（P3 F2/F3，§5.6）：
//  1. 原生 resume（首选）：acquire conn → 按能力 LoadSession（opencode load=true）或
//     ResumeSession（dsh resume=true）恢复同一 provider session → reason=restart。
//  2. handoff memo 兜底（原生不可用时）：底座未声明 load/resume 能力、无 provider session
//     （旧数据）、agent 现路由 runtime ≠ 会话原 runtime（跨 runtime 续聊）、或原生恢复失败 →
//     在该 runtime 建 fresh session + 把历史压成 bounded memo 挂到首条 prompt（reason=handoff），
//     并更新 rec 的 provider_session_id / runtime_hint（记忆与 sqlite 同步）。
//
// **可续聊性只看会话是否还存在**（ADR-4：执行可丢弃、会话平台拥有）：上一轮的终态
// （completed / failed / cancelled）是轮次结果的事实，不是「会话作废」的判据——旧语义把终态会话
// 按未找到处理，会让 daemon 每重启一次就作废全部历史会话（容器里 `docker restart` 即触发）。
// 只有真正不可用的情形才报错：会话不存在（session_not_found）、agent 已删（agent_not_found）、
// runtime 未注册（runtime_not_found）、原生与 memo 都不可用（resume_unavailable）。
// memo 预算由 SetHandoffMaxChars 控制（0 = 关闭 memo 兜底 → 原生不可用时回退明确错误）。
//
// **重开即迁移（M3-6）**：重建时按**当前定义**重算代际指纹，于是"改了 skill 正文/MCP 定义后
// 会话在进程死掉重开"这一路径天然落在最新代际上；算不出或按新代际重开失败 → 回退钉住的旧代际
// 继续跑（可用性优先，见 generation.go 的四条纪律）。
func (o *Orchestrator) ensureSession(ctx context.Context, sessionID string) (*ActiveSession, error) {
	if as := o.session(sessionID); as != nil {
		return as, nil
	}
	rec := o.svc.sessionRec(sessionID)
	if rec == nil {
		return nil, acl.NewError(acl.ClassRequest, "session_not_found",
			fmt.Sprintf("未知会话 %q", sessionID), nil)
	}
	agent, ok := o.svc.AgentByID(rec.AgentID)
	if !ok {
		// agent 已被删除/未注册（归属不可变：平台不自动改绑到别的 agent，也不静默换权限面），
		// 但错误必须给出路——同一个 id 重新注册即可复活绑定（会话记的是 agent id），
		// 或导出历史另开新会话。
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found",
			fmt.Sprintf("会话 %q 绑定的 agent %q 已不存在：用同一个 id 重新注册该 agent 后可继续，或导出本会话历史另开新会话",
				sessionID, rec.AgentID), nil)
	}
	// runtime 选择：**构建期选定的那一个**。
	//
	// 历史：这里曾在「会话原 runtime（`rec.RuntimeHint`）」与「agent 现路由首选」之间做迁移判定
	// （跨 runtime 续聊，§18 F3）。产品钉在单一底座之后没有第二个候选——若会话记录里的原生 runtime
	// 与当前不同（改过构建 / 老数据），下面 `connectSession` 的 `sameRuntime` 判据自然为 false，
	// 走 fresh + handoff memo 重灌，会话照样能续。**这条路径要有真实用例**（M5 验收项）。
	runtimeID, err := o.svc.ResolveRuntime()
	if err != nil {
		return nil, err
	}
	spec, ok := o.reg.Get(runtimeID)
	if !ok {
		return nil, acl.NewError(acl.ClassRequest, "runtime_not_found",
			fmt.Sprintf("runtime %q 未注册（agent %q 无法建会话）", runtimeID, rec.AgentID), nil)
	}
	cwd := rec.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	// **恢复路径也要带上会话钉住的选定**：漏改这一处的后果是"重启后能力集悄悄换一套"。
	// 与指纹的分工：指纹决定挂到哪个代际（连接复用），选定决定下发哪些 MCP。
	sel := ParseCapabilitySelection(rec.CapabilityJSON)
	// 目标代际：有声明就**按当前定义现算**——重开即迁移，落下就是最新代际（M3-6）。
	// 缺省路径（sel == nil）现算结果恒为空 → 保持钉住的空指纹 → 与引入代际之前逐字一致（R11）。
	pinned := rec.Fingerprint
	want := o.svc.capabilityFingerprint(agent, spec.ID, sel)
	if want == "" {
		want = pinned
	}
	as, reason, err := o.connectSession(ctx, rec, agent, spec, cwd, sel, want)
	if err != nil && want != pinned {
		// ④ 迁移失败 → 回退钉住的旧代际继续跑（可用性优先）。**不**把会话变成不可用：
		// "能力没换过来"是诊断层面的事实，不是这一轮发不出去的理由。
		failed := err
		as, reason, err = o.connectSession(ctx, rec, agent, spec, cwd, sel, pinned)
		if err == nil {
			o.noteGenerationFallback(rec.AgentID, sessionID, spec.ID, pinned, want, failed)
		}
	}
	if err != nil {
		return nil, err
	}
	// 事件语义：重开时**真的换了代际** → reason=generation（前端据此提示"能力已更新"，
	// 与"进程重启后原生续接"区分开）；否则沿用 restart/handoff。
	if want != pinned && as.Fingerprint == want {
		reason = "generation"
	}
	o.attachSession(as, reason)
	return as, nil
}

// connectSession 在 `fingerprint` 指定的代际上重开一条会话（原生 resume 优先、handoff memo
// 兜底），并把「实际挂上的代际」写回会话记录。**不碰活跃集合**（装配由 attachSession 做）——
// 于是"进程死后重建"与"轮次边界迁移"两条路径共用同一段开会话逻辑，不会各自漂移。
//
// 记录用**副本**更新后整体写回（`registerSession` 会同时持久化）：中途失败时调用方手里的
// rec 保持原样，回退旧代际那一次才不会被半成品污染。
func (o *Orchestrator) connectSession(ctx context.Context, recIn *SessionRec, agent *AgentRec,
	spec runtime.Spec, cwd string, sel *CapabilitySelection, fingerprint string) (*ActiveSession, string, error) {
	rec := *recIn
	// **恢复路径也要带上会话钉住的指纹**：漏改这一处的后果是"重启后能力集悄悄换了一套"。
	rc, err := o.acquire(ctx, agent, spec, fingerprint)
	if err != nil {
		return nil, "", err
	}
	// **恢复路径必须用会话钉住的选定**（不是 agent 当前声明、更不是全量）：它从
	// `sessions.capability_json` 读回。漏了这一步的后果是会话重启后注入集悄悄换一套，
	// 而且没有任何报错——用户只会发现"昨天能用的 MCP 今天不见了"。
	opts := acl.SessionOptions{Cwd: cwd, McpServers: o.svc.McpServersFor(agent, spec.ID, sel)}
	caps := rc.client.Capabilities()
	sameRuntime := spec.ID == rec.RuntimeHint && rec.RuntimeHint != ""
	// `nativeID` 是**平台侧的值**（`rt_` 壳）；给底座的必须是**原文**。
	//
	// U1：`rt_ses_…` 底座认不出来 → `LoadSession` 失败 → 打一条 WARN 后走 handoff memo 兜底
	// （ADR-4 明确允许的降级）——用户侧只看到"上下文丢了"，**不响**。所以这里严格解包，
	// 失败即返回错误，不降级（"让错的那次在边界上当场失败"是这两条函数存在的全部理由）。
	nativeID := rec.ProviderSessionID
	rawNativeID := ""
	if nativeID != "" {
		raw, uerr := objid.UnwrapProvider(nativeID)
		if uerr != nil {
			return nil, "", uerr
		}
		rawNativeID = raw
	}
	resumeOK := sameRuntime && rawNativeID != "" &&
		(caps.Raw.LoadSession || caps.Raw.SessionCapabilities.Resume != nil)

	var sess *acl.Session
	var memo string
	reason := "restart"
	switch {
	case resumeOK:
		if caps.Raw.LoadSession {
			sess, err = rc.client.LoadSession(ctx, rawNativeID, opts)
		} else {
			sess, err = rc.client.ResumeSession(ctx, rawNativeID, opts)
		}
		if err != nil {
			glog.Warnw(ctx, "原生恢复失败，转 handoff memo 兜底", "session", rec.ID,
				"runtime", spec.ID, "provider_session", nativeID, "err", err)
			resumeOK = false
		}
	}
	if !resumeOK {
		if o.handoffMaxChars <= 0 {
			return nil, "", acl.NewError(acl.ClassRequest, "resume_unavailable",
				fmt.Sprintf("会话 %q 无法原生恢复（runtime %q 无 load/resume 或 provider 会话无效），且 memo 兜底关闭",
					rec.ID, spec.ID), nil)
		}
		sess, err = rc.client.NewSession(ctx, opts)
		if err != nil {
			return nil, "", err
		}
		memo = o.svc.HandoffMemo(rec.ID, o.handoffMaxChars)
		reason = "handoff"
		// W2（最容易漏的一处：它在"原生恢复失败/迁移"分支里）：新会话的底座原文同样要加壳，
		// 否则换底座/换会话之后 id 会退化成裸值——库里的形状就变成了两种。
		wrappedNew, werr := objid.WrapProvider(sess.ID())
		if werr != nil {
			return nil, "", werr
		}
		// 迁移登记：fresh session 顶替原 provider id；跨 runtime 时同步 runtime_hint。
		if nativeID != wrappedNew || spec.ID != rec.RuntimeHint {
			rec.ProviderSessionID = wrappedNew
			rec.RuntimeHint = spec.ID
		}
	}
	// 代际落账：**实际挂上的**指纹写回记录（迁移后这里就是新值；回退时是旧值）。
	if rec.Fingerprint != fingerprint {
		rec.Fingerprint = fingerprint
	}
	o.svc.registerSession(&rec)
	as := &ActiveSession{ID: rec.ID, AgentID: agent.ID, Runtime: spec.ID, Fingerprint: fingerprint,
		client: rc.client, sess: sess, providerID: sess.ID(),
		proj: runtime.NewProjection(spec, sess), memo: memo}
	// 会话级模型在恢复后重新断言一次（平台记录是权威意图；原生恢复未必带回该状态）：
	// **best-effort** —— 恢复不是用户在选模型，运行时若已不再提供该值（换了 provider/档案），
	// 只留痕 + 标灰，不让老会话因为一个失效取值而打不开。
	//
	// `proj == nil` = 该 runtime 没有会话级投影（`NewProjection` 按 runtime id 派生；测试桩
	// 就是这一类），此时没有模型语义可言——**必须显式跳过**，否则对 nil 接口调方法直接 panic。
	if as.proj == nil {
		return as, reason, nil
	}
	if rec.Model != "" {
		if _, err := as.proj.ApplyModel(ctx, runtime.ModelConfig{Model: rec.Model}); err != nil {
			if ae, ok := acl.As(err); !ok || !strings.HasPrefix(ae.Code, "unsupported_") {
				o.svc.catalog.markRejected(agent.ID, rec.Model, modelApplyReason(err))
			}
			glog.Warnw(ctx, "恢复后重放会话模型失败（忽略，继续用运行时当前值）",
				"session", rec.ID, "runtime", spec.ID, "model", rec.Model, "err", err)
		}
	} else if len(as.proj.ConfigOptions()) > 0 {
		o.svc.catalog.put(agent.ID, spec.ID, as.proj.ConfigOptions())
	}
	return as, reason, nil
}

// attachSession 把句柄装进活跃集合（替换同 id 的旧句柄），发布 session.resumed。
//
// 被替换的旧句柄：**只作废它自己**（摘 provider 映射 + best-effort 关掉那条原生会话），
// 绝不动它所在的连接——旧代际的暖进程可能还在服务别的会话，代际回收是 M3-7 的事。
// 关掉旧原生会话是为了让底座尽快释放它那份资源（会话级，不是进程级）。
func (o *Orchestrator) attachSession(as *ActiveSession, reason string) {
	o.mu.Lock()
	old := o.sessions[as.ID]
	o.sessions[as.ID] = as
	o.byProvider[as.providerID] = as.ID
	if old != nil && old != as && old.providerID != as.providerID {
		if o.byProvider[old.providerID] == old.ID {
			delete(o.byProvider, old.providerID)
		}
	}
	o.mu.Unlock()
	if old != nil && old != as {
		old.mu.Lock()
		old.dead = true // 旧句柄作废：拿到它旧指针的路径会走重建（而不是用一个已被顶替的会话）
		old.mu.Unlock()
		if c, ok := old.sess.(*acl.Session); ok && c != nil {
			// 关会话是收尾动作：用**脱离请求**的 ctx（本轮 ctx 可能已取消）并带上限等待，
			// 免得底座吞掉 session/close 时把这一轮拖住。
			cctx, cancel := context.WithTimeout(context.Background(), agentCancelTimeout)
			_ = c.Close(cctx)
			cancel()
		}
	}
	// §6：执行上下文刚被重建（本次拿到了新的连接/会话）→ 会话执行代际自增。
	// 排队输入比对的就是这个值：重建之后，为旧上下文排的队不再成立（见 inputqueue.go）。
	o.svc.bumpGeneration(as.ID)
	o.svc.markSession(as.ID, SessionStatusInProgress)
	payload := map[string]any{
		"session_id": as.ID, "agent_id": as.AgentID, "runtime": as.Runtime, "reason": reason,
	}
	if reason == "generation" {
		// 迁移事件：把**换到了哪一代**带上（审计要把"第 N 轮到第 M 轮用的哪套能力"串起来）。
		payload["capability_fingerprint"] = as.Fingerprint
	}
	o.svc.publish(BusSessionResumed, as.ID, payload)
}

// applyModelOnSession 把模型应用到一条会话上（建会话与「会话内改模型」共用同一路径）。
//
// 语义（决策记录）：**用户显式指定的取值，运行时要么接受、要么报错**——绝不静默回落默认模型：
//   - 运行时未广告模型项（未配置 provider 的 opencode 等）→ model_not_supported；
//   - 运行时拒绝该取值（协议要求值必须来自广告列表）→ 原样上抛并记入「不可用」反馈；
//   - 成功 → 用返回的全量状态刷新平台目录缓存。
//
// model.Model 为空 = 不下发设置（跟随运行时默认），永远成功。
// 思考档位不在平台侧下发（见 runtime.ModelConfig 说明）。
func (o *Orchestrator) applyModelOnSession(ctx context.Context, as *ActiveSession,
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
		glog.Debugw(ctx, "模型取值不在当前广告列表中，交由运行时裁决",
			"session", as.ID, "model", model.Model)
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
	glog.Warnw(ctx, "apply model rejected by runtime",
		"session", as.ID, "runtime", spec.ID, "model", model.Model, "err", err)
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
	for _, as := range o.sessions {
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
