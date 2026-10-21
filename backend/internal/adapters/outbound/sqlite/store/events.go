package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/morehao/ark-mate/internal/domain/session/event"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/dao"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"gorm.io/gorm"
)

// AppendEvent 追加一条事件（facts 唯一写入口的落库侧）：
// 单事务内完成「分配**本会话**位次（`event_sequence` upsert 取号）→ 插 events → 更新 session.last_seq」。
// 调用方保证 session 已存在（sessions 表 FK）。
// payload 序列化为 JSON 文本（map/结构体均可——结构体载荷此前不入 transcript，见设计文档修复清单 #10）。
//
// id 是行身份（`evt_…`），**由调用方铸造**（v17：铸号点唯一在 `manager.publishRaw`；迁移回填在
// store 的 v17 步骤里）。store 不自己铸——否则「DB 行的 id」与「同一事件在 transcript/帧上的 id」
// 会是两支值，"身份随数据流动"当场失效。
func (s *Store) AppendEvent(ctx context.Context, id, sessionID, typ string, payload any) (int64, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("store: marshal payload: %w", err)
	}
	return s.AppendEventJSON(ctx, id, sessionID, typ, string(b))
}

// AppendEventJSON 同 AppendEvent，但载荷已是 JSON 文本（迁移导入与高频写路径复用，免二次序列化）。
func (s *Store) AppendEventJSON(ctx context.Context, id, sessionID, typ, payloadJSON string) (int64, error) {
	var seq int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		seq, err = s.appendEventTx(ctx, tx, id, sessionID, typ, payloadJSON)
		return err
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// appendEventTx 在**已有事务**内追加事件（位次分配 → 落库 → 抬会话水位），
// 供单条追加与批量导入共用，保证两条路径的事务边界与水位语义完全一致。
//
// 注入的 id 原样落库（不校验、不重铸）：live 路径的 id 由 `manager.publishRaw` 铸，
// 导入路径的 id 来自 transcript 行且**已在 ImportBatch 里过形状与判重**。
//
// **位次是会话内的**（v21）：取号器是 `event_sequence(session_id, seq)`，所以第一条事件的
// seq 是 0、第二条是 1……与会话第一条事件是 `session.created` 这件事逐字对应。
func (s *Store) appendEventTx(ctx context.Context, tx *gorm.DB, id, sessionID, typ, payloadJSON string) (int64, error) {
	seq, err := s.eventSequences.WithTx(tx).NextSeq(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	ev := &model.EventEntity{
		ID:          id,
		Seq:         seq,
		SessionID:   sessionID,
		Type:        typ,
		PayloadJSON: payloadJSON,
		CreatedAt:   nonZeroTime(model.ParseTime(model.NowTS())),
	}
	if err := s.events.WithTx(tx).Insert(ctx, ev); err != nil {
		return 0, fmt.Errorf("store: insert event: %w", err)
	}
	if err := s.touchLastSeqTx(ctx, tx, sessionID, seq); err != nil {
		return 0, err
	}
	return seq, nil
}

// defaultEventPage 与 defaultEventPageAll 是游标分页的默认页大小
// （调用方传 <=0 时生效；两者不同源：单会话回放按 200，重启恢复的跨会话分页按 2000）。
const (
	defaultEventPage    = int64(200)
	defaultEventPageAll = int64(2000)
)

// EventsAfter 返回某会话 seq > after 的事件（升序，limit 上限；hasMore 表示仍有余量）。
// 服务端事件回放（history 续传/断线补偿）从 events 表读，不再受内存环缓冲淘汰限制。
//
// **after = -1 = 从该会话第 0 条起**（"没有游标"的哨兵）；`after = 0` 表示"第 0 条之后"。
//
// 游标只认 seq：v21 起 seq 是会话内位次（0 起、稠密），所以 `seq = 0` 是合法位次，
// 本方法一律按开区间比较（`seq > after`），哨兵语义由调用方给定。
func (s *Store) EventsAfter(ctx context.Context, sessionID string, after, limit int64) ([]event.Item, bool, error) {
	if limit <= 0 {
		limit = defaultEventPage
	}
	list, err := s.scanEvents(ctx, s.events.DB(ctx).
		Where("session_id = ? AND seq > ?", sessionID, after).
		Order("seq ASC").Limit(int(limit+1)))
	items, hasMore, err := truncateEvents(list, int(limit), err)
	if err != nil {
		return nil, false, err
	}
	return eventItemsOf(items), hasMore, nil
}

// EventCount 统计某会话/全局事件行数（迁移校验用；sessionID 空 = 全库）。
func (s *Store) EventCount(ctx context.Context, sessionID string) (int64, error) {
	return s.events.CountByCond(ctx, &dao.EventCond{SessionID: sessionID})
}

// EventsAllBySession 全表按 `(session_id, seq)` 升序做复合 keyset 分页（跨会话）。
//
// 消费面只有一处：重启恢复把历史事件注入 eventbus。v21 起位次是会话内的量、**没有跨会话全序**，
// 所以注入只需要"组内按 seq 升序"——按 `(session_id, seq)` 取自然同时满足分组与组内有序。
//
// 为什么不是 v17 的那条 `WHERE seq > ?`（全表全局号序游标）：v21 起 `seq` 是会话内量，
// 它**不再全库唯一**、也**不再跨会话可比**——拿它当全表游标会在第二个会话处回首（"seq > 5"
// 在每个会话里都成立），回放顺序与取号顺序脱钩。复合游标把排序键写进谓词，顺序由 DB 定。
func (s *Store) EventsAllBySession(ctx context.Context, afterSessionID string, afterSeq, limit int64) ([]event.Item, bool, error) {
	if limit <= 0 {
		limit = defaultEventPageAll
	}
	q := s.events.DB(ctx)
	if afterSessionID != "" {
		q = q.Where("session_id > ? OR (session_id = ? AND seq > ?)", afterSessionID, afterSessionID, afterSeq)
	}
	list, err := s.scanEvents(ctx, q.Order("session_id ASC, seq ASC").Limit(int(limit+1)))
	items, hasMore, err := truncateEvents(list, int(limit), err)
	if err != nil {
		return nil, false, err
	}
	return eventItemsOf(items), hasMore, nil
}

// EventsBefore 反向分页：返回 seq < before 的最近 limit 条事件（结果仍按 seq 升序）。
// hasMore 表示窗口之外还有更早的事件（即返回的第一条不是该会话最早的一条）。
// **before < 0 时按「从最新往回」处理**（哨兵）；`before = 0` = "第 0 条之前" → 空页。
func (s *Store) EventsBefore(ctx context.Context, sessionID string, before, limit int64) ([]event.Item, bool, error) {
	if limit <= 0 {
		limit = defaultEventPage
	}
	// 先按 seq 降序多取一条（判定是否还有更早），再在内存里反转成升序。
	q := s.events.DB(ctx).Where("session_id = ?", sessionID)
	if before >= 0 {
		q = q.Where("seq < ?", before)
	}
	list, err := s.scanEvents(ctx, q.Order("seq DESC").Limit(int(limit+1)))
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
	return eventItemsOf(out), hasMore, nil
}

// FindEventByIDTx 按行身份取一条事件（导入判重与分叉检测用；找不到返回 ok=false）。
//
// 事务内版本：导入判重必须读同一事务的快照。
func (s *Store) FindEventByIDTx(ctx context.Context, tx *gorm.DB, id string) (*event.Item, bool, error) {
	ev, err := s.events.WithTx(tx).GetByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if ev == nil {
		return nil, false, nil
	}
	item := eventItemOf(*ev)
	return &item, true, nil
}

// scanEvents 执行查询并扫描事件行。
func (s *Store) scanEvents(ctx context.Context, q *gorm.DB) (model.EventEntityList, error) {
	out := model.EventEntityList{}
	if err := q.Model(&model.EventEntity{}).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// truncateEvents 按 limit 截断并给出 hasMore（配合「多取一条」的调用约定）。
func truncateEvents(list model.EventEntityList, limit int, err error) (model.EventEntityList, bool, error) {
	if err != nil {
		return nil, false, err
	}
	if len(list) > limit {
		return list[:limit], true, nil
	}
	return list, false, nil
}

// ——— 行 ↔ 聚合映射（C1：领域自持 event.Item）———

func eventItemOf(e model.EventEntity) event.Item {
	return event.Item{
		ID: e.ID, Seq: e.Seq, SessionID: e.SessionID, Type: e.Type,
		PayloadJSON: e.PayloadJSON, CreatedAt: e.CreatedAt,
	}
}

func eventItemsOf(rows []model.EventEntity) []event.Item {
	out := make([]event.Item, 0, len(rows))
	for _, e := range rows {
		out = append(out, eventItemOf(e))
	}
	return out
}
