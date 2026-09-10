package dao

import (
	"context"

	"github.com/morehao/ark-forge/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// defaultEventPage 与 defaultEventPageAll 是游标分页的默认页大小
// （调用方传 <=0 时生效；两者不同源：单会话回放按 200，全库回放按 2000）。
const (
	defaultEventPage    = int64(200)
	defaultEventPageAll = int64(2000)
)

// EventDao 访问 events 表（facts 的唯一事实源）。
type EventDao struct {
	*gormdao.Dao[model.Event, model.EventList, int64]
}

// NewEventDao 构造 EventDao（getDB 必填，见包注释）。
func NewEventDao(getDB gormdao.DBGetter) *EventDao {
	return &EventDao{Dao: newDao[model.Event, model.EventList, int64]("EventDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO（序号分配与事件插入必须同事务）。
func (d *EventDao) WithTx(tx *gorm.DB) *EventDao {
	return &EventDao{Dao: d.Dao.WithTx(tx)}
}

// After 返回某会话 seq > after 的事件（升序）。
// hasMore 表示窗口之外仍有事件（内部多取一条判定，不额外 COUNT）。
func (d *EventDao) After(ctx context.Context, conversationID string, after, limit int64) (model.EventList, bool, error) {
	if limit <= 0 {
		limit = defaultEventPage
	}
	list, err := d.scan(ctx, d.DB(ctx).
		Where("conversation_id = ? AND seq > ?", conversationID, after).
		Order("seq ASC").Limit(int(limit+1)))
	return truncate(list, int(limit), err)
}

// AllAfter 全表按 seq 升序游标分页（跨会话；重启恢复注入事件总线用，保证全局号序升序回放）。
func (d *EventDao) AllAfter(ctx context.Context, after, limit int64) (model.EventList, bool, error) {
	if limit <= 0 {
		limit = defaultEventPageAll
	}
	list, err := d.scan(ctx, d.DB(ctx).
		Where("seq > ?", after).
		Order("seq ASC").Limit(int(limit+1)))
	return truncate(list, int(limit), err)
}

// Before 反向分页：返回 seq < before 的最近 limit 条事件，结果仍按 seq 升序。
// hasMore 表示窗口之外还有更早的事件；before <= 0 时按「从最新往回」处理。
func (d *EventDao) Before(ctx context.Context, conversationID string, before, limit int64) (model.EventList, bool, error) {
	if limit <= 0 {
		limit = defaultEventPage
	}
	// 先按 seq 降序多取一条（判定是否还有更早），再在内存里反转成升序。
	q := d.DB(ctx).Where("conversation_id = ?", conversationID)
	if before > 0 {
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
	out := make(model.EventList, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	return out, hasMore, nil
}

// Count 统计事件行数（conversationID 空 = 全库；迁移校验用）。
func (d *EventDao) Count(ctx context.Context, conversationID string) (int64, error) {
	q := d.DB(ctx).Model(&model.Event{})
	if conversationID != "" {
		q = q.Where("conversation_id = ?", conversationID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// scan 执行查询并扫描事件行。
func (d *EventDao) scan(ctx context.Context, q *gorm.DB) (model.EventList, error) {
	out := model.EventList{}
	if err := q.Model(&model.Event{}).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// truncate 按 limit 截断并给出 hasMore（配合「多取一条」的调用约定）。
func truncate(list model.EventList, limit int, err error) (model.EventList, bool, error) {
	if err != nil {
		return nil, false, err
	}
	if len(list) > limit {
		return list[:limit], true, nil
	}
	return list, false, nil
}
