package store

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/dao"
)

// UpsertConversation 写入（覆盖）一条会话记录。last_seq 采用触顶语义（MAX），避免误清零。
func (s *Store) UpsertConversation(ctx context.Context, sess Conversation) error {
	sess.CreatedAt = nonZeroTime(sess.CreatedAt)
	sess.UpdatedAt = nonZeroTime(sess.UpdatedAt)
	return s.conversations.Upsert(ctx, &sess)
}

// GetConversation 读取单个会话。
func (s *Store) GetConversation(ctx context.Context, id string) (Conversation, bool, error) {
	row, err := s.conversations.GetByID(ctx, id)
	if err != nil {
		return Conversation{}, false, err
	}
	if row == nil {
		return Conversation{}, false, nil
	}
	return *row, true, nil
}

// ListConversations 列出全部会话（updated_at 降序；archived=nil 表示不过滤）。
func (s *Store) ListConversations(ctx context.Context, archived *bool) ([]Conversation, error) {
	list, err := s.conversations.List(ctx, archived)
	if err != nil {
		return nil, err
	}
	return list, nil
}

// UpdateConversationMeta 更新会话元数据子集（title/pinned/archived/status 的 nil 指针 = 不更新；
// updated_at 恒刷新）。
func (s *Store) UpdateConversationMeta(ctx context.Context, id string, meta ConversationMetaPatch) error {
	return s.conversations.UpdateMeta(ctx, id, meta)
}

// nonZeroTime 把零值时间替换为当前时刻（写入路径的默认值语义）。
func nonZeroTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// CountConversations 会话行数（迁移校验用）。
func (s *Store) CountConversations(ctx context.Context) (int64, error) {
	return s.conversations.CountByCond(ctx, &dao.ConversationCond{})
}
