package sessionruntime

// ——— 排队输入的**执行面能力**（P5）———
//
// `delivery` 三值（reject / after_run / interrupt_then_prompt）的用例语义在
// `service/svcsession`（`PromptSession` 校验 + `DeliverQueuedNow` 的 no-op 三种情况）；
// 本层只剩"投递这个动作"——它要与在途状态、收尾路径共用同一份事实（`Runner.DeliverQueued`），
// 所以实现必须留在执行面。
//
// 为什么 `DeliverQueued` 没有 ctx/error：投递是**本地动作**（不发起 I/O、不等待），
// 没有执行面的路径本来就该静默 no-op。把它写成 error 只会让每个调用点重复一段"忽略错误"。

// DeliverQueued 若该会话有活跃句柄，立刻投递队首排队输入；否则 no-op。
//
// 忙时那一轮结束的收尾路径会自然投队首（此时排队项已被排到第一位），所以这里不需要分支。
func (s *Service) DeliverQueued(sessionID string) {
	if s.orch == nil || sessionID == "" {
		return
	}
	if as := s.orch.session(sessionID); as != nil {
		s.orch.runner.DeliverQueued(as)
	}
}

// CancelElicitations 归档时作废该会话的挂起提问（`reason:archive`，ADR-11）。
//
// 它实现 `svcsession.MetaDeps` 的端口：走 `CancelSessionReason` 而不是直接删表项——那一支会把
// `cancel` 投给正在等的那一轮、再由终局回调发 `elicitation_resolved{action:cancel,reason:archive}`。
func (s *Service) CancelElicitations(sessionID string) {
	if s.orch == nil || sessionID == "" {
		return
	}
	s.orch.CancelElicitations(sessionID)
}
