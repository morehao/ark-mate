package manager

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/runtime"
)

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
	if err := o.applyModelOnConversation(ctx, as, as.AgentID, spec, as.sess, model); err != nil {
		return true, err
	}
	return true, nil
}

// StartConversation 在 agent 路由选定的 runtime 上建立持久 ACP 会话（conversation/new，绝对 cwd），
// 并发布 conversation.created。不发起 prompt；Prompt 负责每轮。
// 路由 = ResolveRuntime（§7 FirstAvailable：候选顺序 × 已注册 × 命令可执行）。
func (o *Orchestrator) StartConversation(ctx context.Context, agentID string, opt StartOptions) (*CreateResult, error) {
	agent, ok := o.svc.AgentByID(agentID)
	if !ok {
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", agentID), nil)
	}
	runtimeID, err := o.svc.ResolveRuntime(agent)
	if err != nil {
		return nil, err
	}
	return o.startConversationOn(ctx, agent, runtimeID, opt)
}

// startConversationOn 在指定 runtime 上建立持久 ACP 会话（StartConversation 的路由首候选、
// RunOnce 的显式 --runtime 都汇聚于此）。返回的 CreateResult.Runtime 为实际选中的 runtime。
func (o *Orchestrator) startConversationOn(ctx context.Context, agent *AgentRec, runtimeID string, opt StartOptions) (*CreateResult, error) {
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
	convID := opt.ConversationID
	if convID == "" {
		convID = uuid.NewString()
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

	// P3 双 id（D-1，用户已授权破坏性变更）：会话对外 id = 平台 uuid（隐藏底座 id 形状、
	// 跨重启稳定定位）；原生 provider id 存 sess/ProviderSessionID 列，供恢复承载（load/resume）。
	as := &ActiveConversation{ID: convID, AgentID: agent.ID, Runtime: spec.ID, Fingerprint: fingerprint,
		client: rc.client, sess: sess, proj: runtime.NewProjection(spec, sess)}
	// 会话级模型应用（运行时派生模型面的落地，决策记录）：
	//   - 值必须来自运行时自报目录（category=model），set 的成败即「可用性」判据；
	//   - 运行时**拒绝**该值 → 建会话失败（错误上抛给调用方，绝不静默回落到底座默认模型）；
	//   - unsupported_*（运行时未广告模型配置项）→ 忽略（无模型语义）；
	//   - 成功 → 用返回的**全量**配置项刷新平台目录缓存（顺带清掉曾失败值的标记）。
	if err := o.applyModelOnConversation(ctx, as, agent.ID, spec, sess, opt.Model); err != nil {
		_ = sess.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	o.mu.Lock()
	o.conversations[as.ID] = as
	o.byProvider[sess.ID()] = as.ID
	o.mu.Unlock()

	o.svc.registerConversation(&ConversationRec{ID: as.ID, AgentID: agent.ID, Cwd: cwd,
		WorkspaceTemporary: opt.WorkspaceTemporary, Provider: agent.Provider,
		Model: opt.Model.Model, RuntimeHint: spec.ID, Fingerprint: fingerprint,
		ProviderSessionID: sess.ID(), Status: ConversationStatusInProgress,
		// 钉住的选定**与指纹一起**落库（M3-3 起）：两者必须同源，缺一则恢复路径
		// 要么注入集不对、要么代际挂错。
		CapabilityJSON: MarshalCapabilitySelection(opt.Selection)})
	o.svc.beginTranscript(as.ID, agent.ID, spec.ID, cwd, opt.WorkspaceTemporary) // wire transcript header（未启用时 no-op）
	evt := o.svc.publish(BusSessionCreated, as.ID, map[string]any{
		"session_id": as.ID, "agent_id": agent.ID, "runtime": spec.ID,
	})
	return &CreateResult{ConversationID: as.ID, AgentID: agent.ID, Runtime: spec.ID, Status: ConversationStatusInProgress, EventSeq: evt.Seq}, nil
}

// ensureConversation 返回活跃会话；不在活跃集合但平台有记录时按需重建。
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
// 只有真正不可用的情形才报错：会话不存在（conversation_not_found）、agent 已删（agent_not_found）、
// runtime 未注册（runtime_not_found）、原生与 memo 都不可用（resume_unavailable）。
// memo 预算由 SetHandoffMaxChars 控制（0 = 关闭 memo 兜底 → 原生不可用时回退明确错误）。
func (o *Orchestrator) ensureConversation(ctx context.Context, conversationID string) (*ActiveConversation, error) {
	if as := o.conversation(conversationID); as != nil {
		return as, nil
	}
	rec := o.svc.conversationRec(conversationID)
	if rec == nil {
		return nil, acl.NewError(acl.ClassRequest, "conversation_not_found",
			fmt.Sprintf("未知会话 %q", conversationID), nil)
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
	// **恢复路径也要带上会话钉住的指纹**：漏改这一处的后果是"重启后能力集悄悄换了一套"。
	rc, err := o.acquire(ctx, agent, spec, rec.Fingerprint)
	if err != nil {
		return nil, err
	}
	// **恢复路径必须用会话钉住的选定**（不是 agent 当前声明、更不是全量）：它从
	// `conversations.capability_json` 读回。漏了这一步的后果是会话重启后注入集悄悄换一套，
	// 而且没有任何报错——用户只会发现"昨天能用的 MCP 今天不见了"。
	// 与 rec.Fingerprint 的分工：指纹决定挂到哪个代际（连接复用），选定决定下发哪些 MCP。
	sel := ParseCapabilitySelection(rec.CapabilityJSON)
	opts := acl.SessionOptions{Cwd: cwd, McpServers: o.svc.McpServersFor(agent, spec.ID, sel)}
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
	as := &ActiveConversation{ID: rec.ID, AgentID: agent.ID, Runtime: spec.ID, Fingerprint: rec.Fingerprint,
		client: rc.client, sess: sess, proj: runtime.NewProjection(spec, sess), memo: memo}
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
	// §6：执行上下文刚被重建（本次拿到了新的连接/会话）→ 会话执行代际自增。
	// 排队输入比对的就是这个值：重建之后，为旧上下文排的队不再成立（见 inputqueue.go）。
	o.svc.bumpGeneration(as.ID)
	o.svc.markConversation(as.ID, ConversationStatusInProgress)
	o.svc.publish(BusSessionResumed, as.ID, map[string]any{
		"session_id": as.ID, "agent_id": as.AgentID, "runtime": spec.ID, "reason": reason,
	})
	return as, nil
}

// applyModelOnConversation 把模型应用到一条会话上（建会话与「会话内改模型」共用同一路径）。
//
// 语义（决策记录）：**用户显式指定的取值，运行时要么接受、要么报错**——绝不静默回落默认模型：
//   - 运行时未广告模型项（未配置 provider 的 opencode 等）→ model_not_supported；
//   - 运行时拒绝该取值（协议要求值必须来自广告列表）→ 原样上抛并记入「不可用」反馈；
//   - 成功 → 用返回的全量状态刷新平台目录缓存。
//
// model.Model 为空 = 不下发设置（跟随运行时默认），永远成功。
// 思考档位不在平台侧下发（见 runtime.ModelConfig 说明）。
func (o *Orchestrator) applyModelOnConversation(ctx context.Context, as *ActiveConversation,
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
