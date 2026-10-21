package automation

// ——— 投递 outbox（v33 / T5）：把"触发"与"送达"拆开 ———
//
// 改造前两者是同一条路：`RunOnce` 里直接调 `host.CreateSession`，一失败那一轮就只剩一句 reason
// 留在 `recent_runs` 里——**没有可重试的落点**。而底座一时不可用（重启、端口占用、恢复中）是
// 常态而非例外，代价是"今晚的巡检永久丢一次"，用户只会在第二天发现没跑。
//
// 现在的分工（与 T5 的口径一字对应）：
//
//	执行侧（`RunOnce`）：认领 occurrence → **写一行 pending**（幂等键 = automation|occurrence|trigger）
//	                     → 顺手投一次（成功即 sent；失败留 pending，本轮的 reason 写"待重试"）
//	投递侧（worker）   ：每隔一段取**到期**的 pending → 投 → sent / 退避重试 / 次数用尽落 failed
//
// 三条边界，写在这里免得下一个人以为是漏做：
//
//	① **只对 DB 载体生效**：文件载体与 memory 模式没有库，投递仍是同步的（老语义：失败即
//	   `Failure` + needs_attention）。差异是**刻意的**，与"文件载体崩溃窗口内可能重跑一次"同源。
//	② **不改 ADR-10**：认领游标照旧"失败也推进"（否则同一个错过每 30s 重报一次）。outbox 记的是
//	   **投递**，不是认领——两者不打架：一个 occurrence 认领一次，投递可以重试多次。
//	③ **只投一次**：第一道闸是库级唯一索引（重复入队不增行），第二道是投递前/后的状态 CAS
//	   （`WHERE status = pending`）。消费端不做幂等（会话是平台自己建的，重复建会话没有
//	   可去的去重键），所以去重放在**发送端**——写清楚在这里。
//
// 状态机：pending --(投成功)--> sent
//                --(投失败, 还有次数)--> pending（attempts+1, next_attempt_at 退避）
//                --(次数用尽)--> failed（ERROR 留痕：默默放弃与还在重试看起来一样）

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	arow "github.com/morehao/ark-mate/internal/domain/automation/row"
	"github.com/morehao/ark-mate/internal/platform/automation"
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/golib/glog"
)

const (
	// outboxMaxAttempts 一条投递最多尝试几次（含即时那次）：5 次退避后仍不行，就该有人看了。
	outboxMaxAttempts = 6
	// outboxBaseBackoff 首次退避（之后按 2 的幂增长）。
	outboxBaseBackoff = 30 * time.Second
	// outboxMaxBackoff 退避上限（一条卡住的投递不该把 next_attempt_at 推到几小时后）。
	outboxMaxBackoff = 30 * time.Minute
)

// OutboxKey 幂等键（同一 occurrence 的同一触发来源只有一条投递）。
func OutboxKey(automationID, occurrence, trigger string) string {
	return automationID + "|" + occurrence + "|" + trigger
}

// outboxBackoff 第 attempts 次失败之后的退避（attempts 从 1 起）。
func outboxBackoff(attempts int64) time.Duration {
	if attempts <= 1 {
		return outboxBaseBackoff
	}
	d := outboxBaseBackoff
	for i := int64(1); i < attempts; i++ {
		d *= 2
		if d >= outboxMaxBackoff {
			return outboxMaxBackoff
		}
	}
	return d
}

// deliveryPayload 投递入参的落库形态（`HostSessionReq` 的直投影）。
//
// 只存"建哪个会话"，不存运行结果：结果在会话里（ADR-4 不变）。
func deliveryPayload(req HostSessionReq) (string, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("投递入参序列化失败: %w", err)
	}
	return string(b), nil
}

