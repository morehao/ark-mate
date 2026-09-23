package manager

import (
	"context"
	"log/slog"
	"time"

	"github.com/morehao/ark-forge/internal/dto/dtoconversation"
	"github.com/morehao/ark-forge/pkg/automation"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/ark-forge/pkg/redact"
)

// ——— Automation 调度循环（M4-4）———
//
// 单 goroutine + 一个 ticker：本平台是**单实例**（多实例需要 lease/heartbeat，属非目标），
// 所以"谁在调度"这个问题根本不存在，也就不需要分布式认领那套东西（ADR-11）。
//
// 一次触发的事实全部落在那一个定义文件里（认领游标 `last_fired_at` + 最近若干次
// `recent_runs` + `state`），因此**崩溃恢复判据就是文件本身**：不需要 lease，
// 也不需要"运行时台账"这第二份事实源。
//
// 三条硬规矩：
//  1. **失败也推进游标**（ADR-10）：认领之后不论成败都写 `last_fired_at`，否则同一次错过
//     会每 30s 重报一次，把真正的问题淹在刷屏里。
//  2. **overlap 不排队**：上一次触发的会话还在跑 → 记 `overlap` 跳过本次（排队等于
//     让有副作用的 prompt 堆积，最终一起冲出去）。
//  3. **不回写会话结果**：`claimed` 之后这轮成没成看**会话自身**（ADR-4）。

// automationRunOutcome 一次触发的结果（写回与事件都从它派生，避免两处各写一半）。
type automationRunOutcome struct {
	// scheduledFor 本次认领的 occurrence（写进认领游标；manual 不写）。
	scheduledFor time.Time
	status       string
	reason       string
	// conversationID 本次产生的会话（非 claimed 为空）。
	conversationID string
	trigger        string
	// state 期望落到的状态；空 = 由 failure/成功与否推导。
	state string
	// advance 是否推进认领游标（scheduled/recovery = true；**manual = false**——
	// 手动跑一次不该吃掉下一个排期，否则"我手动试一下"会静默跳过当晚的巡检）。
	advance bool
	// failure "没跑成"（累加 consecutive_failures 并落 needs_attention）。
	failure bool
	// noRecord 不追加 recent_runs、不写盘（duplicate：本次没有任何新事实）。
	noRecord bool
	// cause 失败原因的原错误（**只给手动触发用**：定时触发把原因写进文件就够了，
	// 而手动触发时用户就在等一个回答，必须把真实错误码原样给他）。
	cause error
}

// StartAutomationScheduler 启动调度循环（幂等；interval<=0 = 关闭，不启 goroutine）。
//
// 启动 pass 在**同步**执行（先落定 ADR-9 的终局，再开始 tick）：它只做两件不建会话的事
// ——过期的 `once` 置 inactive、已跑完的 `once` 补状态——所以放在装配期不会拉起任何底座进程。
func (s *Service) StartAutomationScheduler(interval time.Duration) {
	if interval <= 0 {
		slog.Info("automation 调度未启动（FORGE_AUTOMATION_TICK=0）")
		return
	}
	s.mu.Lock()
	if s.automationStop != nil {
		s.mu.Unlock()
		return // 已启动（幂等）
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.automationStop = cancel
	s.automationDone = done
	s.mu.Unlock()

	s.automationStartupPass()

	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				s.automationTick(ctx, now)
			}
		}
	}()
}

// StopAutomationScheduler 停止调度循环并**等它退出**（daemon 退出；幂等）。
//
// 等退出而不是只 cancel：`select` 在 ctx.Done 与 tick 同时就绪时是**随机**选一个的，
// 所以"cancel 返回了"不等于"不会再有新触发起步"——停机流程需要的是后者。
// 已在执行中的那一次触发不受影响（它走到一半，取消它只会留下半截状态）。
func (s *Service) StopAutomationScheduler() {
	s.mu.Lock()
	stop, done := s.automationStop, s.automationDone
	s.automationStop, s.automationDone = nil, nil
	s.mu.Unlock()
	if stop == nil {
		return
	}
	stop()
	if done != nil {
		<-done
	}
}

