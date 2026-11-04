package automation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/core/automation"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/redact"
	"github.com/morehao/golib/glog"
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
//  3. **不回写会话结果**：`Claimed` 之后这轮成没成看**会话自身**（ADR-4）。

// RunOutcome 一次触发的结果（写回与事件都从它派生，避免两处各写一半）。
type RunOutcome struct {
	// ScheduledFor 本次认领的 occurrence（写进认领游标；manual 不写）。
	ScheduledFor time.Time
	Status       string
	Reason       string
	// SessionID 本次产生的会话（非 Claimed 为空）。
	SessionID string
	Trigger   string
	// State 期望落到的状态；空 = 由 Failure/成功与否推导。
	State string
	// Advance 是否推进认领游标（scheduled/recovery = true；**manual = false**——
	// 手动跑一次不该吃掉下一个排期，否则"我手动试一下"会静默跳过当晚的巡检）。
	Advance bool
	// Failure "没跑成"（累加 consecutive_failures 并落 needs_attention）。
	Failure bool
	// NoRecord 不追加 recent_runs、不写盘（duplicate：本次没有任何新事实）。
	NoRecord bool
	// Claimed 已在**建会话之前**把认领游标落库（DB 载体的 scheduled/recovery 路径；见
	// `claimAutomationOccurrence`）。带它之后运行态写回的 CAS 判据是"这次刚认领的那个值"。
	Claimed bool
	// Cause 失败原因的原错误（**只给手动触发用**：定时触发把原因写进文件就够了，
	// 而手动触发时用户就在等一个回答，必须把真实错误码原样给他）。
	Cause error
}

// StartAutomationScheduler 启动调度循环（幂等；interval<=0 = 关闭，不启 goroutine）。
//
// 启动 pass 在**同步**执行（先落定 ADR-9 的终局，再开始 tick）：它只做两件不建会话的事
// ——过期的 `once` 置 inactive、已跑完的 `once` 补状态——所以放在装配期不会拉起任何底座进程。
func (pl *Plane) StartAutomationScheduler(interval time.Duration) {
	if interval <= 0 {
		glog.Infow(context.Background(), "automation 调度未启动（MATE_AUTOMATION_TICK=0）")
		return
	}
	pl.mu.Lock()
	if pl.stop != nil {
		pl.mu.Unlock()
		return // 已启动（幂等）
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	pl.stop = cancel
	pl.done = done
	pl.mu.Unlock()

	pl.StartupPass(ctx)

	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				// 调度循环（真实 ticker）每次先重载定义，让 `mate mcp automation` 子进程
				// 写进来的新定义当轮生效。**只在这个真实循环里重载**，不做进 `Tick` 里：
				// 单测直接调 `Tick` 时会手动拨 in-memory 游标，若 Tick 自身重载会把那个
				// 拨动冲掉（装配事实见 reloadDefinitionsForTick 的注释）。
				pl.ReloadDefinitionsForTick(ctx)
				pl.Tick(ctx, now)
			}
		}
	}()
}

