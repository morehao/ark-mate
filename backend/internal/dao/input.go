package dao

import (
	"context"
	"errors"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// InputCond 队列项查询条件。
type InputCond struct {
	*gormdao.BaseCond
	ConversationID string
	Status         string
}

// BuildCondition 实现 gormdao.Cond。
func (c *InputCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.ConversationID != "" {
		db.Where(tableName+".conversation_id = ?", c.ConversationID)
	}
	if c.Status != "" {
		db.Where(tableName+".status = ?", c.Status)
	}
}

// InputDao 访问 inputs 表（§6 busy input 队列）。
type InputDao struct {
	*gormdao.Dao[model.Input, model.InputList, string]
}

// NewInputDao 构造 InputDao（getDB 必填，见包注释）。
func NewInputDao(getDB gormdao.DBGetter) *InputDao {
	return &InputDao{Dao: newDao[model.Input, model.InputList, string]("InputDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *InputDao) WithTx(tx *gorm.DB) *InputDao {
	return &InputDao{Dao: d.Dao.WithTx(tx)}
}

// Create 写入一条队列项。
func (d *InputDao) Create(ctx context.Context, in *model.Input) error {
	return d.DB(ctx).Create(in).Error
}

// ListQueued 列出某会话待投递的队列项（创建时间升序；同刻按 id 升序保证确定性）。
func (d *InputDao) ListQueued(ctx context.Context, conversationID string) (model.InputList, error) {
	return d.GetListByCond(ctx, &InputCond{
		BaseCond:       &gormdao.BaseCond{OrderField: "created_at ASC, id ASC"},
		ConversationID: conversationID,
		Status:         model.InputStatusQueued,
	})
}

// ListAllQueued 列出**全库**待投递项（跨会话，创建时间升序）。
// 只给启动恢复用：作废前先取快照，才能为每条发出 input_cancelled（见 manager.staleQueuedInputsOnRestore）。
func (d *InputDao) ListAllQueued(ctx context.Context) (model.InputList, error) {
	return d.GetListByCond(ctx, &InputCond{
		BaseCond: &gormdao.BaseCond{OrderField: "created_at ASC, id ASC"},
		Status:   model.InputStatusQueued,
	})
}

// Head 取队首（创建时间最早的待投递项）；无 → (nil, nil)。
func (d *InputDao) Head(ctx context.Context, conversationID string) (*model.Input, error) {
	var in model.Input
	err := d.DB(ctx).
		Where("conversation_id = ? AND status = ?", conversationID, model.InputStatusQueued).
		Order("created_at ASC, id ASC").Limit(1).Take(&in).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &in, nil
}

// MarkDelivered 把一条待投递项置为已投递（**仅当它仍处于 queued**：条件更新是并发下的
// 唯一仲裁者，返回 false 表示别人先 claim 了）。
func (d *InputDao) MarkDelivered(ctx context.Context, id string, at time.Time) (bool, error) {
	res := d.DB(ctx).Model(&model.Input{}).
		Where("id = ? AND status = ?", id, model.InputStatusQueued).
		Updates(map[string]any{"status": model.InputStatusDelivered, "claimed_at": model.FormatTimeOrNow(at)})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// MarkStale 把一条待投递项置为作废（代际不符 / 撤下时用；仅 queued 生效）。
func (d *InputDao) MarkStale(ctx context.Context, id string) (bool, error) {
	res := d.DB(ctx).Model(&model.Input{}).
		Where("id = ? AND status = ?", id, model.InputStatusQueued).
		Update("status", model.InputStatusStale)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// Delete 删除一条待投递项（**仅 queued**：已投递/已作废是审计行，不可删）；返回是否删除。
func (d *InputDao) Delete(ctx context.Context, conversationID, id string) (bool, error) {
	res := d.DB(ctx).Where("conversation_id = ? AND id = ? AND status = ?",
		conversationID, id, model.InputStatusQueued).Delete(&model.Input{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// MarkAllQueuedStale 把全库所有待投递项置为作废（启动恢复：等待的那一轮已随进程消失）。
func (d *InputDao) MarkAllQueuedStale(ctx context.Context) (int64, error) {
	res := d.DB(ctx).Model(&model.Input{}).
		Where("status = ?", model.InputStatusQueued).
		Update("status", model.InputStatusStale)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