// automationStartupPass 启动时的一次性收敛（ADR-9）。
//
// 只处理 `once` 的两种终局，**不补跑**：`once` 是"就在那一刻跑一次"的语义（像闹钟），
// 进程没在跑就是错过了——补跑一个早已过去的 `once` 只会让人意外（"我约的是凌晨三点的迁移"）。
// daily/weekly 的"补最近一次"由第一个 tick 走正常到期路径（rec.startup=recover → trigger=recovery）。
func (s *Service) automationStartupPass() {
	for _, rec := range s.automationRecs() {
		if rec.loadErr != "" {
			continue
		}
		switch rec.startup {
		case automation.StartupExpire:
			// 幂等闸门：状态已经是 inactive 就不再写（否则每次重启都会追加一条 recent_runs）。
			if rec.def.State == automation.StateInactive || automation.Terminal(rec.def.State) {
				continue
			}
			missed := time.Time{}
			if rec.def.Schedule.At != nil {
				missed = *rec.def.Schedule.At
			}
			slog.Warn("automation: once 已过期且从未运行，置 inactive（不补跑）", "automation", rec.def.ID)
			s.finishAutomationRun(rec, automationRunOutcome{
				scheduledFor: missed,
				status:       automation.RunSkipped,
				trigger:      automation.TriggerRecovery,
				reason:       "进程停机期间错过，且 once 是\"到点跑一次\"的语义：不补跑（ADR-9 只补 daily/weekly 的最近一次）",
				state:        automation.StateInactive,
			})
		case automation.StartupInactive:
			// 已跑完的 once：定义文件里若还写着 active，补一句 inactive（只在需要时写盘）。
			if rec.def.State == automation.StateInactive || automation.Terminal(rec.def.State) || rec.def.LastFiredAt == nil {
				continue
			}
			if err := s.saveAutomationOverlay(rec.def.ID, map[string]any{"state": automation.StateInactive}); err != nil {
				slog.Warn("automation: 补写 inactive 失败", "automation", rec.def.ID, "err", err)
			}
		}
	}
}

// automationTick 一次扫描（导出给测试直接驱动，不必等 ticker）。
func (s *Service) automationTick(ctx context.Context, now time.Time) {
	for _, rec := range s.automationDueSnapshot(now) {
		trigger := automation.TriggerScheduled
		if rec.startup == automation.StartupRecover {
			trigger = automation.TriggerRecovery
		}
		s.automationRunOnce(ctx, rec, rec.nextDue, trigger)
	}
}

// automationRunOnce 认领并执行一条到期的 automation。
//
// 认领三态（`pkg/automation.Decide`）：claimed / duplicate / overlap。
// `duplicate` 在前：**"这次触发过没有"必须优先于"上一次还在不在跑"**——否则一个
// "上次会话卡住不结束"的 automation，会在重启后把同一 occurrence 再跑一遍。
func (s *Service) automationRunOnce(ctx context.Context, rec automationRec, scheduledFor time.Time, trigger string) automationRunOutcome {
	// 认领判定不是原子的（见 automationRunMu 注释）：判定与写回之间必须独占，
	// 否则 tick 与手动触发会为同一个 occurrence 各建一个会话。
	//
	// 进临界区后**必须重取记录**：`rec` 是锁外取的快照，另一路可能已经认领并写回了游标
	// ——拿旧快照判定等于没加锁（三路并发会建出三个会话，这条有专门的用例）。
	s.automationRunMu.Lock()
	defer s.automationRunMu.Unlock()
	fresh, ok := s.automationRecByID(rec.def.ID)
	if !ok || fresh.loadErr != "" || !fresh.def.Enabled || automation.Terminal(fresh.def.State) {
		// 快照与临界区之间定义被删/停用/归档：什么都不做（没有新事实可留痕）。
		return automationRunOutcome{
			scheduledFor: scheduledFor, status: automation.RunSkipped, trigger: trigger, noRecord: true,
			reason: "定义在触发前已被删除、停用或归档（本次不执行）",
		}
	}
	rec = fresh
	claim := automation.Decide(scheduledFor, rec.def.LastFiredAt, s.automationHasActiveRun(rec))
	switch claim.Status {
	case automation.ClaimDuplicate:
		// 文件里已经记着这次认领（上一轮写回失败 / 重启后重算）：**不写盘、不追加留痕**，
		// 只把内存游标推到下一个 occurrence，并推一条事件（这是"重复触发"唯一可见的痕迹）。
		s.refreshAutomationSchedule(rec.def.ID)
		out := automationRunOutcome{
			scheduledFor: scheduledFor, status: automation.RunDuplicate, trigger: trigger,
			reason: claim.Reason, noRecord: true,
		}
		s.emitAutomationRun(rec.def.ID, out)
		return out
	case automation.ClaimOverlap:
		// 理由沿用纯函数里的措辞（那一句本身就是要给用户看的），只在后面补"然后呢"。
		out := automationRunOutcome{
			scheduledFor: scheduledFor, status: automation.RunOverlap, trigger: trigger, advance: true,
			reason: claim.Reason + "；下一次 occurrence 照常",
		}
		return s.finishAutomationRun(rec, out)
	}

	// claimed：真的建会话。
	//
	// **不在这里做第二份"前置校验"**（agent 是否存在 / runtime 是否可用 / cwd 是否还在）：
	// 那份校验必然与真实创建路径漂移，而 ADR-10 真正要求的是"失败也推进游标"——那与校验
	// 在哪一层无关。所以直接调真实路径，把它带码的错误落成 needs_attention 的**原因**，
	// 用户在面板上看到的正是"为什么没跑成"。
	resp, err := s.CreateConversation(ctx, dtoconversation.ConversationCreateReq{
		AgentID:     rec.def.Target.AgentID,
		Prompt:      rec.def.Target.Prompt,
		Cwd:         rec.def.Target.Cwd,
		Model:       rec.def.Target.Model,
		Origin:      automationOrigin(rec.def.ID),
		RuntimeHint: rec.def.Target.RuntimeHint,
	})
	if err != nil {
		out := automationRunOutcome{
			scheduledFor: scheduledFor, status: automation.RunSkipped, trigger: trigger, advance: true,
			failure: true, cause: err,
			reason: "触发失败（已跳过本次，等修好后的下一个 occurrence）：" + errText(err),
		}
		slog.Warn("automation 触发失败", "automation", rec.def.ID, "err", err)
		return s.finishAutomationRun(rec, out)
	}
	out := automationRunOutcome{
		scheduledFor: scheduledFor, status: automation.RunClaimed, trigger: trigger,
		conversationID: resp.ID,
		// 手动触发不推进游标（见字段注释）。
		advance: trigger != automation.TriggerManual,
	}
	res := s.finishAutomationRun(rec, out)
	// max_run_duration：可选的**收紧**（默认只靠既有 FORGE_TURN_TIMEOUT 看门狗），
	// 走同一条取消通道，不引入第二套超时机制。
	if d, err := rec.def.RunDuration(); err == nil && d > 0 {
		go s.automationWatchdog(ctx, rec.def.ID, resp.ID, d)
	}
	return res
}