// StopAutomationScheduler 停止调度循环并**等它退出**（daemon 退出；幂等）。
//
// 等退出而不是只 cancel：`select` 在 ctx.Done 与 tick 同时就绪时是**随机**选一个的，
// 所以"cancel 返回了"不等于"不会再有新触发起步"——停机流程需要的是后者。
// 已在执行中的那一次触发不受影响（它走到一半，取消它只会留下半截状态）。
func (pl *Plane) StopAutomationScheduler() {
	pl.mu.Lock()
	stop, done := pl.stop, pl.done
	pl.stop, pl.done = nil, nil
	pl.mu.Unlock()
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
// daily/weekly 的"补最近一次"由第一个 tick 走正常到期路径（rec.Startup=recover → trigger=recovery）。
func (pl *Plane) StartupPass(ctx context.Context) {
	for _, rec := range pl.Recs() {
		if rec.LoadErr != "" {
			continue
		}
		switch rec.Startup {
		case automation.StartupExpire:
			// 幂等闸门：状态已经是 inactive 就不再写（否则每次重启都会追加一条 recent_runs）。
			if rec.Def.State == automation.StateInactive || automation.Terminal(rec.Def.State) {
				continue
			}
			missed := time.Time{}
			if rec.Def.Schedule.At != nil {
				missed = *rec.Def.Schedule.At
			}
			glog.Warnw(ctx, "automation: once 已过期且从未运行，置 inactive（不补跑）", "automation", rec.Def.ID)
			pl.FinishAutomationRun(ctx, rec, RunOutcome{
				ScheduledFor: missed,
				Status:       automation.RunSkipped,
				Trigger:      automation.TriggerRecovery,
				Reason:       "进程停机期间错过，且 once 是\"到点跑一次\"的语义：不补跑（ADR-9 只补 daily/weekly 的最近一次）",
				State:        automation.StateInactive,
			})
		case automation.StartupInactive:
			// 已跑完的 once：定义文件里若还写着 active，补一句 inactive（只在需要时写盘）。
			if rec.Def.State == automation.StateInactive || automation.Terminal(rec.Def.State) || rec.Def.LastFiredAt == nil {
				continue
			}
			if err := pl.SaveOverlay(ctx, rec.Def.ID,
				map[string]any{"state": automation.StateInactive}, LastFiredText(rec.Def.LastFiredAt)); err != nil {
				glog.Warnw(ctx, "automation: 补写 inactive 失败", "automation", rec.Def.ID, "err", err)
			}
		}
	}
}

// automationTick 一次扫描（导出给测试直接驱动，不必等 ticker）。
//
// **不在这里重载定义**：重载由真实的调度循环（StartAutomationScheduler 的 ticker goroutine）
// 在每次调用 Tick 前做（`reloadDefinitionsForTick`）。单测直接调 Tick 时会手动拨 in-memory
// 游标来构造边界，Tick 自身重载会把那个拨动冲掉，故保持 Tick 只管"按当前内存态扫一遍"。
func (pl *Plane) Tick(ctx context.Context, now time.Time) {
	for _, rec := range pl.DueSnapshot(now) {
		trigger := automation.TriggerScheduled
		if rec.Startup == automation.StartupRecover {
			trigger = automation.TriggerRecovery
		}
		pl.RunOnce(ctx, rec, rec.NextDue, trigger)
	}
}

// ReloadDefinitionsForTick 按当前载体重载一遍定义（DB 载体读库、否则读定义目录）。
//
// **持 `runMu` 重载**：重载会整体替换 `recs`，若与一次在途的 `RunOnce`（同样持 runMu）重叠，
// 会让那次触发拿到的 `rec` 在临界区里被换掉（重取 `RecByID` 只保证 id 一致，不保证"定义没被
// 另一进程改过"）。串行到 runMu 后面，语义最稳：重载只在两次触发之间发生。
//
// 无载体的纯内存模式（`st==nil && dir==""`）跳过：没有跨进程来源，重载只会白白清空内存态。
func (pl *Plane) ReloadDefinitionsForTick(ctx context.Context) {
	pl.runMu.Lock()
	defer pl.runMu.Unlock()
	pl.mu.Lock()
	st, dir := pl.st, pl.dir
	pl.mu.Unlock()
	if st == nil && dir == "" {
		return
	}
	if st != nil {
		pl.reloadMergeFromStore(ctx, dir)
		return
	}
	pl.reloadMergeFromFiles(dir)
}

// reloadMergeFromStore 每 tick 的**增量**重载（DB 载体）：只重建**定义正文变了/新增**的条目，
// 正文未变的条目**保留内存里的 NextDue/Startup**。
//
// 为什么不能像 `LoadAutomationDefinitionsFromStore` 那样整体重建并按 now 重算：`interval` 的
// "从未触发"分支每次算出的都是 `now+every`（永远在未来），整体重建会把 `NextDue` 每 tick 往后推、
// 永远等不到 due；`once` 过期分支则会算成 expire（没有下一次）而直接不跑。正文没变就没有"重新
// 排期"的理由——保留调度态才是正确语义（与文件载体的 mtime 乐观锁同一取向：变了才重算）。
func (pl *Plane) reloadMergeFromStore(ctx context.Context, dir string) {
	pl.mu.Lock()
	pl.dir = dir
	existing := make(map[string]*Rec, len(pl.recs))
	for id, rec := range pl.recs {
		existing[id] = rec
	}
	pl.mu.Unlock()
	if pl.st == nil {
		return
	}
	rows, err := pl.st.ListAutomationDefinitions(ctx)
	if err != nil {
		glog.Warnw(ctx, "automation 增量重载：读定义失败（保留当前内存态）", "err", err)
		return
	}
	now := time.Now()
	seen := make(map[string]bool, len(rows))
	for i := range rows {
		row := &rows[i]
		seen[row.ID] = true
		if prev, ok := existing[row.ID]; ok && prev.LoadErr == "" && prev.sourceBody == row.Body {
			continue // 正文未变：保留内存调度态（关键——不重算 NextDue）
		}
		rec := loadAutomationRow(row, now)
		if rec.LoadErr != "" {
			glog.Warnw(ctx, "automation 定义装载失败（已留在列表里带 error）", "id", row.ID, "err", rec.LoadErr)
		}
		pl.mu.Lock()
		if pl.recs == nil {
			pl.recs = map[string]*Rec{}
		}
		pl.recs[row.ID] = rec
		pl.mu.Unlock()
	}
	// 删掉载体里已不存在的条目（与整体重建同口径：库里删了不能以幽灵任务留在内存里）。
	pl.mu.Lock()
	for id := range pl.recs {
		if !seen[id] {
			delete(pl.recs, id)
		}
	}
	pl.mu.Unlock()
}

// reloadMergeFromFiles 每 tick 的增量重载（文件载体）：正文（文件原文）未变的条目保留内存调度态。
func (pl *Plane) reloadMergeFromFiles(dir string) {
	pl.mu.Lock()
	pl.dir = dir
	existing := make(map[string]*Rec, len(pl.recs))
	for id, rec := range pl.recs {
		existing[id] = rec
	}
	pl.mu.Unlock()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			glog.Warnw(context.Background(), "automation 增量重载：目录读取失败（保留当前内存态）", "dir", dir, "err", err)
		}
		return
	}
	now := time.Now()
	seen := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		seen[id] = true
		path := filepath.Join(dir, e.Name())
		raw, rerr := os.ReadFile(path)
		if prev, ok := existing[id]; ok && prev.LoadErr == "" && rerr == nil && prev.sourceBody == string(raw) {
			continue // 文件原文未变：保留内存调度态
		}
		rec := loadAutomationFile(path, id, now)
		if rec.LoadErr != "" {
			glog.Warnw(context.Background(), "automation 定义装载失败（已留在列表里带 error）", "id", id, "err", rec.LoadErr)
		}
		pl.mu.Lock()
		if pl.recs == nil {
			pl.recs = map[string]*Rec{}
		}
		pl.recs[id] = rec
		pl.mu.Unlock()
	}
	pl.mu.Lock()
	for id := range pl.recs {
		if !seen[id] {
			delete(pl.recs, id)
		}
	}
	pl.mu.Unlock()
}