// deliverThroughOutbox 入队一条投递并**立刻**投一次。
//
// 返回：
//   - sessionID 非空 = 已经投出去了（outbox 行落 sent）；
//   - pending = 入队成功、但这次没投出去（留在 outbox 等 worker 重试）。**这不是错误**：
//     执行侧的事实（"这个 occurrence 已认领"）已经成立，通道的问题不该改写成"这一轮失败"；
//   - err 非空 = 连入队都失败（库不可用）。调用方才该按 Failure 处理。
func (pl *Plane) deliverThroughOutbox(ctx context.Context, rec Rec, trigger string, scheduledFor time.Time) (sessionID string, pending bool, err error) {
	if pl.st == nil {
		// 无库载体：调用方走同步投递（见文件头 ①）。
		return "", false, errNoOutboxStore
	}
	req := HostSessionReq{
		Prompt: rec.Def.Target.Prompt,
		Cwd:    rec.Def.Target.Cwd,
		Model:  rec.Def.Target.Model,
		Origin: Origin(rec.Def.ID),
	}
	payload, err := deliveryPayload(req)
	if err != nil {
		return "", false, err
	}
	occurrence := scheduledFor.UTC().Format(time.RFC3339Nano)
	now := nowFn().UTC()
	id, inserted, err := pl.st.EnqueueAutomationDelivery(ctx, arow.OutboxItem{
		AutomationID:   rec.Def.ID,
		Occurrence:     occurrence,
		Trigger:        trigger,
		Payload:        payload,
		IdempotencyKey: OutboxKey(rec.Def.ID, occurrence, trigger),
		Status:         arow.OutboxPending,
		NextAttemptAt:  now,
		CreatedAt:      now,
	})
	if err != nil {
		return "", false, err
	}
	if !inserted {
		// 同一个 occurrence 已经入过队（上一次入队后崩溃 / 重放）：**不再投第二次**，
		// 交给 worker 接手那一行。这正是"补投且只投一次"的落点。
		glog.Infow(ctx, "automation 投递已在队列里（本次不重复投）",
			"automation", rec.Def.ID, "occurrence", occurrence, "trigger", trigger, "outbox_id", id)
		return "", true, nil
	}
	it, _, err := pl.st.AutomationOutboxByKey(ctx, OutboxKey(rec.Def.ID, occurrence, trigger))
	if err != nil {
		return "", true, nil // 读不回来不算投递失败：worker 那条路会再试
	}
	return pl.attemptDeliveryRow(ctx, it)
}

// errNoOutboxStore 无库载体（文件载体 / memory 模式）：调用方回落到同步投递。
var errNoOutboxStore = fmt.Errorf("automation: 该载体没有投递队列")

// attemptDeliveryRow 对一行 outbox 尝试投递一次，并把结果写回该行。
func (pl *Plane) attemptDeliveryRow(ctx context.Context, it arow.OutboxItem) (sessionID string, pending bool, err error) {
	var req HostSessionReq
	if uerr := json.Unmarshal([]byte(it.Payload), &req); uerr != nil {
		// 载荷坏了：重试不会自己好，直接放弃并留痕（比"每 30s 重试一次坏 JSON"干净）。
		_, _ = pl.st.RecordAutomationDeliveryAttempt(ctx, it.ID, it.Attempts+1, nowFn().UTC(),
			"载荷无法解析："+errText(uerr), true, nowFn().UTC())
		glog.Errorw(ctx, "automation 投递载荷损坏，已放弃", "automation", it.AutomationID, "outbox_id", it.ID, "err", uerr)
		return "", false, nil
	}
	host := pl.hostOf()
	if host == nil {
		// 宿主端口没接线不是通道的问题（重启也不会好），但也不必抛：留 pending + 退避，
		// 装配修好后 worker 会补投——这正是 outbox 相对同步投递多出来的那点韧性。
		return "", true, nil
	}
	sid, derr := host.CreateSession(ctx, req)
	now := nowFn().UTC()
	if derr != nil {
		attempts := it.Attempts + 1
		giveUp := attempts >= outboxMaxAttempts
		next := now.Add(outboxBackoff(attempts))
		if _, rerr := pl.st.RecordAutomationDeliveryAttempt(ctx, it.ID, attempts, next, errText(derr), giveUp, now); rerr != nil {
			glog.Warnw(ctx, "automation 投递失败且状态写回失败（下一轮会重试同一行）",
				"automation", it.AutomationID, "outbox_id", it.ID, "err", rerr)
		}
		if giveUp {
			glog.Errorw(ctx, "automation 投递已放弃（尝试次数用尽）",
				"automation", it.AutomationID, "outbox_id", it.ID, "attempts", attempts, "err", derr)
		} else {
			glog.Warnw(ctx, "automation 投递失败，已排下次重试",
				"automation", it.AutomationID, "outbox_id", it.ID, "attempts", attempts,
				"next_attempt_at", next.Format(time.RFC3339), "err", derr)
		}
		return "", true, nil
	}
	if _, merr := pl.st.MarkAutomationDeliverySent(ctx, it.ID, now); merr != nil {
		// 会话已经建起来了，只是"标记"没落库：下一轮 worker 会**再投一次**（多建一个会话）。
		// 这类错误必须响亮——它是本文件唯一一处无法靠 CAS 挡住的双投窗口。
		glog.Errorw(ctx, "automation 投递成功但状态写回失败（下一轮可能重复投递）",
			"automation", it.AutomationID, "outbox_id", it.ID, "session", sid, "err", merr)
	}
	return sid, false, nil
}

