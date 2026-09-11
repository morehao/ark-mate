package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/morehao/ark-forge/internal/model"
	"gorm.io/gorm"
)

// AppendEvent 追加一条事件（facts 唯一写入口的落库侧）：
// 单事务内完成「分配全局 seq（meta.events_last_seq+1）→ 插 events → 更新 conversation.last_seq」。
// 调用方保证 conversation 已存在（conversations 表 FK）。
// payload 序列化为 JSON 文本（map/结构体均可——结构体载荷此前不入 transcript，见设计文档修复清单 #10）。
func (s *Store) AppendEvent(ctx context.Context, conversationID, typ string, payload any) (int64, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("store: marshal payload: %w", err)
	}
	return s.AppendEventJSON(ctx, conversationID, typ, string(b))
}

// AppendEventJSON 同 AppendEvent，但载荷已是 JSON 文本（迁移导入与高频写路径复用，免二次序列化）。
func (s *Store) AppendEventJSON(ctx context.Context, conversationID, typ, payloadJSON string) (int64, error) {
	var seq int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		seq, err = s.appendEventTx(ctx, tx, conversationID, typ, payloadJSON)
		return err
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// appendEventTx 在**已有事务**内追加事件（序号分配 → 落库 → 抬会话水位），
// 供单条追加与批量导入共用，保证两条路径的事务边界与水位语义完全一致。
func (s *Store) appendEventTx(ctx context.Context, tx *gorm.DB, conversationID, typ, payloadJSON string) (int64, error) {
	seq, err := s.meta.WithTx(tx).IncrSeq(ctx)
	if err != nil {
		return 0, err
	}
	ev := &model.Event{
		Seq:            seq,
		ConversationID: conversationID,
		Type:           typ,
		PayloadJSON:    payloadJSON,
		CreatedAt:      nonZeroTime(model.ParseTime(model.NowTS())),
	}
	if err := s.events.WithTx(tx).Insert(ctx, ev); err != nil {
		return 0, fmt.Errorf("store: insert event: %w", err)
	}
	if err := s.conversations.WithTx(tx).TouchLastSeq(ctx, conversationID, seq); err != nil {
		return 0, err
	}
	return seq, nil
}

// EventsAfter 返回某会话 seq > after 的事件（升序，limit 上限；hasMore 表示仍有余量）。
// 服务端事件回放（history 续传/断线补偿）从 events 表读，不再受内存环缓冲淘汰限制。
func (s *Store) EventsAfter(ctx context.Context, conversationID string, after, limit int64) ([]Event, bool, error) {
	return s.events.After(ctx, conversationID, after, limit)
}

// EventCount 统计某会话/全局事件行数（迁移校验用；conversationID 空 = 全库）。
func (s *Store) EventCount(ctx context.Context, conversationID string) (int64, error) {
	return s.events.Count(ctx, conversationID)
}

// EventsAllAfter 全表按 seq 升序游标分页（跨会话；重启恢复注入 bus 用，保证全局号序升序回放）。
func (s *Store) EventsAllAfter(ctx context.Context, after, limit int64) ([]Event, bool, error) {
	return s.events.AllAfter(ctx, after, limit)
}

// EventsBefore 反向分页：返回 seq < before 的最近 limit 条事件（结果仍按 seq 升序）。
// hasMore 表示窗口之外还有更早的事件（即返回的第一条不是该会话最早的一条）。
// before<=0 时按「从最新往回」处理（等价于取末尾 limit 条）。
func (s *Store) EventsBefore(ctx context.Context, conversationID string, before, limit int64) ([]Event, bool, error) {
	return s.events.Before(ctx, conversationID, before, limit)
}