// automationRunOnce 认领并执行一条到期的 automation。
//
// 认领三态（`internal/core/automation.Decide`）：Claimed / duplicate / overlap。
// `duplicate` 在前：**"这次触发过没有"必须优先于"上一次还在不在跑"**——否则一个
// "上次会话卡住不结束"的 automation，会在重启后把同一 occurrence 再跑一遍。
func (pl *Plane) RunOnce(ctx context.Context, rec Rec, ScheduledFor time.Time, trigger string) RunOutcome {
	// 认领判定不是原子的（见 automationRunMu 注释）：判定与写回之间必须独占，
	// 否则 tick 与手动触发会为同一个 occurrence 各建一个会话。
	//
	// 进临界区后**必须重取记录**：`rec` 是锁外取的快照，另一路可能已经认领并写回了游标
	// ——拿旧快照判定等于没加锁（三路并发会建出三个会话，这条有专门的用例）。
	pl.runMu.Lock()
	defer pl.runMu.Unlock()
	fresh, ok := pl.RecByID(rec.Def.ID)
	if !ok || fresh.LoadErr != "" || !fresh.Def.Enabled || automation.Terminal(fresh.Def.State) {
		// 快照与临界区之间定义被删/停用/归档：什么都不做（没有新事实可留痕）。
		return RunOutcome{
			ScheduledFor: ScheduledFor, Status: automation.RunSkipped, Trigger: trigger, NoRecord: true,
			Reason: "定义在触发前已被删除、停用或归档（本次不执行）",
		}
	}
	rec = fresh
	claim := automation.Decide(ScheduledFor, rec.Def.LastFiredAt, pl.HasActiveRun(rec))
	switch claim.Status {
	case automation.ClaimDuplicate:
		// 文件里已经记着这次认领（上一轮写回失败 / 重启后重算）：**不写盘、不追加留痕**，
		// 只把内存游标推到下一个 occurrence，并推一条事件（这是"重复触发"唯一可见的痕迹）。
		pl.refreshAutomationSchedule(rec.Def.ID)
		out := RunOutcome{
			ScheduledFor: ScheduledFor, Status: automation.RunDuplicate, Trigger: trigger,
			Reason: claim.Reason, NoRecord: true,
		}
		pl.emitAutomationRun(rec.Def.ID, out)
		return out
	case automation.ClaimOverlap:
		// 理由沿用纯函数里的措辞（那一句本身就是要给用户看的），只在后面补"然后呢"。
		out := RunOutcome{
			ScheduledFor: ScheduledFor, Status: automation.RunOverlap, Trigger: trigger, Advance: true,
			Reason: claim.Reason + "；下一次 occurrence 照常",
		}
		return pl.FinishAutomationRun(ctx, rec, out)
	}

	// **预认领**（Plan §7 P-12）：DB 载体在**建会话之前**用一次 CAS 把 `last_fired_at` 钉到
	// 本次 occurrence 上——同一 occurrence 至多触发一次（at-most-once）。两个 tick 同时到、
	// 或"会话建好了进程却没来得及写回"，都只会有一次真的跑起来。
	//
	// 文件载体不做这一步：那张表靠文件 mtime 乐观锁，跨进程认领它给不了（也正因为如此，
	// 文件载体的崩溃窗口里可能重复跑一次——这是**有意的差异**，不是漏改）。
	//
	// 认领失败**不建会话**、**不推进内存游标**（下一 tick 重试）；认领不到 = 别人先认领了，
	// 与 duplicate 同解（不写、不追加留痕，只推事件）。
	out := RunOutcome{}
	if pl.st != nil && trigger != automation.TriggerManual {
		Claimed, err := pl.claimAutomationOccurrence(ctx, rec, ScheduledFor)
		if err != nil {
			out := RunOutcome{
				ScheduledFor: ScheduledFor, Status: automation.RunSkipped, Trigger: trigger,
				Failure: true, Cause: err,
				Reason: "认领失败（本次未建会话，下一个 tick 重试）：" + errText(err),
			}
			glog.Warnw(ctx, "automation 认领失败", "automation", rec.Def.ID, "err", err)
			pl.emitAutomationRun(rec.Def.ID, out)
			return out
		}
		if !Claimed {
			pl.refreshAutomationSchedule(rec.Def.ID)
			out := RunOutcome{
				ScheduledFor: ScheduledFor, Status: automation.RunDuplicate, Trigger: trigger, NoRecord: true,
				Reason: "认领游标已被推进：这个 occurrence 已经触发过（本次不再建会话）",
			}
			pl.emitAutomationRun(rec.Def.ID, out)
			return out
		}
		out.Claimed = true
	}

	// Claimed：真的执行一次（按 delivery 分派）。
	//
	// **不在这里做第二份"前置校验"**（runtime 是否可用 / cwd 是否还在）：
	// 那份校验必然与真实创建路径漂移，而 ADR-10 真正要求的是"失败也推进游标"——那与校验
	// 在哪一层无关。所以直接调真实路径，把它带码的错误落成 needs_attention 的**原因**，
	// 用户在面板上看到的正是"为什么没跑成"。
	host := pl.hostOf()
	if host == nil {
		return pl.FinishAutomationRun(ctx, rec, RunOutcome{ScheduledFor: ScheduledFor, Status: automation.RunSkipped,
			Trigger: trigger, Advance: true, Failure: true, Claimed: out.Claimed,
			Reason: "宿主端口未接线（装配期漏了 SetHost）：本次跳过"})
	}
	// v35：按 target.delivery 分派。缺省（空串）= session（投递回既有会话）；
	// new_session = 新建会话（与 v35 之前逐字相同）。
	var sessionID string
	var runErr error
	if automation.NormalizeDelivery(rec.Def.Target.Delivery) == automation.DeliverySession {
		runErr = host.DeliverSession(ctx, HostDeliverReq{
			SessionID: rec.Def.Target.SessionID,
			Prompt:    rec.Def.Target.Prompt,
		})
		// 投递到既有会话：last_session_id 记的就是目标会话（不发事件时也要让运行史指向它）。
		sessionID = rec.Def.Target.SessionID
	} else {
		sessionID, runErr = host.CreateSession(ctx, HostSessionReq{
			Prompt: rec.Def.Target.Prompt,
			Cwd:    rec.Def.Target.Cwd,
			Model:  rec.Def.Target.Model,
			Origin: Origin(rec.Def.ID),
		})
	}
	if runErr != nil {
		out := RunOutcome{
			ScheduledFor: ScheduledFor, Status: automation.RunSkipped, Trigger: trigger, Advance: true,
			Failure: true, Cause: runErr, Claimed: out.Claimed,
			Reason: "触发失败（已跳过本次，等修好后的下一个 occurrence）：" + errText(runErr),
		}
		glog.Warnw(ctx, "automation 触发失败", "automation", rec.Def.ID, "err", runErr)
		return pl.FinishAutomationRun(ctx, rec, out)
	}
	out = RunOutcome{
		ScheduledFor: ScheduledFor, Status: automation.RunClaimed, Trigger: trigger,
		SessionID: sessionID,
		// 手动触发不推进游标（见字段注释）。
		Advance: trigger != automation.TriggerManual, Claimed: out.Claimed,
	}
	res := pl.FinishAutomationRun(ctx, rec, out)
	// max_run_duration：可选的**收紧**（默认只靠既有 MATE_RUN_TIMEOUT 看门狗），
	// 走同一条取消通道，不引入第二套超时机制。
	if d, err := rec.Def.RunDuration(); err == nil && d > 0 {
		go pl.automationWatchdog(ctx, rec.Def.ID, sessionID, d)
	}
	return res
}

