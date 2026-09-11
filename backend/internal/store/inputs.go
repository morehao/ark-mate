package store

import (
	"context"
	"time"

	"github.com/morehao/ark-forge/internal/model"
)

// 队列项状态与投递模式的取值别名（权威定义在 internal/model/enum.go）。
// manager 经 store 这一层取值，避免在业务代码里散落裸字面量。
const (
	// InputStatusQueued 等待投递。
	InputStatusQueued = model.InputStatusQueued
	// InputStatusDelivered 已投递。
	InputStatusDelivered = model.InputStatusDelivered
	// InputStatusStale 已作废（进程重启 / 会话执行代际已变）。
	InputStatusStale = model.InputStatusStale

	// InputDeliveryAfterTurn 排队，等当前轮结束再投递。
	InputDeliveryAfterTurn = model.InputDeliveryAfterTurn
	// InputDeliveryInterruptThenPrompt 取消当前轮，随后投递。
	InputDeliveryInterruptThenPrompt = model.InputDeliveryInterruptThenPrompt
)

// ——— inputs 表（§6 busy input 队列） ———

// CreateInput 写入一条待投递的用户输入。
func (s *Store) CreateInput(ctx context.Context, in Input) error {
	return s.inputs.Create(ctx, &in)
}

// ListQueuedInputs 列出某会话待投递的输入（创建时间升序）。
func (s *Store) ListQueuedInputs(ctx context.Context, conversationID string) ([]Input, error) {
	return s.inputs.ListQueued(ctx, conversationID)
}

// ListAllQueuedInputs 列出全库待投递输入（跨会话，创建时间升序；启动恢复用）。
func (s *Store) ListAllQueuedInputs(ctx context.Context) ([]Input, error) {
	return s.inputs.ListAllQueued(ctx)
}

// HeadQueuedInput 取队首待投递输入（无 → (nil, nil)）。
func (s *Store) HeadQueuedInput(ctx context.Context, conversationID string) (*Input, error) {
	return s.inputs.Head(ctx, conversationID)
}

// ClaimQueuedInput 把一条待投递输入置为已投递；false = 已不在 queued（别人先 claim / 已被撤）。
func (s *Store) ClaimQueuedInput(ctx context.Context, id string, at time.Time) (bool, error) {
	return s.inputs.MarkDelivered(ctx, id, at)
}

// StaleQueuedInput 把一条待投递输入置为作废（仅 queued 生效）。
func (s *Store) StaleQueuedInput(ctx context.Context, id string) (bool, error) {
	return s.inputs.MarkStale(ctx, id)
}

// DeleteQueuedInput 删除一条待投递输入（仅 queued；返回是否删除）。
func (s *Store) DeleteQueuedInput(ctx context.Context, conversationID, id string) (bool, error) {
	return s.inputs.Delete(ctx, conversationID, id)
}

// StaleAllQueuedInputs 启动恢复：把全库待投递输入一次性置为作废（等待的那一轮已随进程消失）。
func (s *Store) StaleAllQueuedInputs(ctx context.Context) (int64, error) {
	return s.inputs.MarkAllQueuedStale(ctx)
}
