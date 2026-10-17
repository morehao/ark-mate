package manager

import (
	"context"
	"fmt"
	"github.com/morehao/ark-mate/internal/domain/session"
	"os"
	"strings"

	"github.com/morehao/ark-mate/internal/domain/provider"
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
	spec, ok := o.reg.Get(as.Runtime)
	if !ok {
		return false, nil
	}
	if err := o.applyModelOnSession(ctx, as, spec, as.sess, model); err != nil {
		return true, err
	}
	return true, nil
}

// StartSession 在**构建期选定的** runtime 上建立持久 ACP 会话（session/new，绝对 cwd），
// 并发布 session.created。不发起 prompt；Prompt 负责每轮。
//
// 底座不是这次调用的入参：它是二进制的属性（pkg/runtime/build.go）。这里只做"取那一个 + 校验
// 已装配"（`ResolveRuntime`）。
func (o *Orchestrator) StartSession(ctx context.Context, opt StartOptions) (*CreateResult, error) {
	return o.startSessionOn(ctx, opt)
}

// startSessionOn 在**构建期选定的** runtime 上建立持久 ACP 会话（Service.CreateSession
// 与 RunOnce 都汇聚于此）。返回的 CreateResult.Runtime 为实际选中的 runtime。
func (o *Orchestrator) startSessionOn(ctx context.Context, opt StartOptions) (*CreateResult, error) {
	// **单点铸码**（P4a）：`runtime_not_found` 只由执行面铸（`runtimeconn.SelectedSpec`）。
	// 这里原来先 `ResolveRuntime()` 再自己 `reg.Get` 判一次——第二次判定**永远不成立**
	// （id 就是刚解析出来的那个），而它铸的是**同一个码的第二份文本**：两份文本迟早只改一处。
	spec, err := o.svc.runtimePlane.SelectedSpec()
	if err != nil {
		return nil, err
	}
	// 能力选定校验（M3-3）：**在建连之前**。位次很关键——它既要在已知 runtime 之后
	// （可达性是 runtime 相关的），又要在任何副作用之前（不建会话/不拉进程/不写库）。
	// 不可达就报错而非静默丢弃：静默丢弃等于让用户以为"选了就生效了"。
	if err := o.svc.validateSelection(opt.Selection, spec.ID); err != nil {
		return nil, err
	}
	// 模型解析（v26 定案「辰」，设计 §5.4）：请求里给的是**平台模型行 id**（空 = 用平台默认那一行），
	// 这里解析成**行**并现算底座认的不透明值。位次刻意与能力校验并列——都要在**任何副作用之前**
	// （不建会话/不拉进程/不写库）：一个查不到的模型 id 不该先把底座拉起来再失败。
	platformModelID, modelCfg, err := o.svc.resolveSessionModel(ctx, spec.ID, opt.Model.Model)
	if err != nil {
		return nil, err
	}
	// 能力指纹由**选定**现算（M3）：缺省路径 opt.Selection == nil → 空指纹 →
	// 代际键与引入代际之前逐字相同（R11）。指纹与下面注入用的 opt.Selection 同源，
	// 不存在"按 A 算指纹、按 B 注入"的可能。
	fingerprint := o.svc.capabilityFingerprint(spec.ID, opt.Selection)
	rc, err := o.acquire(ctx, spec, fingerprint)
	if err != nil {
		o.svc.runtimePlane.SetState(session.AgentStateStopped)
		// A3：底座起不来 / 握手失败时记诊断（含该阶段的 stderr 尾巴），供
		// `GET /runtime/diagnostics` 外露。这一句原来在 `StartAgent`（v19 之前那个"暖启动"入口，
		// 已随 agent 面删除）；今天唯一的启动路径就是"建会话把它拉起来"，所以落在这里。
		if ae := acl.Classify(err); ae != nil {
			o.svc.recordRuntimeError(opt.SessionID, ae, ae.StderrTail)
		}
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
	// P2 MCP 注入：平台注册表 MCP 子集按「会话选定 > 全量」+ runtime 传输能力过滤后
	// 随 session/new 下发（§5.2/§5.3；会话级选定见 M3）。**必须传 opt.Selection**：
	// 指纹就是按它算出来的，注入若用别的口径，会话拿到的东西会与代际台账不符。
	sess, err := rc.Client.NewSession(ctx, acl.SessionOptions{
		Cwd:        cwd,
		McpServers: o.svc.McpServersFor(spec.ID, opt.Selection),
	})
	if err != nil {
		return nil, err
	}

	// P3 双 id（D-1，用户已授权破坏性变更）：会话对外 id = 平台铸造的 `ses_<ULID>`（隐藏底座 id 形状、
	// 跨重启稳定定位）；原生 provider id 存 sess/RuntimeSessionID 列，供恢复承载（load/resume）。
	as := &ActiveSession{ID: convID, Runtime: spec.ID, Fingerprint: fingerprint,
		client: rc.Client, sess: sess, providerID: sess.ID(), proj: runtime.NewProjection(spec, sess)}
	// 会话级模型应用（运行时派生模型面的落地，决策记录）：
	//   - 值由 `modelCfg` 给出（平台行 → adapter 现算，见 `resolveSessionModel`）；
	//   - 运行时**拒绝**该值 → 建会话失败（错误上抛给调用方，绝不静默回落到底座默认模型）；
	//   - unsupported_*（运行时未广告模型配置项）→ 忽略（无模型语义）；
	//   - 成功 → 用返回的**全量**配置项刷新平台目录缓存（顺带清掉曾失败值的标记）。
	if err := o.applyModelOnSession(ctx, as, spec, sess, modelCfg); err != nil {
		_ = sess.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	// W1：底座原文 → 平台侧的值（`rt_` 壳）。**失败即返回错误、不建 rec**——把裸 id 落库会让
	// "平台 id 与底座 id 形状可分"这条不变式当场失效，而那是静默的（守卫② 事后才发现）。
	// `as.providerID` 刻意保留**原文**：它是进程内映射 `byProvider` 的键，与 ACP 回调同源。
	providerSessID, err := objid.WrapRuntime(sess.ID())
	if err != nil {
		_ = sess.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	o.mu.Lock()
	o.sessions[as.ID] = as
	o.byProvider[sess.ID()] = as.ID
	o.mu.Unlock()

	o.svc.registerSession(&SessionRec{ID: as.ID, Cwd: cwd,
		WorkspaceID: opt.WorkspaceID, WorkspaceTemporary: opt.WorkspaceTemporary,
		// **落库的是身份**（平台模型行 id），不是运行时值——定案「辰」的核心。
		ModelID: platformModelID, RuntimeID: spec.ID, Fingerprint: fingerprint, Origin: opt.Origin,
		RuntimeSessionID: providerSessID, Status: session.SessionStatusInProgress,
		// 钉住的选定**与指纹一起**落库（M3-3 起）：两者必须同源，缺一则恢复路径
		// 要么注入集不对、要么代际挂错。
		CapabilityJSON: MarshalCapabilitySelection(opt.Selection)})
	o.svc.beginTranscript(as.ID, spec.ID, cwd, opt.WorkspaceTemporary) // wire transcript header（未启用时 no-op）
	evt := o.svc.publish(session.BusSessionCreated, as.ID, map[string]any{
		"session_id": as.ID, "runtime": spec.ID,
	})
	return &CreateResult{SessionID: as.ID, Runtime: spec.ID, Status: session.SessionStatusInProgress, EventSeq: evt.Seq}, nil
}

// ensureSession 返回活跃会话；不在活跃集合但平台有记录时按需重建。
// 恢复次序（P3 F2/F3，§5.6）：
//  1. 原生 resume（首选）：acquire conn → 按该底座声明的能力走 LoadSession（load=true）或
//     ResumeSession（resume=true）恢复同一 provider session → reason=restart。
//  2. handoff memo 兜底（原生不可用时）：底座未声明 load/resume 能力、无 provider session
//     （旧数据）、会话记录的 runtime ≠ 当前构建期固定的那个（跨 runtime 续聊）、或原生恢复失败 →
//     在该 runtime 建 fresh session + 把历史压成 bounded memo 挂到首条 prompt（reason=handoff），
//     并更新 rec 的 runtime_session_id / runtime_id（记忆与 sqlite 同步）。
//
// **可续聊性只看会话是否还存在**（ADR-4：执行可丢弃、会话平台拥有）：上一轮的终态
// （completed / failed / cancelled）是轮次结果的事实，不是「会话作废」的判据——旧语义把终态会话
// 按未找到处理，会让 daemon 每重启一次就作废全部历史会话（容器里 `docker restart` 即触发）。
// 只有真正不可用的情形才报错：会话不存在（session_not_found）、runtime 未注册
// （runtime_not_found）、原生与 memo 都不可用（resume_unavailable）。
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
	// runtime 选择：**构建期选定的那一个**。
	//
	// 历史：这里曾在「会话原 runtime（`rec.RuntimeID`）」与「agent 现路由首选」之间做迁移判定
	// （跨 runtime 续聊，§18 F3）。产品钉在单一底座之后没有第二个候选——若会话记录里的原生 runtime
	// 与当前不同（改过构建 / 老数据），下面 `connectSession` 的 `sameRuntime` 判据自然为 false，
	// 走 fresh + handoff memo 重灌，会话照样能续。**这条路径要有真实用例**（M5 验收项）。
	// 同 `startSessionOn`：`runtime_not_found` 只由执行面铸（单点）。
	spec, err := o.svc.runtimePlane.SelectedSpec()
	if err != nil {
		return nil, err
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
	want := o.svc.capabilityFingerprint(spec.ID, sel)
	if want == "" {
		want = pinned
	}
	as, reason, err := o.connectSession(ctx, rec, spec, cwd, sel, want)
	if err != nil && want != pinned {
		// ④ 迁移失败 → 回退钉住的旧代际继续跑（可用性优先）。**不**把会话变成不可用：
		// "能力没换过来"是诊断层面的事实，不是这一轮发不出去的理由。
		failed := err
		as, reason, err = o.connectSession(ctx, rec, spec, cwd, sel, pinned)
		if err == nil {
			o.noteGenerationFallback(sessionID, spec.ID, pinned, want, failed)
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
func (o *Orchestrator) connectSession(ctx context.Context, recIn *SessionRec,
	spec runtime.Spec, cwd string, sel *CapabilitySelection, fingerprint string) (*ActiveSession, string, error) {
	rec := *recIn
	// **恢复路径也要带上会话钉住的指纹**：漏改这一处的后果是"重启后能力集悄悄换了一套"。
	rc, err := o.acquire(ctx, spec, fingerprint)
	if err != nil {
		return nil, "", err
	}
	// **恢复路径必须用会话钉住的选定**（不是全量）：它从
	// `sessions.capability_json` 读回。漏了这一步的后果是会话重启后注入集悄悄换一套，
	// 而且没有任何报错——用户只会发现"昨天能用的 MCP 今天不见了"。
	opts := acl.SessionOptions{Cwd: cwd, McpServers: o.svc.McpServersFor(spec.ID, sel)}
	caps := rc.Client.Capabilities()
	sameRuntime := spec.ID == rec.RuntimeID && rec.RuntimeID != ""
	// `nativeID` 是**平台侧的值**（`rt_` 壳）；给底座的必须是**原文**。
	//
	// U1：`rt_ses_…` 底座认不出来 → `LoadSession` 失败 → 打一条 WARN 后走 handoff memo 兜底
	// （ADR-4 明确允许的降级）——用户侧只看到"上下文丢了"，**不响**。所以这里严格解包，
	// 失败即返回错误，不降级（"让错的那次在边界上当场失败"是这两条函数存在的全部理由）。
	nativeID := rec.RuntimeSessionID
	rawNativeID := ""
	if nativeID != "" {
		raw, uerr := objid.UnwrapRuntime(nativeID)
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
			sess, err = rc.Client.LoadSession(ctx, rawNativeID, opts)
		} else {
			sess, err = rc.Client.ResumeSession(ctx, rawNativeID, opts)
		}
		if err != nil {
			glog.Warnw(ctx, "原生恢复失败，转 handoff memo 兜底", "session", rec.ID,
				"runtime", spec.ID, "runtime_session", nativeID, "err", err)
			resumeOK = false
		}
	}
	if !resumeOK {
		if o.handoffMaxChars <= 0 {
			return nil, "", acl.NewError(acl.ClassRequest, "resume_unavailable",
				fmt.Sprintf("会话 %q 无法原生恢复（runtime %q 无 load/resume 或 provider 会话无效），且 memo 兜底关闭",
					rec.ID, spec.ID), nil)
		}
		sess, err = rc.Client.NewSession(ctx, opts)
		if err != nil {
			return nil, "", err
		}
		memo = o.svc.handoffMemo(rec.ID, o.handoffMaxChars)
		reason = "handoff"
		// W2（最容易漏的一处：它在"原生恢复失败/迁移"分支里）：新会话的底座原文同样要加壳，
		// 否则换底座/换会话之后 id 会退化成裸值——库里的形状就变成了两种。
		wrappedNew, werr := objid.WrapRuntime(sess.ID())
		if werr != nil {
			return nil, "", werr
		}
		// 迁移登记：fresh session 顶替原 provider id；跨 runtime 时同步 runtime_id。
		if nativeID != wrappedNew || spec.ID != rec.RuntimeID {
			rec.RuntimeSessionID = wrappedNew
			rec.RuntimeID = spec.ID
		}
	}
	// 代际落账：**实际挂上的**指纹写回记录（迁移后这里就是新值；回退时是旧值）。
	if rec.Fingerprint != fingerprint {
		rec.Fingerprint = fingerprint
	}
	o.svc.registerSession(&rec)
	as := &ActiveSession{ID: rec.ID, Runtime: spec.ID, Fingerprint: fingerprint,
		client: rc.Client, sess: sess, providerID: sess.ID(),
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
	if rec.ModelID != "" {
		// v26：`rec.ModelID` 是**平台行 id**（库里不再存底座词表的串），所以恢复时必须**重新现算**
		// 运行时值。算不出来（行被删了 / 存量回填还没跑到）→ **跳过重放**并留痕：恢复不是用户在
		// 选模型，不该让老会话因为一个失效引用而打不开（与上面 best-effort 的口径一致）。
		_, recCfg, rerr := o.svc.resolveSessionModel(ctx, spec.ID, rec.ModelID)
		if rerr != nil || recCfg.Model == "" {
			glog.Warnw(ctx, "恢复时按平台模型行重算运行时值失败（跳过重放，继续用运行时当前值）",
				"session", rec.ID, "runtime", spec.ID, "model", rec.ModelID, "err", rerr)
		} else if _, err := as.proj.ApplyModel(ctx, recCfg); err != nil {
			if ae, ok := acl.As(err); !ok || !strings.HasPrefix(ae.Code, "unsupported_") {
				o.svc.catalog.markRejected(spec.ID, recCfg.Model, provider.ModelApplyReason(err))
			}
			glog.Warnw(ctx, "恢复后重放会话模型失败（忽略，继续用运行时当前值）",
				"session", rec.ID, "runtime", spec.ID, "model", recCfg.Model, "err", err)
		}
	} else if len(as.proj.ConfigOptions()) > 0 {
		o.svc.catalog.put(spec.ID, as.proj.ConfigOptions())
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
	o.svc.markSession(as.ID, session.SessionStatusInProgress)
	payload := map[string]any{
		"session_id": as.ID, "runtime": as.Runtime, "reason": reason,
	}
	if reason == "generation" {
		// 迁移事件：把**换到了哪一代**带上（审计要把"第 N 轮到第 M 轮用的哪套能力"串起来）。
		payload["capability_fingerprint"] = as.Fingerprint
	}
	o.svc.publish(session.BusSessionResumed, as.ID, payload)
}

// applyModelOnSession 把模型应用到一条会话上（建会话与「会话内改模型」共用同一路径）。
//
// 语义（决策记录）：**用户显式指定的取值，运行时要么接受、要么报错**——绝不静默回落默认模型：
//   - 运行时未广告模型项（该底座未配置任何 provider 等）→ model_not_supported；
//   - 运行时拒绝该取值（协议要求值必须来自广告列表）→ 原样上抛并记入「不可用」反馈；
//   - 成功 → 用返回的全量状态刷新平台目录缓存。
//
// model.Model 为空 = 不下发设置（跟随运行时默认），永远成功。
// 思考档位不在平台侧下发（见 runtime.ModelConfig 说明）。
func (o *Orchestrator) applyModelOnSession(ctx context.Context, as *ActiveSession,
	spec runtime.Spec, sess acpSession, model runtime.ModelConfig) error {
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
		o.svc.catalog.clearRejection(spec.ID, model.Model)
		if len(applied) > 0 {
			o.svc.catalog.put(spec.ID, applied)
		}
		return nil
	}
	ae, _ := acl.As(err)
	if ae != nil && ae.Code == "unsupported_model" {
		return acl.NewError(acl.ClassRequest, "model_not_supported",
			"[model_not_supported] runtime "+spec.ID+" 未提供模型配置项，无法应用所选模型", nil)
	}
	// 运行时明确拒绝：记录该取值不可用（目录里标灰）后原样上抛。
	o.svc.catalog.markRejected(spec.ID, model.Model, provider.ModelApplyReason(err))
	glog.Warnw(ctx, "apply model rejected by runtime",
		"session", as.ID, "runtime", spec.ID, "model", model.Model, "err", err)
	return err
}

// liveConfigOptions 返回任一活跃会话的配置项（模型目录的**首选来源**：运行时此刻
// 的真实状态，零额外开销）。无活跃会话返回 false。
//
// 注：有活跃会话但配置项为空时同样返回 ok=true（空即运行时的真实答案：未配 provider/未登录），
// 不走 resume 兜底去「凑一份」目录。
func (o *Orchestrator) liveConfigOptions() ([]acl.ConfigOption, bool) {
	o.mu.Lock()
	var sess acpSession
	for _, as := range o.sessions {
		if as.dead || as.sess == nil {
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