// runAutomationNow 手动触发一次（`POST /automations/:id/run` 的领域实现；端口形态在 automation_rest.go）。
//
// 走**同一个认领判据**（上一次还在跑 → overlap 跳过）：手动不等于可以并发跑两条会话。
// 但**不推进认领游标**，所以手动试跑不会吃掉下一个排期。
func (pl *Plane) RunNow(ctx context.Context, id string) (RunOutcome, error) {
	rec, ok := pl.RecByID(id)
	if !ok {
		return RunOutcome{}, notFoundAutomation(id)
	}
	if rec.LoadErr != "" {
		return RunOutcome{}, invalidAutomation(rec)
	}
	if !rec.Def.Enabled {
		return RunOutcome{}, automationDisabled(id)
	}
	out := pl.RunOnce(ctx, rec, time.Now(), automation.TriggerManual)
	// 手动触发失败 → **既留痕又报错**：痕迹让运行史完整（"我点过、没跑成"），
	// 报错让用户当场知道为什么（把 skipped 当成功返回等于让面板显示一个绿色的失败）。
	if out.Cause != nil {
		return out, code.FromError(out.Cause)
	}
	return out, nil
}

// automationWatchdog 单次运行的外层超时：到点仍在本轮 → 走既有取消通道。
func (pl *Plane) automationWatchdog(ctx context.Context, automationID, SessionID string, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if pl.hostOf() == nil || !pl.hostOf().SessionRunning(SessionID) {
		return
	}
	glog.Warnw(ctx, "automation 本轮超过 max_run_duration，自动取消",
		"automation", automationID, "session", SessionID, "max_run_duration", d.String())
	// 取消的终态（run.cancelled）由 executeRun 依取消语义发布；这里只负责按下按钮。
	_ = pl.hostOf().CancelRun(context.WithoutCancel(ctx), SessionID)
}