// DrainOutbox 跑一轮投递（worker 的一次 tick；测试直接调它，不必等 ticker）。
//
// 返回 sent / retried（还留在 pending）/ failed（这一轮放弃）三档计数，只用于日志与测试。
func (pl *Plane) DrainOutbox(ctx context.Context, limit int) (sent, retried, failed int) {
	if pl.st == nil {
		return 0, 0, 0
	}
	due, err := pl.st.DueAutomationDeliveries(ctx, nowFn().UTC(), limit)
	if err != nil {
		glog.Warnw(ctx, "automation 投递队列读取失败（本轮不投）", "err", err)
		return 0, 0, 0
	}
	for _, it := range due {
		sid, pending, _ := pl.attemptDeliveryRow(ctx, it)
		switch {
		case sid != "":
			sent++
			pl.attachRunWatchdog(it.AutomationID, sid)
		case pending:
			retried++
		default:
			failed++
		}
	}
	if sent+retried+failed > 0 {
		glog.Infow(ctx, "automation 投递队列一轮结束", "sent", sent, "retried", retried, "failed", failed)
	}
	return sent, retried, failed
}

// StartAutomationOutboxWorker 起投递 worker（幂等；interval<=0 或无库载体 → 不起）。
//
// 为什么单独一条 goroutine 而不是塞进调度 tick：两者的节拍与失败语义不同——调度 tick 关心
// "该不该触发"，投递 worker 关心"还欠谁一次送达"，混在一起会让"退避"变成"调度也被拖慢"。
func (pl *Plane) StartAutomationOutboxWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 || pl.st == nil {
		return
	}
	pl.outboxOnce.Do(func() {
		go func() {
			t := time.NewTicker(interval)
			defer t.Stop()
			glog.Infow(ctx, "automation 投递 worker 已启动", "interval", interval.String())
			for {
				select {
				case <-ctx.Done():
					glog.Infow(ctx, "automation 投递 worker 退出（ctx 结束）")
					return
				case <-t.C:
					pl.DrainOutbox(ctx, 0)
				}
			}
		}()
	})
}

// attachRunWatchdog 重投成功之后补挂 max_run_duration 看门狗。
//
// 即时投递成功那条路在看门狗是在 `RunOnce` 里挂的；重投成功时那条路径早走完了，所以这里补一次
// ——否则"重试成功的那一轮"会绕过用户声明的运行时长上限（一处不对称，两处行为就不同）。
func (pl *Plane) attachRunWatchdog(automationID, sessionID string) {
	rec, ok := pl.RecByID(automationID)
	if !ok {
		return
	}
	d, err := rec.Def.RunDuration()
	if err != nil || d <= 0 {
		return
	}
	go pl.automationWatchdog(context.WithoutCancel(context.Background()), automationID, sessionID, d)
}

// Deliver 投递一次触发。
//
// 分派规则只有一条：**有库载体且不是手动触发 → 走 outbox**（失败留在队列里重试），
// 其余情况同步投递（老语义：失败即 Failure + needs_attention）。手动触发不走队列是刻意的：
// 用户在等一个回答，`POST /automations/:id/run` 必须当场把真实错误交回去，而不是回一句
// "已排重试"。
func (pl *Plane) Deliver(ctx context.Context, rec Rec, trigger string, scheduledFor time.Time) (sessionID string, pending bool, err error) {
	if pl.st != nil && trigger != automation.TriggerManual {
		return pl.deliverThroughOutbox(ctx, rec, trigger, scheduledFor)
	}
	host := pl.hostOf()
	if host == nil {
		return "", false, code.NewError(code.ClassService, "automation_host_missing",
			"宿主端口未接线（装配期漏了 SetHost）：本次无法投递", nil)
	}
	sid, derr := host.CreateSession(ctx, HostSessionReq{
		Prompt: rec.Def.Target.Prompt,
		Cwd:    rec.Def.Target.Cwd,
		Model:  rec.Def.Target.Model,
		Origin: Origin(rec.Def.ID),
	})
	return sid, false, derr
}
