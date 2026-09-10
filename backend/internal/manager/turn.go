package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// Prompt 向会话追加一轮 prompt（异步；事件经 bus/WS 出流）。
// 返回 user_message 事件后的 bus 水位（客户端 attach 续传用）。会话正忙时返回 turn_in_progress。
// 会话无活句柄（进程刚起、或连接已断）时按需重建：持久层有记录 → ensureConversation 走
// native resume / handoff memo（ADR-4：执行可丢弃、会话平台拥有）；无记录才 conversation_not_found。
func (o *Orchestrator) Prompt(ctx context.Context, conversationID, text string) (int64, error) {
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
			return 0, err
		}
	}
	as.mu.Lock()
	if as.busy {
		as.mu.Unlock()
		return 0, acl.NewError(acl.ClassLimit, "turn_in_progress", "该会话正有 turn 在执行，请等待结束", nil)
	}
	memo := as.memo
	as.memo = "" // 一次性消费：仅 fresh+handoff 会话首条 prompt 注入 memo
	tctx, cancel := context.WithCancel(ctx)
	t := &Turn{done: make(chan struct{}), cancel: cancel, ctx: tctx}
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

// watchdogTurn 单轮超时兜底：超时后走 CancelTurn 的有界取消路径（agent 无响应也本地解除），
// 终态事件由 runTurn 依取消语义发布（run.cancelled）。与用户手动取消幂等共存：turn 已结束时
// select 到 t.done 直接退出；已取消则 CancelTurn 对 nil turn 为 no-op。
func (o *Orchestrator) watchdogTurn(as *ActiveConversation, conversationID string, t *Turn) {
	timer := time.NewTimer(o.turnTimeout)
	defer timer.Stop()
	select {
	case <-t.done:
	case <-timer.C:
		o.logger.Warn("turn 超过看门狗时限，自动取消", "conversation", conversationID, "timeout", o.turnTimeout.String())
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

	switch {
	case userCancel:
		// 用户取消优先：与停机撞车时，取消是用户意图，不能改写成 interrupted。
		o.svc.markConversation(as.ID, ConversationStatusCancelled)
		o.svc.publish(BusRunCancelled, as.ID, map[string]any{"session_id": as.ID, "stop_reason": stop})
	case intentional:
		// 主动拆除（daemon 停机 / 显式停 agent）打断的轮次：既不是用户取消也不是失败，
		// 归 interrupted —— 无终态、可续聊重发（与崩溃恢复同义）。刻意不发 turn.error /
		// run.cancelled：伪终态会污染历史、失败轮列表与重试入口。
		o.svc.markConversation(as.ID, ConversationStatusInterrupted)
		o.logger.Info("turn 因连接主动拆除而中断（会话保留，可续聊重发）",
			"conversation", as.ID, "agent", as.AgentID)
	case cancelled:
		o.svc.markConversation(as.ID, ConversationStatusCancelled)
		o.svc.publish(BusRunCancelled, as.ID, map[string]any{"session_id": as.ID, "stop_reason": stop})
	case err != nil:
		o.svc.markConversation(as.ID, ConversationStatusFailed)
		o.publishError(as.ID, err, client)
	default:
		o.svc.markConversation(as.ID, ConversationStatusCompleted)
		o.svc.publish(BusTurnEnded, as.ID, map[string]any{
			"session_id": as.ID, "agent_id": as.AgentID, "stop_reason": stop,
		})
	}
	close(t.done)
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
		o.logger.Debug("conversation/cancel RPC 未在限期内响应，本地强制解除",
			"conversation", conversationID, "timeout", agentCancelTimeout.String())
	}
	t.cancel()                           // 解除 SDK 阻塞
	o.perm.CancelSession(conversationID) // conversation/cancel 后按 ACP 规范回 Cancelled 全部挂起权限
	o.elic.CancelSession(conversationID) // 挂起的结构化提问一并按取消回馈
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

// RunOnce 在指定 runtime 上跑一次 fresh conversation + prompt 并等待结束（forge demo 用）。
func (o *Orchestrator) RunOnce(ctx context.Context, opt RunOptions) (*RunResult, error) {
	agent, err := o.svc.requireDefaultAgent()
	if err != nil {
		return nil, err
	}
	res, err := o.startConversationOn(ctx, agent, opt.RuntimeID, StartOptions{Cwd: opt.Cwd})
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