// finishAutomationRun 把结果写回载体（定义文件 / 运行态列）并推事件，然后刷新内存调度态。
//
// 写回失败的语义按载体分岔（Plan §7 P-12），两处都保持"事实可见"这条底线：
//
//	DB 载体：**内存态不推进**（与文件载体相反）。认领已经在建会话之前落库了，所以"记录失败"
//	  不会变成重复触发；下一 tick 看到游标已推进 → duplicate → 不重复跑。这一轮的留痕会丢，
//	  因此错误日志是 ERROR 级：丢了什么必须看得见。
//	文件载体：维持历史语义（写失败不阻断调度，事件里带上失败事实）。
func (pl *Plane) FinishAutomationRun(ctx context.Context, rec Rec, out RunOutcome) RunOutcome {
	if out.NoRecord {
		return out
	}
	now := time.Now().UTC()
	overlay := map[string]any{
		"last_status": out.Status,
		"last_reason": out.Reason,
	}
	if out.Advance {
		overlay["last_fired_at"] = out.ScheduledFor.UTC().Format(time.RFC3339Nano)
	}
	if out.SessionID != "" {
		overlay["last_session_id"] = out.SessionID
	}
	failures := rec.Def.ConsecutiveFailures
	switch {
	case out.Failure:
		failures++
	case out.Status == automation.RunClaimed:
		failures = 0 // 跑起来了 → 之前的连续失败清零（用户修好了）
	}
	overlay["consecutive_failures"] = failures
	state := rec.Def.State
	switch {
	case out.State != "":
		state = out.State
	case out.Failure:
		state = automation.StateNeedsAttention
	case out.Status == automation.RunClaimed || out.Status == automation.RunOverlap:
		state = automation.StateActive // 自愈：跑成了/只是撞车，不该继续停在 needs_attention
	}
	overlay["state"] = state
	// recent_runs 环形缓冲：最新在前，超出上限截断（只保留最近 N 次，文件不无限长）。
	runs := make([]automation.RunRecord, 0, len(rec.Def.RecentRuns)+1)
	runs = append(runs, automation.RunRecord{
		ScheduledFor: out.ScheduledFor.UTC(), FiredAt: now, Status: out.Status,
		Reason: out.Reason, SessionID: out.SessionID, Trigger: out.Trigger,
	})
	runs = append(runs, rec.Def.RecentRuns...)
	if len(runs) > automation.RecentRunsMax {
		runs = runs[:automation.RecentRunsMax]
	}
	overlay["recent_runs"] = runs

	// 认领 CAS 判据：预认领过 → 就是刚写进去的那个值；否则 = 进临界区时读到的旧值
	// （文件载体忽略这个参数，它靠自己的 mtime 乐观锁）。
	expected := LastFiredText(rec.Def.LastFiredAt)
	if out.Claimed {
		expected = out.ScheduledFor.UTC().Format(time.RFC3339Nano)
	}
	if err := pl.SaveOverlay(ctx, rec.Def.ID, overlay, expected); err != nil {
		if pl.st != nil {
			glog.Errorw(ctx, "automation 运行态写回失败（内存调度态不推进，下一 tick 按已认领的游标判为 duplicate，不会重复触发；这一轮留痕丢失）",
				"automation", rec.Def.ID, "err", err)
		} else {
			glog.Errorw(ctx, "automation 运行态写回失败（内存调度态照常推进，重启后可能重跑一次）",
				"automation", rec.Def.ID, "err", err)
		}
	}
	out.State = state
	pl.emitAutomationRun(rec.Def.ID, out)
	return out
}