// runAutomationNow 手动触发一次（`POST /automations/:id/run` 的领域实现；端口形态在 automation_rest.go）。
//
// 走**同一个认领判据**（上一次还在跑 → overlap 跳过）：手动不等于可以并发跑两条会话。
// 但**不推进认领游标**，所以手动试跑不会吃掉下一个排期。
func (s *Service) runAutomationNow(ctx context.Context, id string) (automationRunOutcome, error) {
	rec, ok := s.automationRecByID(id)
	if !ok {
		return automationRunOutcome{}, notFoundAutomation(id)
	}
	if rec.loadErr != "" {
		return automationRunOutcome{}, invalidAutomation(rec)
	}
	if !rec.def.Enabled {
		return automationRunOutcome{}, automationDisabled(id)
	}
	out := s.automationRunOnce(ctx, rec, time.Now(), automation.TriggerManual)
	// 手动触发失败 → **既留痕又报错**：痕迹让运行史完整（"我点过、没跑成"），
	// 报错让用户当场知道为什么（把 skipped 当成功返回等于让面板显示一个绿色的失败）。
	if out.cause != nil {
		return out, code.FromError(out.cause)
	}
	return out, nil
}

// automationWatchdog 单次运行的外层超时：到点仍在本轮 → 走既有取消通道。
func (s *Service) automationWatchdog(ctx context.Context, automationID, conversationID string, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if !s.conversationRunning(conversationID) {
		return
	}
	slog.Warn("automation 本轮超过 max_run_duration，自动取消",
		"automation", automationID, "conversation", conversationID, "max_run_duration", d.String())
	// 取消的终态（run.cancelled）由 runTurn 依取消语义发布；这里只负责按下按钮。
	_ = s.CancelTurn(context.WithoutCancel(ctx), conversationID)
}

