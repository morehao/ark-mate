package dao

import (
	"context"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// defaultEventPage 与 defaultEventPageAll 是游标分页的默认页大小
// （调用方传 <=0 时生效；两者不同源：单会话回放按 200，重启恢复的跨会话分页按 2000）。
const (
	defaultEventPage    = int64(200)
	defaultEventPageAll = int64(2000)
)

// EventDao 访问 events 表（facts 的唯一事实源）。
//
// 泛型 ID 自 v17 起是 `string`（行身份 `evt_…`）。**注意：游标只有 `seq`**——`After`/
// `AllBySession`/`Before` 一律按 seq 比较与排序，id 只用于"按身份取一行"（导入判重）。
// 守卫 G1 钉着这条（`Order("id` / `seq` 之外的主键比较都会红）。
//
// v21 起 `seq` 是**会话内**位次（0 起、稠密），于是 `seq = 0` 是一个**合法位次**
// （`session.created`）。"没有游标"与"第 0 条之前"从此是两件事，前者用 `-1` 表达
// （早于一切事件；与 DSH 的 `SessionSeqCursor = SessionSeq | -1` 同义）。本文件的查询方法
// 因此一律按**开区间**比较（`seq > after` / `seq < before`），哨兵语义由调用方给定。
type EventDao struct {
	*gormdao.Dao[model.EventEntity, model.EventEntityList, string]
}

// NewEventDao 构造 EventDao（getDB 必填，见包注释）。
func NewEventDao(getDB gormdao.DBGetter) *EventDao {
	return &EventDao{Dao: newDao[model.EventEntity, model.EventEntityList, string]("EventDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO（序号分配与事件插入必须同事务）。
func (d *EventDao) WithTx(tx *gorm.DB) *EventDao {
	return &EventDao{Dao: d.Dao.WithTx(tx)}
}

// FindByID 按行身份取一条事件（导入判重与分叉检测用；找不到返回 ok=false）。
//
// 刻意**不**用 gormdao 的 `GetByID`（虽然 v17 起 events 的主键列真的叫 id 了）：这里要的是
// 「在**给定事务**里查、且区分"没有这一行"与"查询失败"」这两条语义，写成自定义方法就一目了然。
func (d *EventDao) FindByID(ctx context.Context, id string) (*model.EventEntity, bool, error) {
	var ev model.EventEntity
	err := d.DB(ctx).Where("id = ?", id).Take(&ev).Error
	if err == gorm.ErrRecordNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &ev, true, nil
}

// After 返回某会话 seq > after 的事件（升序）。
// hasMore 表示窗口之外仍有事件（内部多取一条判定，不额外 COUNT）。
func (d *EventDao) After(ctx context.Context, sessionID string, after, limit int64) (model.EventEntityList, bool, error) {
	if limit <= 0 {
		limit = defaultEventPage
	}
	list, err := d.scan(ctx, d.DB(ctx).
		Where("session_id = ? AND seq > ?", sessionID, after).
		Order("seq ASC").Limit(int(limit+1)))
	return truncate(list, int(limit), err)
}

// AllBySession 全表按 `(session_id, seq)` 升序做**复合 keyset 分页**（跨会话；重启恢复
// "按会话分组、组内按 seq 升序"注入事件总线用）。
//
// 为什么不是 v17 的那条 `WHERE seq > ?`（全表全局号序游标）：v21 起 `seq` 是会话内量，
// 它**不再全库唯一**、也**不再跨会话可比**——拿它当全表游标会在第二个会话处回首（"seq > 5"
// 在每个会话里都成立），回放顺序与取号顺序脱钩。复合游标把排序键写进谓词，顺序由 DB 定。
//
// `afterSessionID` 为空 = 从头开始；否则取"该会话内 seq 大于 afterSeq"或"会话 id 更大"的行。
func (d *EventDao) AllBySession(ctx context.Context, afterSessionID string, afterSeq, limit int64) (model.EventEntityList, bool, error) {
	if limit <= 0 {
		limit = defaultEventPageAll
	}
	q := d.DB(ctx)
	if afterSessionID != "" {
		q = q.Where("session_id > ? OR (session_id = ? AND seq > ?)", afterSessionID, afterSessionID, afterSeq)
	}
	list, err := d.scan(ctx, q.Order("session_id ASC, seq ASC").Limit(int(limit+1)))
	return truncate(list, int(limit), err)
}

// Before 反向分页：返回 seq < before 的最近 limit 条事件，结果仍按 seq 升序。
// hasMore 表示窗口之外还有更早的事件。
//
// **`before < 0` = 从最新往回**（哨兵；"第 0 条之前"由 `before = 0` 表达，返回空页）——
// v21 之前哨兵是 `before <= 0`，那会让"取第 0 条之前"与"取末尾一页"撞成同一件事，
// 而 `seq = 0` 现在是 `session.created` 这个**真实存在**的位次。
func (d *EventDao) Before(ctx context.Context, sessionID string, before, limit int64) (model.EventEntityList, bool, error) {
	if limit <= 0 {
		limit = defaultEventPage
	}
	// 先按 seq 降序多取一条（判定是否还有更早），再在内存里反转成升序。
	q := d.DB(ctx).Where("session_id = ?", sessionID)
	if before >= 0 {
		q = q.Where("seq < ?", before)
	}
	list, err := d.scan(ctx, q.Order("seq DESC").Limit(int(limit+1)))
	if err != nil {
		return nil, false, err
	}
	hasMore := int64(len(list)) > limit
	if hasMore {
		list = list[:limit]
	}
	out := make(model.EventEntityList, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	return out, hasMore, nil
}

// Count 统计事件行数（sessionID 空 = 全库；迁移校验用）。
func (d *EventDao) Count(ctx context.Context, sessionID string) (int64, error) {
	q := d.DB(ctx).Model(&model.EventEntity{})
	if sessionID != "" {
		q = q.Where("session_id = ?", sessionID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// scan 执行查询并扫描事件行。
func (d *EventDao) scan(ctx context.Context, q *gorm.DB) (model.EventEntityList, error) {
	out := model.EventEntityList{}
	if err := q.Model(&model.EventEntity{}).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// truncate 按 limit 截断并给出 hasMore（配合「多取一条」的调用约定）。
func truncate(list model.EventEntityList, limit int, err error) (model.EventEntityList, bool, error) {
	if err != nil {
		return nil, false, err
	}
	if len(list) > limit {
		return list[:limit], true, nil
	}
	return list, false, nil
}