// emitAutomationRun 推 `automation.run` 事件（**只进 bus，不落 events 表**）。
//
// 为什么不在 publishRaw 里走一遍：events 表的 `session_id` 是
// `NOT NULL REFERENCES sessions(id)`（`foreign_keys(1)` 由 internal/infra/dbclient 的守卫钉着），
// 而 duplicate/overlap/触发失败这三类结果**没有会话可挂**——硬塞一个假 id 会直接撞外键。
// 运行史（含这三类）落在定义文件的 `recent_runs` 里（见 `internal/core/automation.RunRecord` 注释），
// 这条事件只负责**实时推送**（面板不必等轮询）。
func (pl *Plane) emitAutomationRun(id string, out RunOutcome) {
	host := pl.hostOf()
	if host == nil {
		return
	}
	payload := map[string]any{
		"automation_id": id,
		"status":        out.Status,
		"trigger":       out.Trigger,
	}
	if !out.ScheduledFor.IsZero() {
		payload["scheduled_for"] = out.ScheduledFor.UTC().Format(time.RFC3339)
	}
	if out.SessionID != "" {
		payload["session_id"] = out.SessionID
	}
	if out.Reason != "" {
		payload["reason"] = out.Reason
	}
	if out.State != "" {
		payload["state"] = out.State
	}
	// 与其余事件同一个脱敏闸门（理由里可能带路径/环境串）。
	payload = redact.Payload(payload).(map[string]any)
	pl.mu.Lock()
	pl.autoVer[Origin(id)]++
	v := pl.autoVer[Origin(id)]
	pl.mu.Unlock()
	// 版本号在本域算（`autoVer` 是 <code>automation:&lt;id&gt;</code> 聚合的版本计数），通道与类型名归宿主：
	// 刻意**不带事件 id** 这件事由宿主实现负责（`automation.run` 只走实时通道、不落 events 表、
	// 不进 transcript），于是"帧上有 id ⟺ 它在 durable 日志里查得到"这条不变式成立。
	host.EmitRun(context.Background(), id, v, payload)
}

// refreshAutomationSchedule 按当前定义重算内存调度态（duplicate 分支用；不写盘）。
func (pl *Plane) refreshAutomationSchedule(id string) {
	pl.mu.Lock()
	rec := pl.recs[id]
	pl.mu.Unlock()
	if rec == nil {
		return
	}
	updated := *rec
	updated.ApplySchedule(time.Now())
	pl.mu.Lock()
	pl.recs[id] = &updated
	pl.mu.Unlock()
}

// Recs 取全部记录的副本（启动 pass 用；坏定义也在内，由调用方判断）。
func (pl *Plane) Recs() []Rec {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	out := make([]Rec, 0, len(pl.recs))
	for _, rec := range pl.recs {
		out = append(out, *rec)
	}
	return out
}

// automationOrigin 会话来源标记（`sessions.origin`）：`automation:<id>`。
func Origin(id string) string { return "automation:" + id }
