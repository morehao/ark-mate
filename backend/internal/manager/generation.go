package manager

import (
	"context"
	"fmt"

	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/golib/glog"
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
//	   （`MATE_HANDOFF_MAX_CHARS` 的存在就说明它是有损的）。每轮都切 = 每轮都可能丢上下文。
//	② **忙则顺延**：有在途轮次、或有未应答的权限/提问，都不切。前者是"绝不打断在途轮次"，
//	   后者是"别把用户正盯着的确认卡换到另一个进程去"。
//	③ **探针不可读时保守当忙**：读不到会话状态就不切。宁可多用一个旧代际（多占一点内存），
//	   也不要在不确定的状态下动一个可能正在跑的会话。
//	④ **迁移失败回退旧代际继续跑**：迁代际是**尽力而为**的优化，失败只留痕（日志 + runtime 诊断），
//	   绝不让会话变成不可用——与 openwork 刻意不把超时升级为重启是同一个道理。
//
// 缺省路径（会话未声明能力选择）**不因能力定义变化而迁移**：那种会话的指纹恒为空、代际键与
// 引入代际之前逐字相同（R11），"改了定义下一轮生效"对它们本来就不是承诺。这是记得住的边界，
// 不是疏漏。但它**会因进程级能力面变化而迁移**（原生权限面 / provider 注入面变化）：那些东西在
// 基础键里、不在指纹里，且"缩权立刻生效"说的正是它们（M3-7）。
//
// **缩权与扩权不对称**（ADR-13）：基础键变化（权限被收回）时，旧代际代表的是一套**已经被收回的
// 权限面**，所以迁移失败**不许回退**——宁可这一轮发不出去（带码 + 出路），也不用旧进程继续跑；
// 纯能力集变化（定义改了）失败则回退旧代际（纪律④），因为它没有权限面越界的问题。

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

// generationPlan 裁决结果。`From`/`To` 是**完整代际键**（基础键 + 指纹），只在 switch/defer
// 时有意义（`keep` 时为空）。`PermChanged` 标出这次变化的性质：基础键（原生权限 / provider）
// 变了 = 进程级能力面变了 = 缩权面，迁移失败时不许回退（见文件头的不对称规则）。
type generationPlan struct {
	Action      generationAction
	Reason      string
	From        string
	To          string
	PermChanged bool
	// FingerprintChanged 能力指纹（能力集内容）是否变了。与 PermChanged 相互独立。
	FingerprintChanged bool
	// ConfigChanged T5 的平台模型配置指纹是否变了（清单改了 → 新进程才读得到新配置）。
	// 它**不是缩权面**：迁移失败可以回退旧代际（旧配置不是"已被收回的权限"）。
	ConfigChanged bool
}

// planGenerationSwitch 比较「会话**此刻挂着的代际**」与「现在该用的代际」，给出本轮该怎么办。
//
// "现在该用的代际"由两部分组成：基础键（runtime + 原生权限 env + provider env，进程级能力面）
// 与能力指纹（会话钉住的选定按**当前定义**解析出的内容摘要）。两者都要比——
// 只比指纹会漏掉"权限被收回但定义没动"（那是缩权，必须立刻生效）；
// 只比基础键会漏掉"定义改了"（那是扩权/改内容，下一轮生效）。
//
// 只读、无副作用：四条纪律里的三条（键比较、忙/挂起、探针可读性）都能在不拉进程的前提下被
// 单测逐条钉住。
func (o *Orchestrator) planGenerationSwitch(as *ActiveSession, rec *SessionRec) generationPlan {
	if as == nil || rec == nil {
		return generationPlan{Action: genActionKeep, Reason: "会话记录不可读：不动（保守）"}
	}
	// 探针：句柄状态。dead 的句柄不该在这里动——它已经作废，下一次 prompt 会走
	// ensureSession 的重建路径，那条路径本来就按**当前**配置重开。
	as.mu.Lock()
	busy, dead := as.busy, as.dead
	as.mu.Unlock()
	if dead {
		return generationPlan{Action: genActionKeep, Reason: "句柄已作废：交给重建路径按当前配置重开"}
	}
	spec, ok := o.reg.Get(as.Runtime)
	if !ok {
		return generationPlan{Action: genActionKeep, Reason: "runtime 未注册：不动"}
	}
	// ③ 探针不可读 → 保守当忙。连接反查不到（正在拆/已拆）时无法判断这条句柄挂在什么上面，
	// 那就当它不可动——真正的重建由 ensureSession 负责，不在这里猜。
	rc := o.pool.ConnOfClient(as.client)
	if rc == nil {
		return generationPlan{Action: genActionKeep, Reason: "连接不可读：不动（保守）"}
	}
	wantFP := as.Fingerprint
	if sel := ParseCapabilitySelection(rec.CapabilityJSON); sel != nil {
		if fp := o.svc.capabilityFingerprint(spec.ID, sel); fp != "" {
			wantFP = fp
		}
	}
	// 目标键由池一次算完（基础键 + 能力指纹 + T5 的模型配置指纹，三个维度同源）。
	// T5：平台生成的模型配置指纹也是代际维度——配置文件在**进程启动时**读。
	keys := o.pool.KeyFor(spec, wantFP, as.NativeMode)
	plan := generationPlan{
		From: rc.Key, To: keys.Key,
		PermChanged:        keys.BaseKey != rc.BaseKey,
		FingerprintChanged: wantFP != as.Fingerprint,
		ConfigChanged:      keys.ConfigDigest != rc.ConfigDigest,
	}
	if plan.To == plan.From {
		plan.Action = genActionKeep
		plan.Reason = "代际键未变：不切（绝不每轮切）"
		return plan
	}
	if busy {
		plan.Action = genActionDefer
		plan.Reason = "有在途轮次：顺延到下一轮"
		return plan
	}
	if as.client == nil || as.sess == nil || o.perm == nil || o.elic == nil {
		plan.Action = genActionDefer
		plan.Reason = "会话忙碌状态不可读：保守顺延"
		return plan
	}
	if o.perm.PendingCount(as.ID) > 0 || o.elic.PendingCount(as.ID) > 0 {
		plan.Action = genActionDefer
		plan.Reason = "有未应答的权限/提问：顺延到下一轮"
		return plan
	}
	plan.Action = genActionSwitch
	switch {
	case plan.PermChanged && plan.FingerprintChanged:
		plan.Reason = "权限/provider 与能力定义都变了：本轮边界迁到新代际"
	case plan.PermChanged:
		plan.Reason = "权限/provider 变化（进程级能力面已变，缩权需立刻生效）：本轮边界迁到新代际"
	case plan.ConfigChanged:
		// 纯模型清单变化：**不是缩权**，所以迁移失败可以回退旧代际（ADR-13 的不对称只针对权限面）。
		plan.Reason = "平台模型清单变化（新配置在进程启动时读）：本轮边界迁到新代际"
	default:
		plan.Reason = "能力定义变化：本轮边界迁到新代际"
	}
	return plan
}

// maybeMigrateGeneration 在轮次边界把会话迁到当前该用的代际，返回**本轮该用的句柄**。
//
// 错误只有一种：**缩权面（基础键）变了、而新代际建不起来**。这时不能回退旧代际——旧代际代表的
// 正是已经被收回的那套权限面（ADR-13「缩权立刻生效」）。其余失败一律回退（纪律④，不返回错误）：
// 迁代际是尽力而为的优化，调用方不该因为"能力更新没成功"而让用户这一轮发不出去。
//
// 串行化：迁移是罕见操作（配置变更后的第一个轮次边界），用一把全局锁把并发的第二个调用挡在
// 外面，让它直接拿换好的句柄——比在每个句柄上加状态机简单得多，也不会成为热点。
func (o *Orchestrator) maybeMigrateGeneration(ctx context.Context, as *ActiveSession) (*ActiveSession, error) {
	plan := o.planGenerationSwitch(as, o.svc.sessionRec(as.ID))
	switch plan.Action {
	case genActionKeep:
		return as, nil
	case genActionDefer:
		glog.Infow(ctx, "代际迁移顺延", "session", as.ID, "from", plan.From, "to", plan.To, "reason", plan.Reason)
		return as, nil
	}

	o.migMu.Lock()
	defer o.migMu.Unlock()
	// 等锁期间句柄可能已被别的调用换掉（或已被拆连）：以活跃集合里的当前句柄为准，
	// 不再迁一个已经不在用的句柄。
	if cur := o.session(as.ID); cur != as {
		if cur == nil {
			return as, nil
		}
		return cur, nil
	}
	// 重新读记录并重新裁决：等锁期间键可能又变了（用户连改两次定义），忙碌状态也可能变了。
	rec := o.svc.sessionRec(as.ID)
	plan = o.planGenerationSwitch(as, rec)
	switch plan.Action {
	case genActionSwitch:
	case genActionDefer:
		glog.Infow(ctx, "代际迁移顺延", "session", as.ID, "reason", plan.Reason)
		return as, nil
	default:
		return as, nil
	}
	spec, ok := o.reg.Get(as.Runtime)
	if !ok {
		return as, nil
	}
	sel := ParseCapabilitySelection(rec.CapabilityJSON)
	// 目标指纹：能力集内容没变而只是权限面变了时，**沿用句柄当前钉住的指纹**
	// （不必重算：重算还可能因为定义又变了而算出第三个值，那不是"缩权迁移"该管的事）。
	targetFP := as.Fingerprint
	if sel != nil {
		if fp := o.svc.capabilityFingerprint(spec.ID, sel); fp != "" {
			targetFP = fp
		}
	}
	oldRC := o.pool.ConnOfClient(as.client)
	// 预热新代际并按既有 resume 语义把会话挂上去（`connectSession` 内完成，
	// 与"进程死掉重建"共用同一条路径——两条路径共用才不会出现"重启后能力集悄悄换一套"）。
	next, _, err := o.connectSession(ctx, rec, spec, rec.Cwd, sel, targetFP)
	if err != nil {
		if plan.PermChanged {
			// 缩权面变化 + 建不起新代际 → **不回退**（旧进程 = 已被收回的权限面）。
			glog.Warnw(ctx, "权限/能力面已收窄但新代际建不起来：本轮不发（不使用被收回的权限面继续跑）",
				"session", as.ID, "runtime", as.Runtime, "from", plan.From, "to", plan.To, "err", err)
			return nil, acl.NewError(acl.ClassLimit, "generation_switch_required",
				fmt.Sprintf("会话的权限或能力面已变更（%s），但新代际此刻无法建立：本轮未发出——不会用已经收回的权限面继续跑。请稍后重试（%s）",
					plan.Reason, err.Error()), nil)
		}
		// ④ 回退：旧代际继续跑。诊断留痕（与 `connection_lost` 同一类事实），但不改会话状态。
		o.noteGenerationFallback(as.ID, as.Runtime, plan.From, plan.To, err)
		return as, nil
	}
	glog.Infow(ctx, "代际迁移完成", "session", as.ID, "runtime", spec.ID,
		"from", plan.From, "to", plan.To, "perm_changed", plan.PermChanged,
		"runtime_session", next.providerID)
	o.attachSession(next, "generation")
	// 缩权/换代后立刻淘汰**已无人引用**的旧代际（不等空闲阈值）：旧进程代表的可能是一套
	// 已经不该存在的权限/能力面，留着它既占内存又违反"缩权立刻生效"的直觉。
	// 仍有别的会话引用时不动它（那些会话会在自己的下一个轮次边界迁走）。
	if o.pool.ReclaimGeneration(oldRC) {
		glog.Infow(ctx, "代际迁移后回收旧代际（已无人引用）", "key", plan.From)
	}
	return next, nil
}

// noteGenerationFallback 迁移失败的留痕：一条 WARN + 一条 runtime 诊断。
//
// 刻意**不**发事件、**不**改会话状态：用户这一轮照常发出去（可用性优先），"能力还没换过来"
// 是运维/诊断层面的事实，不是会话的终态。
func (o *Orchestrator) noteGenerationFallback(sessionID, runtimeID, from, to string, cause error) {
	glog.Warnw(context.Background(), "代际迁移失败：回退旧代际继续跑",
		"session", sessionID, "runtime", runtimeID,
		"from", from, "to", to, "err", cause)
	ae := acl.NewError(acl.ClassConnection, "generation_switch_failed",
		"迁移到新能力集失败（已回退到旧代际继续会话）："+cause.Error(), nil)
	o.svc.recordRuntimeError(sessionID, ae, nil)
}
