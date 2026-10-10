package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// Prompt 向会话追加一轮 prompt（异步；事件经 bus/WS 出流）。
// 返回 user_message 事件后的 bus 水位（客户端 attach 续传用）。会话正忙时返回 turn_in_progress。
// 会话无活句柄（进程刚起、或连接已断）时按需重建：持久层有记录 → ensureConversation 走
// native resume / handoff memo（ADR-4：执行可丢弃、会话平台拥有）；无记录才 conversation_not_found。
func (o *Orchestrator) Prompt(ctx context.Context, conversationID, text string) (int64, error) {
	as, err := o.acquireConversation(ctx, conversationID)
	if err != nil {
		return 0, err
	}
	as.mu.Lock()
	if as.busy {
		as.mu.Unlock()
		return 0, acl.NewError(acl.ClassLimit, "turn_in_progress", "该会话正有 turn 在执行，请等待结束", nil)
	}
	memo := as.memo
	as.memo = "" // 一次性消费：仅 fresh+handoff 会话首条 prompt 注入 memo
	tctx, cancel := context.WithCancel(ctx)
	t := &Turn{id: uuid.NewString(), done: make(chan struct{}), cancel: cancel, ctx: tctx}
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

// acquireConversation 取（或重建）会话的活句柄。Prompt 与 §6 排队投递共用：
// 两者都必须在「会话忙不忙」之前先把句柄拿到手——队列入队要记当前执行代际，
// 代际只在活句柄存在时有意义（无句柄 = 需要重建，重建即换代）。
func (o *Orchestrator) acquireConversation(ctx context.Context, conversationID string) (*ActiveConversation, error) {
	as := o.conversation(conversationID)
	if as != nil {
		as.mu.Lock()
		dead := as.dead
		as.mu.Unlock()
		if dead {
			// 连接已断的死句柄（拆连后理应已被摘除；这里兜住拿到旧指针的竞态）：摘除并按需重建。
			o.evictConversation(as)
			as = nil
		}
	}
	if as == nil {
		var err error
		as, err = o.ensureConversation(ctx, conversationID)
		if err != nil {
			return nil, err
		}
	}
	// M3-6/M3-7：把"这一轮该用哪个代际"的裁决放在**每一个轮次边界**上（本函数就是那个边界；
	// Prompt 与排队投递都经它）。迁移失败分两种（见 generation.go 文件头的不对称规则）：
	// 纯能力定义变化 → 回退旧代际继续跑（纪律④，不返回错误）；缩权面（权限/provider）变化
	// → 带码拒绝这一轮，绝不用已被收回的权限面继续跑。
	return o.maybeMigrateGeneration(ctx, as)
}

// PromptWithDelivery 带投递模式的 Prompt（§6 busy input 队列）。
//
//   - reject（缺省）：忙时报 turn_in_progress（历史行为，不排队）。
//   - after_turn：忙时入队，当前轮结束后自动投递；空闲时与 reject 同路径（不产生队列行）。
//   - interrupt_then_prompt：入队**后**取消当前轮，随后由收尾路径自动投递。
//     顺序是刻意的——反过来（先取消再入队）会在「取消已生效、runTurn 已收尾取队」与
//     「本函数还没入队」之间留一个窗口，那一项将永远不被投递。入队后再复查一次空闲，
//     闭合「取消前旧轮已自然结束」的另一半窗口（认领是条件更新，两边都跑也只会投一次）。
func (o *Orchestrator) PromptWithDelivery(ctx context.Context, conversationID, text, delivery string) (PromptOutcome, error) {
	mode, err := NormalizeDelivery(delivery)
	if err != nil {
		return PromptOutcome{}, err
	}
	if mode == DeliveryReject {
		seq, err := o.Prompt(ctx, conversationID, text)
		return PromptOutcome{EventSeq: seq}, err
	}
	as, err := o.acquireConversation(ctx, conversationID)
	if err != nil {
		return PromptOutcome{}, err
	}
	as.mu.Lock()
	busy, turnID := as.busy, ""
	if as.turn != nil {
		turnID = as.turn.id
	}
	as.mu.Unlock()
	if !busy {
		// 空闲：直接执行（与 reject 路径同语义——没有「等的那一轮」，不该留下队列行）。
		seq, err := o.Prompt(ctx, conversationID, text)
		return PromptOutcome{EventSeq: seq}, err
	}
	q, err := o.svc.enqueueInput(ctx, conversationID, text, mode, turnID, o.svc.conversationGeneration(conversationID))
	if err != nil {
		return PromptOutcome{}, err
	}
	evt := o.svc.publish(BusInputQueued, conversationID, map[string]any{
		"session_id": conversationID, "agent_id": as.AgentID, "input_id": q.ID, "text": q.Text,
		"delivery": q.Delivery, "target_turn_id": q.TargetTurnID, "generation": q.Generation,
	})
	if mode == DeliveryInterruptThenPrompt {
		if cerr := o.CancelTurn(ctx, conversationID); cerr != nil {
			// 取消失败（会话/连接已变）：入队项留着没有意义——就地撤下，避免留下一条
			// 「谁也不等」的孤儿队列项。
			if _, derr := o.svc.deleteQueuedInput(ctx, conversationID, q.ID); derr != nil {
				glog.Warnw(ctx, "取消当前轮失败且撤下排队项失败", "conversation", conversationID, "input_id", q.ID, "err", derr)
			}
			o.svc.publish(BusInputCancelled, conversationID, map[string]any{
				"session_id": conversationID, "agent_id": as.AgentID, "input_id": q.ID,
				"delivery": q.Delivery, "reason": ReasonDeliverFail,
			})
			return PromptOutcome{}, cerr
		}
	}
	o.deliverQueued(as) // 空闲复查（见函数注释：闭合窗口；认领条件更新保证只投一次）
	return PromptOutcome{EventSeq: evt.Seq, Queued: true, InputID: q.ID}, nil
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
		glog.Warnw(context.Background(), "turn 超过看门狗时限，自动取消", "conversation", conversationID, "timeout", o.turnTimeout.String())
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
	intentional := t.intentional
	userCancel := t.cancelReq // 用户显式取消：即使正在停机收尾，取消仍是用户事实（优先于 interrupted）
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

	// §6：本轮正常走完（含用户取消）→ 收尾后推进排队输入。**失败不自动续投**：
	// 底座刚报错就把队列一条条灌进去，等于把队列变成「连续失败重放」；停机（intentional）
	// 更不该投——那一轮注定不会被执行，重启时统一作废（见 staleQueuedInputsOnRestore）。
	deliverNext := false
	switch {
	case userCancel:
		// 用户取消优先：与停机撞车时，取消是用户意图，不能改写成 interrupted。
		o.svc.markConversation(as.ID, ConversationStatusCancelled)
		o.svc.publish(BusRunCancelled, as.ID, map[string]any{"session_id": as.ID, "stop_reason": stop})
		deliverNext = true
	case intentional:
		// 主动拆除（daemon 停机 / 显式停 agent）打断的轮次：既不是用户取消也不是失败，
		// 归 interrupted —— 无终态、可续聊重发（与崩溃恢复同义）。刻意不发 turn.error /
		// run.cancelled：伪终态会污染历史、失败轮列表与重试入口。
		o.svc.markConversation(as.ID, ConversationStatusInterrupted)
		glog.Infow(context.Background(), "turn 因连接主动拆除而中断（会话保留，可续聊重发）",
			"conversation", as.ID, "agent", as.AgentID)
	case cancelled:
		o.svc.markConversation(as.ID, ConversationStatusCancelled)
		o.svc.publish(BusRunCancelled, as.ID, map[string]any{"session_id": as.ID, "stop_reason": stop})
		deliverNext = true
	case err != nil:
		o.svc.markConversation(as.ID, ConversationStatusFailed)
		o.publishError(as.ID, err, client)
	default:
		o.svc.markConversation(as.ID, ConversationStatusCompleted)
		o.svc.publish(BusTurnEnded, as.ID, map[string]any{
			"session_id": as.ID, "agent_id": as.AgentID, "stop_reason": stop,
		})
		deliverNext = true
	}
	close(t.done)
	if deliverNext {
		// 在 runTurn 的 goroutine 里同步投递队首（deliverQueued 内部另起 goroutine 跑新一轮，
		// 不会在本 goroutine 上递归执行）。
		o.deliverQueued(as)
	}
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
		glog.Debugw(ctx, "conversation/cancel RPC 未在限期内响应，本地强制解除",
			"conversation", conversationID, "timeout", agentCancelTimeout.String())
	}
	t.cancel()                                // 解除 SDK 阻塞
	o.perm.CancelConversation(conversationID) // conversation/cancel 后按 ACP 规范回 Cancelled 全部挂起权限
	o.elic.CancelConversation(conversationID) // 挂起的结构化提问一并按取消回馈
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

// RunOnce 在**构建期选定的** runtime 上跑一次 fresh conversation + prompt 并等待结束（mate demo 用）。
func (o *Orchestrator) RunOnce(ctx context.Context, opt RunOptions) (*RunResult, error) {
	agent, err := o.svc.requireDefaultAgent()
	if err != nil {
		return nil, err
	}
	res, err := o.startConversationOn(ctx, agent, StartOptions{Cwd: opt.Cwd})
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

// resolveArgs 替换 Launch.Args 中的 {config} 占位（保留给自定义 adapter 模板；
// 当前内置 runtime 的 Args 均已不含占位，此函数为幂等空操作）。
func resolveArgs(args []string, opt RunOptions) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, "{config}", opt.ConfigPath)
	}
	return out
}