// finishAutomationRun 把结果写回定义文件（overlay）并推事件，然后刷新内存调度态。
//
// 写盘失败**不阻断**：内存态照样推进（本进程内不会重复触发），并且事件里带上写盘失败的事实
// ——"调度跑起来了但状态没落盘"必须可见，否则重启后会重跑一次而没人知道为什么。
func (s *Service) finishAutomationRun(rec automationRec, out automationRunOutcome) automationRunOutcome {
	if out.noRecord {
		return out
	}
	now := time.Now().UTC()
	overlay := map[string]any{
		"last_status": out.status,
		"last_reason": out.reason,
	}
	if out.advance {
		overlay["last_fired_at"] = out.scheduledFor.UTC().Format(time.RFC3339Nano)
	}
	if out.conversationID != "" {
		overlay["last_conversation_id"] = out.conversationID
	}
	failures := rec.def.ConsecutiveFailures
	switch {
	case out.failure:
		failures++
	case out.status == automation.RunClaimed:
		failures = 0 // 跑起来了 → 之前的连续失败清零（用户修好了）
	}
	overlay["consecutive_failures"] = failures
	state := rec.def.State
	switch {
	case out.state != "":
		state = out.state
	case out.failure:
		state = automation.StateNeedsAttention
	case out.status == automation.RunClaimed || out.status == automation.RunOverlap:
		state = automation.StateActive // 自愈：跑成了/只是撞车，不该继续停在 needs_attention
	}
	overlay["state"] = state
	// recent_runs 环形缓冲：最新在前，超出上限截断（只保留最近 N 次，文件不无限长）。
	runs := make([]automation.RunRecord, 0, len(rec.def.RecentRuns)+1)
	runs = append(runs, automation.RunRecord{
		ScheduledFor: out.scheduledFor.UTC(), FiredAt: now, Status: out.status,
		Reason: out.reason, ConversationID: out.conversationID, Trigger: out.trigger,
	})
	runs = append(runs, rec.def.RecentRuns...)
	if len(runs) > automation.RecentRunsMax {
		runs = runs[:automation.RecentRunsMax]
	}
	overlay["recent_runs"] = runs

	if err := s.saveAutomationOverlay(rec.def.ID, overlay); err != nil {
		slog.Error("automation 运行态写回失败（内存调度态照常推进，重启后可能重跑一次）",
			"automation", rec.def.ID, "err", err)
	}
	out.state = state
	s.emitAutomationRun(rec.def.ID, out)
	return out
}

// emitAutomationRun 推 `automation.run` 事件（**只进 bus，不落 events 表**）。
//
// 为什么不在 publishRaw 里走一遍：events 表的 `conversation_id` 是
// `NOT NULL REFERENCES conversations(id)`（`foreign_keys(1)` 由 pkg/dbclient 的守卫钉着），
// 而 duplicate/overlap/触发失败这三类结果**没有会话可挂**——硬塞一个假 id 会直接撞外键。
// 运行史（含这三类）落在定义文件的 `recent_runs` 里（见 `pkg/automation.RunRecord` 注释），
// 这条事件只负责**实时推送**（面板不必等轮询）。
func (s *Service) emitAutomationRun(id string, out automationRunOutcome) {
	if s.bus == nil {
		return
	}
	payload := map[string]any{
		"automation_id": id,
		"status":        out.status,
		"trigger":       out.trigger,
	}
	if !out.scheduledFor.IsZero() {
		payload["scheduled_for"] = out.scheduledFor.UTC().Format(time.RFC3339)
	}
	if out.conversationID != "" {
		payload["conversation_id"] = out.conversationID
	}
	if out.reason != "" {
		payload["reason"] = out.reason
	}
	if out.state != "" {
		payload["state"] = out.state
	}
	// 与其余事件同一个脱敏闸门（理由里可能带路径/环境串）。
	payload = redact.Payload(payload).(map[string]any)
	s.mu.Lock()
	s.ver[automationOrigin(id)]++
	v := s.ver[automationOrigin(id)]
	s.mu.Unlock()
	s.bus.Publish(BusAutomationRun, automationOrigin(id), v, payload)
}

// refreshAutomationSchedule 按当前定义重算内存调度态（duplicate 分支用；不写盘）。
func (s *Service) refreshAutomationSchedule(id string) {
	s.mu.Lock()
	rec := s.automations[id]
	s.mu.Unlock()
	if rec == nil {
		return
	}
	updated := *rec
	updated.applySchedule(time.Now())
	s.mu.Lock()
	s.automations[id] = &updated
	s.mu.Unlock()
}

// automationRecs 取全部记录的副本（启动 pass 用；坏定义也在内，由调用方判断）。
func (s *Service) automationRecs() []automationRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]automationRec, 0, len(s.automations))
	for _, rec := range s.automations {
		out = append(out, *rec)
	}
	return out
}

// conversationRunning 该会话是否仍在本轮（自动化的 overlap 判据与 max_run_duration 判据共用）。
func (s *Service) conversationRunning(id string) bool {
	if id == "" {
		return false
	}
	// 状态**在锁内读出**：`markConversation` 归 runTurn 的 goroutine 所有，
	// 出锁后再读 rec.Status 就是数据竞争（-race 直接报）。
	s.mu.Lock()
	defer s.mu.Unlock()
	conv, ok := s.conversations[id]
	if !ok {
		return false
	}
	return conv.Status == ConversationStatusInProgress
}

// automationOrigin 会话来源标记（`conversations.origin`）：`automation:<id>`。
func automationOrigin(id string) string { return "automation:" + id }
