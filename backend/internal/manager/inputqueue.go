package manager

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/golib/glog"
)

// ——— §6 busy input 队列（排队 / 插话） ———
//
// 改造前：会话正忙时 POST /sessions/:id/prompt 直接报 run_in_progress——用户敲下的字
// 在 UI 上就地丢失（要么等，要么先取消，两条路都不是「我先把这句话交代了」）。现在
// delivery 三值：
//
//	reject（缺省）          忙时报错。历史行为，不产生队列行。
//	after_run             入队；当前轮结束后自动投递。
//	interrupt_then_prompt  入队 + 取消当前轮 → 随后自动投递（服务端原子化：前端「先 cancel
//	                       再 prompt」两步之间会丢窗口——cancel 落地前旧轮可能已自然结束）。
//
// 事实源：sqlite 模式 = inputs 表（投递状态 + 审计台账：谁在何时排了什么、最终投递/撤下/作废）；
// memory 模式 = 进程内队列（进程结束即失效，与「memory 模式以 transcript 为事实源」的既有取向
// 一致——没有库可写时不留半吊子台账）。
//
// 顺序：严格 FIFO（created_at, id）。interrupt_then_prompt **不插队**——先排的先投，否则连按
// 三次「插话」会以逆序执行。投递前校验 Generation（入队时的会话执行代际）：不等 = 该项依赖的
// 那一轮上下文已被重建（连接断开重连 / 会话重开），该项**作废**而不是投进用户没见过的上下文。
//
// 不自动续投的两种收尾：run 失败（避免把队列变成「连续失败重放」）与进程主动停机
// （intentional；重启时全库 queued 行统一置作废——等待的那一轮已随进程消失）。

// 队列项终态（事件收敛用；与 store.InputStatus* 同值）。

// ——— Service 侧队列读写（唯一入口；DB / 内存两实现） ———

// ——— Orchestrator 侧：投递 ———

// deliverQueued 在会话空闲时投递队首排队输入（executeRun 收尾 / 入队后复查调用）。
// 不投递：会话仍忙（下一轮自然会再触发）、句柄已死（重建后代际已变，队列项会作废）、队列空。
func (o *Orchestrator) deliverQueued(as *ActiveSession) {
	as.mu.Lock()
	dead, busy := as.dead, as.busy
	as.mu.Unlock()
	if dead || busy {
		return
	}
	gen := o.svc.sessionGeneration(as.ID)
	q, ok := o.svc.claimNextInput(context.Background(), as.ID, gen)
	if !ok {
		return
	}
	// 先启动新一轮、再发布 delivered：反过来（先发 delivered 再 Prompt）一旦启动失败，
	// 前端会看到队列项凭空消失而其文本从未作为 user_message 出现过。
	if _, err := o.Prompt(context.Background(), as.ID, q.Text); err != nil {
		glog.Warnw(context.Background(), "排队输入投递失败", "session", as.ID, "input_id", q.ID, "err", err)
		o.svc.publish(session.BusInputCancelled, as.ID, map[string]any{
			"session_id": as.ID, "runtime": as.Runtime, "input_id": q.ID,
			"delivery": q.Delivery, "reason": ReasonDeliverFail,
		})
		return
	}
	o.svc.publish(session.BusInputDelivered, as.ID, map[string]any{
		"session_id": as.ID, "runtime": as.Runtime, "input_id": q.ID,
		"delivery": q.Delivery, "reason": ReasonRunFinished,
	})
}
