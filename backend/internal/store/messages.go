package store

import (
	"context"
	"strings"

	"github.com/morehao/ark-mate/internal/dao"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// MessageHit 一条全局消息命中（形状在 `internal/model`；v3.1 P4 上移）。
type MessageHit = model.MessageHit

// MessageTextHit 一条命中（搜索片段与计数用）。
type MessageTextHit = model.MessageTextHit

// ——— messages 表（可重建读模型：轮内消息物化；events 仍为唯一事实源） ———
//
// 表的列词汇在 `dao.MessageCond`；"按 seq_lo 排"、"整会话先删后插"、"正文子串怎么转义"
// 都是本层的读/写口径。

// messageListOrder 会话内消息的物化顺序（同 seq 按自增 id 兜底稳定）。
const messageListOrder = "seq_lo ASC, id ASC"

// ReplaceSessionMessages 单事务重铺某会话的 messages（先删会话全部旧行再插新行；
// 空批次 = 该会话清空）。这是**生产上的唯一写路径**（manager 的整会话重建）。
//
// 空批次**也要删**（不是 no-op）：一个会话在当前白名单下可能一行都不产出，那时"重建成功"
// 必须意味着"库里没有该会话的旧行"，否则上一代的幽灵消息会留下。
func (s *Store) ReplaceSessionMessages(ctx context.Context, sessionID string, msgs []model.MessageEntity) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("session_id = ?", sessionID).Delete(&model.MessageEntity{}).Error; err != nil {
			return err
		}
		if len(msgs) == 0 {
			return nil
		}
		return tx.Create(&msgs).Error
	})
}

// CreateMessages 裸批量插入（**只给测试直插**）。生产的写路径只有 `ReplaceSessionMessages`；
// 这里刻意不做"先删后插"——直插的行要自己给 `ordinal`（`UNIQUE(session_id, ordinal)` 会咬重号）。
func (s *Store) CreateMessages(ctx context.Context, msgs []model.MessageEntity) error {
	return s.messages.BatchInsert(ctx, msgs)
}

// ListMessages 列出某会话全部消息行（seq 序；测试与后续 REST 用）。
func (s *Store) ListMessages(ctx context.Context, sessionID string) ([]model.MessageEntity, error) {
	return s.messages.GetListByCond(ctx, &dao.MessageCond{
		BaseCond:  &gormdao.BaseCond{OrderField: messageListOrder},
		SessionID: sessionID,
	})
}

// SearchUserMessages 在指定会话的 user 消息中做子串检索（大小写不敏感；LIKE 通配转义），
// 返回全部命中行（service 侧折叠片段/计数）。
func (s *Store) SearchUserMessages(ctx context.Context, sessionID, q string) ([]MessageTextHit, error) {
	var rows []MessageTextHit
	if err := s.messages.DB(ctx).Model(&model.MessageEntity{}).
		Select("session_id, role, text").
		Where("session_id = ? AND role = ?", sessionID, model.RoleUser).
		Where(`text LIKE ? ESCAPE '\'`, likePattern(q)).
		Order("seq_lo ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// messageHitRow 是跨会话检索的 Scan 目标：创建时间列是 TEXT，保持字符串，
// 由 `ParseTime` 显式解析（不依赖 gorm 对「TEXT → time.Time」的隐式解析）。
type messageHitRow struct {
	SessionID    string
	SessionTitle string
	RuntimeID    string
	Role         string
	MessageID    string
	RunID        string
	Seq          int64
	SeqHi        int64
	Text         string
	CreatedAt    string
}

// SearchMessages 跨会话检索 messages 表正文（角色不限，大小写不敏感子串；LIKE 通配转义）。
// 排序：created_at 降序、seq 降序；截断 limit（调用方已校验 >0）。
func (s *Store) SearchMessages(ctx context.Context, q string, limit int) ([]MessageHit, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []messageHitRow
	if err := s.messages.DB(ctx).Table(model.TableNameMessage+" AS m").
		Select(`m.session_id, COALESCE(c.title, '') AS session_title, COALESCE(c.runtime_id, '') AS runtime_id,
		        m.role, m.id AS message_id, m.run_id, m.seq_lo AS seq, m.seq_hi, m.text, m.created_at`).
		Joins("LEFT JOIN "+model.TableNameSession+" AS c ON c.id = m.session_id").
		Where(`m.text LIKE ? ESCAPE '\'`, likePattern(q)).
		Order("m.created_at DESC, m.seq_lo DESC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]MessageHit, 0, len(rows))
	for _, h := range rows {
		out = append(out, MessageHit{
			SessionID:    h.SessionID,
			SessionTitle: h.SessionTitle,
			RuntimeID:    h.RuntimeID,
			Role:         h.Role,
			MessageID:    h.MessageID,
			RunID:        h.RunID,
			Seq:          h.Seq,
			SeqHi:        h.SeqHi,
			Text:         h.Text,
			CreatedAt:    model.ParseTime(h.CreatedAt),
		})
	}
	return out, nil
}

// likePattern 把检索词包成 LIKE 模式并转义通配符（配合 ESCAPE '\'）。
func likePattern(q string) string {
	s := strings.ReplaceAll(q, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return "%" + s + "%"
}
