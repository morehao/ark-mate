package manager

import (
	"context"

	"github.com/morehao/ark-forge/pkg/acl"
)

// ——— M3-6：轮次边界的代际迁移（ADR-13） ———
//
// 会话钉住一个代际（能力指纹 = 解析后的整套能力）。定义文件被改动之后（改了 skill 正文、加了
// MCP、禁用了某个 server…），**下一轮的边界**才把会话迁到新代际——不是"下一个会话"，也不是
// "立即"：ACP 没有"给活会话换能力"的通道，轮次边界是协议内能做到的最细粒度。
//
// 四条纪律（每条都有对应测试，逐条都刻意不"顺手做多一点"）：
//
//	① **只在指纹真的变了时切**：切代际要走 resume/handoff，而 handoff 是**有损兜底**
//	   （`FORGE_HANDOFF_MAX_CHARS` 的存在就说明它是有损的）。每轮都切 = 每轮都可能丢上下文。
//	② **忙则顺延**：有在途轮次、或有未应答的权限/提问，都不切。前者是"绝不打断在途轮次"，
//	   后者是"别把用户正盯着的确认卡换到另一个进程去"。
//	③ **探针不可读时保守当忙**：读不到会话状态就不切。宁可多用一个旧代际（多占一点内存），
//	   也不要在不确定的状态下动一个可能正在跑的会话。
//	④ **迁移失败回退旧代际继续跑**：迁代际是**尽力而为**的优化，失败只留痕（日志 + agent 诊断），
//	   绝不让会话变成不可用——与 openwork 刻意不把超时升级为重启是同一个道理。
//
// 缺省路径（会话未声明能力选择）**不参与迁移**：那种会话的指纹恒为空、代际键与引入代际之前
// 逐字相同（R11），"改了定义下一轮生效"对它们本来就不是承诺。这是记得住的边界，不是疏漏。

// generationAction 一轮开始前对「要不要迁代际」的裁决。
type generationAction string

const (
	// genActionKeep 保持当前代际（指纹没变 / 未声明能力选择 / 句柄已作废交给重建路径）。
	genActionKeep generationAction = "keep"
	// genActionSwitch 指纹变了，且此刻可以切。
	genActionSwitch generationAction = "switch"
	// genActionDefer 指纹变了，但此刻不能切（忙 / 挂起项未决 / 探针不可读），下一轮再试。
	genActionDefer generationAction = "defer"
)

func (a generationAction) String() string { return string(a) }

// generationPlan 裁决结果。`From`/`To` 只在 switch/defer 时有意义（`keep` 时为空）。
type generationPlan struct {
	Action generationAction
	Reason string
	From   string
	To     string
}

// planGenerationSwitch 比较「会话钉住的代际」与「现在解析出来的代际」，给出本轮该怎么办。
//
// 只读、无副作用：好处是四条纪律里的三条（指纹比较、忙/挂起、探针可读性）都能在不拉进程的
// 前提下被单测逐条钉住。
func (o *Orchestrator) planGenerationSwitch(as *ActiveConversation, rec *ConversationRec) generationPlan {
	if as == nil || rec == nil {
		return generationPlan{Action: genActionKeep, Reason: "会话记录不可读：不动（保守）"}
	}
	// ②/③ 的探针：句柄状态。dead 的句柄不该在这里动——它已经作废，下一次 prompt 会走
	// ensureConversation 的重建路径，那条路径本来就按**当前**指纹重开（迁移由它兜住）。
	as.mu.Lock()
	busy, dead := as.busy, as.dead
	as.mu.Unlock()
	if dead {
		return generationPlan{Action: genActionKeep, Reason: "句柄已作废：交给重建路径按当前指纹重开"}
	}
	sel := ParseCapabilitySelection(rec.CapabilityJSON)
	if sel == nil {
		return generationPlan{Action: genActionKeep, Reason: "未声明能力选择：按既有连接复用语义（不做代际迁移）"}
	}
	agent, ok := o.svc.AgentByID(rec.AgentID)
	if !ok {
		// 归属不可变：agent 被删是"会话不能打开"的事实，不是迁移问题（重开路径会给 agent_not_found）。
		return generationPlan{Action: genActionKeep, Reason: "agent 已不存在：不动"}
	}
	to := o.svc.capabilityFingerprint(agent, as.Runtime, sel)
	plan := generationPlan{From: as.Fingerprint, To: to}
	if to == "" {
		// 解析不出指纹（能力目录不可用等）：没有"新代际"可迁，保持。
		plan.Action = genActionKeep
		plan.Reason = "当前代际指纹解析不出：不动（保守）"
		return plan
	}
	if to == as.Fingerprint {
		plan.Action = genActionKeep
		plan.Reason = "指纹未变：不切（绝不每轮切）"
		return plan
	}
	if busy {
		plan.Action = genActionDefer
		plan.Reason = "有在途轮次：顺延到下一轮"
		return plan
	}
	// ③ 探针不可读 → 保守当忙。会话句柄缺 client/sess、或挂起项管理器缺失，都无法判断"现在
	// 是不是真的空闲"，那就当它忙。
	if as.client == nil || as.sess == nil || o.perm == nil || o.elic == nil {
		plan.Action = genActionDefer
		plan.Reason = "会话忙碌状态不可读：保守顺延"
		return plan
	}
	if o.perm.pendingCount(as.ID) > 0 || o.elic.pendingCount(as.ID) > 0 {
		plan.Action = genActionDefer
		plan.Reason = "有未应答的权限/提问：顺延到下一轮"
		return plan
	}
	plan.Action = genActionSwitch
	plan.Reason = "指纹变化：本轮边界迁到新代际"
	return plan
}

