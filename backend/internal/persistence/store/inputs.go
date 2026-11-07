package store

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/dao"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// 队列项状态与投递模式的取值别名（权威定义在 internal/persistence/model/enum.go）。
// manager 经 store 这一层取值，避免在业务代码里散落裸字面量。
const (
	// InputStatusQueued 等待投递。
	InputStatusQueued = model.InputStatusQueued
	// InputStatusDelivered 已投递。
	InputStatusDelivered = model.InputStatusDelivered
	// InputStatusStale 已作废（进程重启 / 会话执行代际已变）。
	InputStatusStale = model.InputStatusStale

	// InputDeliveryAfterRun 排队，等当前轮结束再投递。
	InputDeliveryAfterRun = model.InputDeliveryAfterRun
	// InputDeliveryInterruptThenPrompt 取消当前轮，随后投递。
	InputDeliveryInterruptThenPrompt = model.InputDeliveryInterruptThenPrompt
)

// ——— inputs 表（§6 busy input 队列） ———
//
// 表的列词汇在 `dao.InputCond`；"位次怎么分配"、"队首是谁"、"哪些状态可迁移"都是队列的业务口径，
// 所以在这一层表达。

// 队列的读序（会话内位次升序；跨会话恢复按会话分组、组内位次升序）。
const (
	inputQueueOrder    = "seq ASC"
	inputQueueAllOrder = "session_id ASC, seq ASC"
)

// CreateInput 写入一条待投递的用户输入，**并在同一事务里分配会话内位次 seq**
// （D4：`UNIQUE(session_id, seq)` 是 DB 不变式，所以分配必须与插入原子发生——分开做就会在
// 并发入队时撞约束）。
//
// 取号口径是 `MAX(seq) + 1`（无行 → 0），不是"数一数有几行"：行**不会被删**（撤下/作废都是
// 改 status 的审计行），只有 `DeleteQueuedInput` 会真删一条 queued 行，而那时它的 seq 空出来
// 正好可以复用。
func (s *Store) CreateInput(ctx context.Context, in model.InputEntity) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		d := s.inputs.WithTx(tx)
		var next int64
		if err := d.DB(ctx).Model(&model.InputEntity{}).
			Where("session_id = ?", in.SessionID).
			Select("COALESCE(MAX(seq), -1) + 1").Scan(&next).Error; err != nil {
			return err
		}
		in.Seq = next
		return d.Insert(ctx, &in)
	})
}

// ListQueuedInputs 列出某会话待投递的输入（会话内位次升序）。
func (s *Store) ListQueuedInputs(ctx context.Context, sessionID string) ([]model.InputEntity, error) {
	return s.inputs.GetListByCond(ctx, &dao.InputCond{
		BaseCond:  &gormdao.BaseCond{OrderField: inputQueueOrder},
		SessionID: sessionID,
		Status:    model.InputStatusQueued,
	})
}

// ListAllQueuedInputs 列出全库待投递输入（跨会话，按会话分组、组内位次升序；启动恢复用）。
func (s *Store) ListAllQueuedInputs(ctx context.Context) ([]model.InputEntity, error) {
	return s.inputs.GetListByCond(ctx, &dao.InputCond{
		BaseCond: &gormdao.BaseCond{OrderField: inputQueueAllOrder},
		Status:   model.InputStatusQueued,
	})
}

// HeadQueuedInput 取队首待投递输入（位次最小；无 → (nil, nil)）。
func (s *Store) HeadQueuedInput(ctx context.Context, sessionID string) (*model.InputEntity, error) {
	return s.inputs.GetByCond(ctx, &dao.InputCond{
		BaseCond:  &gormdao.BaseCond{OrderField: inputQueueOrder},
		SessionID: sessionID,
		Status:    model.InputStatusQueued,
	})
}

