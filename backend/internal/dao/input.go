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
	SessionID string
	Status    string
}

// BuildCondition 实现 gormdao.Cond。
func (c *InputCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SessionID != "" {
		db.Where(tableName+".session_id = ?", c.SessionID)
	}
	if c.Status != "" {
		db.Where(tableName+".status = ?", c.Status)
	}
}

// InputDao 访问 inputs 表（§6 busy input 队列）。
type InputDao struct {
	*gormdao.Dao[model.InputEntity, model.InputEntityList, string]
}

// NewInputDao 构造 InputDao（getDB 必填，见包注释）。
func NewInputDao(getDB gormdao.DBGetter) *InputDao {
	return &InputDao{Dao: newDao[model.InputEntity, model.InputEntityList, string]("InputDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *InputDao) WithTx(tx *gorm.DB) *InputDao {
	return &InputDao{Dao: d.Dao.WithTx(tx)}
}

// Create 写入一条队列项，**并在同一事务里分配会话内位次 seq**（D4：`UNIQUE(session_id, seq)`
// 是 DB 不变式，所以分配必须与插入原子发生——分开做就会在并发入队时撞约束）。
//
// 取号口径是 `MAX(seq) + 1`（无行 → 0），不是"数一数有几行"：行**不会被删**（撤下/作废都是
// 改 status 的审计行），只有 `Delete` 会真删一条 queued 行，而那时它的 seq 空出来正好可以复用。
func (d *InputDao) Create(ctx context.Context, in *model.InputEntity) error {
	return d.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var next int64
		if err := tx.Model(&model.InputEntity{}).
			Where("session_id = ?", in.SessionID).
			Select("COALESCE(MAX(seq), -1) + 1").Scan(&next).Error; err != nil {
			return err
		}
		in.Seq = next
		return tx.Create(in).Error
	})
}

// ListQueued 列出某会话待投递的队列项（**会话内位次升序**：队列顺序就是 seq 的顺序，D4）。
func (d *InputDao) ListQueued(ctx context.Context, sessionID string) (model.InputEntityList, error) {
	return d.GetListByCond(ctx, &InputCond{
		BaseCond:  &gormdao.BaseCond{OrderField: "seq ASC"},
		SessionID: sessionID,
		Status:    model.InputStatusQueued,
	})
}

// ListAllQueued 列出**全库**待投递项（跨会话；按会话分组、组内 seq 升序）。
// 只给启动恢复用：作废前先取快照，才能为每条发出 input_cancelled（见 manager.staleQueuedInputsOnRestore）。
func (d *InputDao) ListAllQueued(ctx context.Context) (model.InputEntityList, error) {
	return d.GetListByCond(ctx, &InputCond{
		BaseCond: &gormdao.BaseCond{OrderField: "session_id ASC, seq ASC"},
		Status:   model.InputStatusQueued,
	})
}

// Head 取队首（该会话位次最小的待投递项）；无 → (nil, nil)。
func (d *InputDao) Head(ctx context.Context, sessionID string) (*model.InputEntity, error) {
	var in model.InputEntity
	err := d.DB(ctx).
		Where("session_id = ? AND status = ?", sessionID, model.InputStatusQueued).
		Order("seq ASC").Limit(1).Take(&in).Error
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
	res := d.DB(ctx).Model(&model.InputEntity{}).
		Where("id = ? AND status = ?", id, model.InputStatusQueued).
		Updates(map[string]any{"status": model.InputStatusDelivered, "claimed_at": model.FormatTimeOrNow(at)})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// MarkStale 把一条待投递项置为作废（代际不符 / 撤下时用；仅 queued 生效）。
func (d *InputDao) MarkStale(ctx context.Context, id string) (bool, error) {
	res := d.DB(ctx).Model(&model.InputEntity{}).
		Where("id = ? AND status = ?", id, model.InputStatusQueued).
		Update("status", model.InputStatusStale)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// Delete 删除一条待投递项（**仅 queued**：已投递/已作废是审计行，不可删）；返回是否删除。
func (d *InputDao) Delete(ctx context.Context, sessionID, id string) (bool, error) {
	res := d.DB(ctx).Where("session_id = ? AND id = ? AND status = ?",
		sessionID, id, model.InputStatusQueued).Delete(&model.InputEntity{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// MarkAllQueuedStale 把全库所有待投递项置为作废（启动恢复：等待的那一轮已随进程消失）。
func (d *InputDao) MarkAllQueuedStale(ctx context.Context) (int64, error) {
	res := d.DB(ctx).Model(&model.InputEntity{}).
		Where("status = ?", model.InputStatusQueued).
		Update("status", model.InputStatusStale)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