// maybeMigrateGeneration 在轮次边界把会话迁到当前指纹对应的代际，返回**本轮该用的句柄**。
//
// **永不返回错误**（纪律④）：迁代际是尽力而为的优化，失败就继续用旧代际——调用方（Prompt 的
// 取句柄路径）不该因为"能力更新没成功"而让用户这一轮发不出去。
//
// 串行化：迁移是罕见操作（定义变更后的第一个轮次边界），用一把全局锁把并发的第二个调用挡在
// 外面，让它直接拿换好的句柄——比在每个句柄上加状态机简单得多，也不会成为热点。
func (o *Orchestrator) maybeMigrateGeneration(ctx context.Context, as *ActiveConversation) *ActiveConversation {
	plan := o.planGenerationSwitch(as, o.svc.conversationRec(as.ID))
	switch plan.Action {
	case genActionKeep:
		return as
	case genActionDefer:
		o.logger.Info("代际迁移顺延", "conversation", as.ID, "from", plan.From, "to", plan.To, "reason", plan.Reason)
		return as
	}

	o.migMu.Lock()
	defer o.migMu.Unlock()
	// 等锁期间句柄可能已被别的调用换掉（或已被拆连）：以活跃集合里的当前句柄为准，
	// 不再迁一个已经不在用的句柄。
	if cur := o.conversation(as.ID); cur != as {
		if cur == nil {
			return as
		}
		return cur
	}
	// 重新读记录并重新裁决：等锁期间指纹可能又变了（用户连改两次定义），忙碌状态也可能变了。
	rec := o.svc.conversationRec(as.ID)
	plan = o.planGenerationSwitch(as, rec)
	if plan.Action != genActionSwitch {
		if plan.Action == genActionDefer {
			o.logger.Info("代际迁移顺延", "conversation", as.ID, "reason", plan.Reason)
		}
		return as
	}
	agent, ok := o.svc.AgentByID(rec.AgentID)
	if !ok {
		return as
	}
	spec, ok := o.reg.Get(as.Runtime)
	if !ok {
		return as
	}
	sel := ParseCapabilitySelection(rec.CapabilityJSON)
	// 预热新代际并按既有 resume 语义把会话挂上去（`connectConversation` 内完成，
	// 与"进程死后重建"共用同一条路径——两条路径共用才不会出现"重启后能力集悄悄换一套"）。
	next, _, err := o.connectConversation(ctx, rec, agent, spec, rec.Cwd, sel, plan.To)
	if err != nil {
		// ④ 回退：旧代际继续跑。诊断留痕（与 `connection_lost` 同一类事实），但不改会话状态。
		o.noteGenerationFallback(as.AgentID, as.ID, as.Runtime, plan.From, plan.To, err)
		return as
	}
	o.logger.Info("代际迁移完成", "conversation", as.ID, "runtime", spec.ID,
		"from", plan.From, "to", plan.To, "provider_session", next.providerID)
	o.attachConversation(next, "generation")
	return next
}

// noteGenerationFallback 迁移失败的留痕：一条 WARN + 一条 agent 诊断。
//
// 刻意**不**发事件、**不**改会话状态：用户这一轮照常发出去（可用性优先），"能力还没换过来"
// 是运维/诊断层面的事实，不是会话的终态。
func (o *Orchestrator) noteGenerationFallback(agentID, conversationID, runtimeID, from, to string, cause error) {
	o.logger.Warn("代际迁移失败：回退旧代际继续跑",
		"conversation", conversationID, "runtime", runtimeID,
		"from", from, "to", to, "err", cause)
	ae := acl.NewError(acl.ClassConnection, "generation_switch_failed",
		"迁移到新能力集失败（已回退到旧代际继续会话）："+cause.Error(), nil)
	o.svc.recordAgentError(agentID, conversationID, ae, nil)
}
