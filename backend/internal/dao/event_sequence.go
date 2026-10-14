package dao

import (
	"context"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// EventSequenceDao 访问 `event_sequence` 表（每会话一行的事件号序计数器，v21）。
//
// 与 `MetaDao` 同形的一处：本表主键列叫 `session_id`（不是 `id`），所以 gormdao 那批
// "按主键操作"的泛型方法（`GetByID`/`UpdateByID`/`Delete`）在这里**都不适用**，取号必须写成
// 自定义方法（见包的 `MetaDao.Get` 注释）。
type EventSequenceDao struct {
	*gormdao.Dao[model.EventSequenceEntity, model.EventSequenceEntityList, string]
}

// NewEventSequenceDao 构造 EventSequenceDao（getDB 必填，见包注释）。
func NewEventSequenceDao(getDB gormdao.DBGetter) *EventSequenceDao {
	return &EventSequenceDao{Dao: newDao[model.EventSequenceEntity, model.EventSequenceEntityList, string]("EventSequenceDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO（事务内调用不得脱离 tx，否则破坏单写者语义）。
func (d *EventSequenceDao) WithTx(tx *gorm.DB) *EventSequenceDao {
	return &EventSequenceDao{Dao: d.Dao.WithTx(tx)}
}

// NextSeq 在事务内把某会话的号序推进一步并返回新值（`events.seq` 的唯一分配源，v21 起）。
//
// **必须是事务**（调用方持有 tx 并已用 WithTx 绑定）：本仓是单写者模型，号序分配与事件插入
// 必须原子，否则并发 run 会拿到重号（`UNIQUE(session_id, seq)` 会当场把它判成写失败）。
//
// 两步（upsert 取号 → 回读）的语义：
//   - **新会话**：`INSERT` 落一行 `seq = 0`，于是该会话的**第一条事件 seq = 0**
//     （`session.created` 是会话第 0 条事件——设计 §3/D1 与 opencode 同形）；
//   - **已有会话**：撞主键 → `DO UPDATE SET seq = seq + 1`（单调推），回读拿到新值。
//
// 刻意**不用**"先 `MAX(seq)` 再 +1"：那在读侧也成立，但它把不变量（下一号恰好是上一号 +1）
// 放进了调用方的算术里，而且多一次读；upsert 把"推进"这件事本身交给 DB。
// 也**不用** `RETURNING`：`ncruces/go-sqlite3` 支持它，但 gorm 的 `Raw(...).Row()` 回读
// 与本包别处的写法一致，少一个"驱动是否支持"的隐含前提。
func (d *EventSequenceDao) NextSeq(ctx context.Context, sessionID string) (int64, error) {
	if err := d.DB(ctx).Exec(
		`INSERT INTO `+model.TableNameEventSequence+` (session_id, seq) VALUES (?, 0)
		 ON CONFLICT(session_id) DO UPDATE SET seq = seq + 1`, sessionID).Error; err != nil {
		return 0, err
	}
	var seq int64
	if err := d.DB(ctx).Raw(
		`SELECT seq FROM `+model.TableNameEventSequence+` WHERE session_id = ?`, sessionID).
		Row().Scan(&seq); err != nil {
		return 0, err
	}
	return seq, nil
}
