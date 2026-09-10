package dao

import (
	"context"
	"strings"
	"time"

	"github.com/morehao/ark-forge/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// MessageCond 消息查询条件。
type MessageCond struct {
	*gormdao.BaseCond
	ConversationID string
	RunID          string
	Role           string
}

// BuildCondition 实现 gormdao.Cond。
func (c *MessageCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.ConversationID != "" {
		db.Where(tableName+".conversation_id = ?", c.ConversationID)
	}
	if c.RunID != "" {
		db.Where(tableName+".run_id = ?", c.RunID)
	}
	if c.Role != "" {
		db.Where(tableName+".role = ?", c.Role)
	}
}

// MessageDao 访问 messages 表（消息物化投影，可从 events 重建）。
type MessageDao struct {
	*gormdao.Dao[model.Message, model.MessageList, int64]
}

// NewMessageDao 构造 MessageDao（getDB 必填，见包注释）。
func NewMessageDao(getDB gormdao.DBGetter) *MessageDao {
	return &MessageDao{Dao: newDao[model.Message, model.MessageList, int64]("MessageDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *MessageDao) WithTx(tx *gorm.DB) *MessageDao {
	return &MessageDao{Dao: d.Dao.WithTx(tx)}
}

// ReplaceRunMessages 单事务替换某一轮的 messages（先按 run_id 删后插）。
// 可重复调用：保证「从 events 重建」幂等。
func (d *MessageDao) ReplaceRunMessages(ctx context.Context, msgs model.MessageList) error {
	if len(msgs) == 0 {
		return nil
	}
	return d.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("run_id = ?", msgs[0].RunID).Delete(&model.Message{}).Error; err != nil {
			return err
		}
		return tx.Create(&msgs).Error
	})
}

// List 列出某会话全部消息行（seq 升序，同 seq 按自增 id 升序）。
func (d *MessageDao) List(ctx context.Context, conversationID string) (model.MessageList, error) {
	return d.GetListByCond(ctx, &MessageCond{
		BaseCond:       &gormdao.BaseCond{OrderField: "seq_lo ASC, id ASC"},
		ConversationID: conversationID,
	})
}

// TextHit 是一条正文命中（会话内检索用；时间/序号由调用方按需补齐）。
type TextHit struct {
	ConversationID string
	Role           string
	Text           string
}

// SearchUserText 在指定会话的 user 消息中做子串检索（大小写不敏感；LIKE 通配转义）。
func (d *MessageDao) SearchUserText(ctx context.Context, conversationID, q string) ([]TextHit, error) {
	var rows []TextHit
	if err := d.DB(ctx).Model(&model.Message{}).
		Select("conversation_id, role, text").
		Where("conversation_id = ? AND role = ?", conversationID, model.RoleUser).
		Where(`text LIKE ? ESCAPE '\'`, likePattern(q)).
		Order("seq_lo ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Hit 是一条跨会话消息命中（含会话元数据与锚点序号）。
//
// CreatedAt 用 string 承载：列是 TEXT，scan 后由调用方经 model.ParseTime 转换，
// 不依赖 gorm 对「字符串 → time.Time」的隐式解析。
type Hit struct {
	ConversationID    string
	ConversationTitle string
	AgentID           string
	Role              string
	MessageID         int64
	RunID             string
	Seq               int64
	SeqHi             int64
	Text              string
	CreatedAt         string
}

// CreatedAtTime 返回解析后的创建时间（无法解析 → 零值）。
func (h Hit) CreatedAtTime() time.Time { return model.ParseTime(h.CreatedAt) }

// SearchText 跨会话检索 messages 正文（角色不限，大小写不敏感子串；LIKE 通配转义）。
// 排序：created_at 降序、seq 降序；截断 limit。
func (d *MessageDao) SearchText(ctx context.Context, q string, limit int) ([]Hit, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []Hit
	if err := d.DB(ctx).Table(model.TableMessage+" AS m").
		Select(`m.conversation_id, COALESCE(c.title, '') AS conversation_title, COALESCE(c.agent_id, '') AS agent_id,
		        m.role, m.id AS message_id, m.run_id, m.seq_lo AS seq, m.seq_hi, m.text, m.created_at`).
		Joins("LEFT JOIN "+model.TableConversation+" AS c ON c.id = m.conversation_id").
		Where(`m.text LIKE ? ESCAPE '\'`, likePattern(q)).
		Order("m.created_at DESC, m.seq_lo DESC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// likePattern 把检索词包成 LIKE 模式并转义通配符（配合 ESCAPE '\'）。
func likePattern(q string) string {
	s := strings.ReplaceAll(q, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return "%" + s + "%"
}
