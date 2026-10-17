package manager

import (
	"context"
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
//
// **投递本体**（`deliverQueued`）随轮次状态机进了 `domain/session.Runner`（P4a 正文第五十五刀）：
// 它要与在途状态、收尾路径共用同一份"谁在投递"的事实，留在本层就会出现两份。

// DeliverQueuedNow 会话**空闲**时立刻投递队首排队输入（「立即发送」在空闲态的落点）。
//
// 三种情况都是 no-op 且**都不该报错**：
//   - 没有执行面（离线命令 / 测试夹具）：队列只是一份状态，没有人会跑它；
//   - 会话句柄不在活跃集合里：没有活连接就没有"现在"这一说，下次 prompt 重建句柄时
//     队首会按正常路径投递；
//   - 会话正忙：那一轮结束时收尾路径会投队首——而此刻排队项已经被排到第一位。
//
// 为什么放在 Service 而不是让它自己启动一轮：投递只有**一份实现**（Runner.DeliverQueued），
// 多了第二份之后"谁在投递"就再也说不清，而两条路在并发下的分叉表现是重复投递或丢投递。
func (s *Service) DeliverQueuedNow(ctx context.Context, id string) error {
	if s.orch == nil || id == "" {
		return nil
	}
	if as := s.orch.session(id); as != nil {
		s.orch.runner.DeliverQueued(as)
	}
	return nil
}