// ClaimQueuedInput 把一条待投递输入置为已投递；false = 已不在 queued（别人先 claim / 已被撤）。
//
// 条件更新是并发下的唯一仲裁者（`WHERE status = queued`），所以保留手写 UPDATE。
func (s *Store) ClaimQueuedInput(ctx context.Context, id string, at time.Time) (bool, error) {
	res := s.inputs.DB(ctx).Model(&model.InputEntity{}).
		Where("id = ? AND status = ?", id, model.InputStatusQueued).
		Updates(map[string]any{"status": model.InputStatusDelivered, "claimed_at": model.FormatTimeOrNow(at)})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// StaleQueuedInput 把一条待投递输入置为作废（仅 queued 生效）。
func (s *Store) StaleQueuedInput(ctx context.Context, id string) (bool, error) {
	res := s.inputs.DB(ctx).Model(&model.InputEntity{}).
		Where("id = ? AND status = ?", id, model.InputStatusQueued).
		Update("status", model.InputStatusStale)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ReorderQueuedInputs 按给定 id 顺序重写该会话待投递输入的会话内位次（同一事务，两阶段）。
//
// 调用方负责把 ids 传成**该会话当前 queued 行的完整顺序**：漏掉的行会被留在偏移段里
// （排在所有目标之后），而那是"我明明把它排到中间了"最隐蔽的失效方式。
//
// 为什么必须两阶段：`inputs` 上有 `UNIQUE(session_id, seq)`（v22 的 D4），就地互换会中途撞约束
// ——那不会"看起来能跑"：约束直接让事务失败。所以阶段一把整段挪到远离现有区间的偏移段，
// 阶段二按目标顺序写回 0..n-1。
//
// 只动 **queued** 行：已投递/已作废是台账，它们的位次是"当时排第几"的历史事实，重排队列
// 没有资格改历史。
func (s *Store) ReorderQueuedInputs(ctx context.Context, sessionID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	// 偏移量要大于任何现实的队列长度（队列是"人手动排的几句话"，不是数据管道）。
	const stride = 1_000_000
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		d := s.inputs.WithTx(tx)
		if err := d.DB(ctx).Model(&model.InputEntity{}).
			Where("session_id = ? AND status = ?", sessionID, model.InputStatusQueued).
			Update("seq", gorm.Expr("seq + ?", stride)).Error; err != nil {
			return err
		}
		for i, id := range ids {
			if err := d.DB(ctx).Model(&model.InputEntity{}).
				Where("session_id = ? AND id = ? AND status = ?", sessionID, id, model.InputStatusQueued).
				Update("seq", i).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// EditQueuedInput 改一条仍待投递输入的正文与附件（v4 行内编辑；只对 queued 行生效）。
//
// 条件更新 `WHERE status = queued`：已投递/已作废是台账行，改它等于篡改历史；返回 false 让
// 调用方据实报"队项不在队列里"，而不是假装改成功（假成功会让界面去改本地顺序）。
func (s *Store) EditQueuedInput(ctx context.Context, sessionID, id, text, attachments string) (bool, error) {
	res := s.inputs.DB(ctx).Model(&model.InputEntity{}).
		Where("session_id = ? AND id = ? AND status = ?", sessionID, id, model.InputStatusQueued).
		Updates(map[string]any{"text": text, "attachments": attachments})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteQueuedInput 删除一条待投递输入（仅 queued；返回是否删除）。
func (s *Store) DeleteQueuedInput(ctx context.Context, sessionID, id string) (bool, error) {
	res := s.inputs.DB(ctx).Where("session_id = ? AND id = ? AND status = ?",
		sessionID, id, model.InputStatusQueued).Delete(&model.InputEntity{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// StaleAllQueuedInputs 启动恢复：把全库待投递输入一次性置为作废（等待的那一轮已随进程消失）。
func (s *Store) StaleAllQueuedInputs(ctx context.Context) (int64, error) {
	res := s.inputs.DB(ctx).Model(&model.InputEntity{}).
		Where("status = ?", model.InputStatusQueued).
		Update("status", model.InputStatusStale)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
