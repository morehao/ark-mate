package store

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/dao"
	"github.com/morehao/ark-mate/internal/model"
)

// UpsertSession 写入（覆盖）一条会话记录。last_seq 采用触顶语义（MAX），避免误清零。
func (s *Store) UpsertSession(ctx context.Context, sess model.SessionEntity) error {
	sess.CreatedAt = nonZeroTime(sess.CreatedAt)
	sess.UpdatedAt = nonZeroTime(sess.UpdatedAt)
	return s.sessions.Upsert(ctx, &sess)
}

// GetSession 读取单个会话。
func (s *Store) GetSession(ctx context.Context, id string) (model.SessionEntity, bool, error) {
	row, err := s.sessions.GetByID(ctx, id)
	if err != nil {
		return model.SessionEntity{}, false, err
	}
	if row == nil {
		return model.SessionEntity{}, false, nil
	}
	return *row, true, nil
}

// ListSessions 列出全部会话（updated_at 降序；archived=nil 表示不过滤）。
func (s *Store) ListSessions(ctx context.Context, archived *bool) ([]model.SessionEntity, error) {
	list, err := s.sessions.List(ctx, archived)
	if err != nil {
		return nil, err
	}
	return list, nil
}

// UpdateSessionMeta 更新会话元数据子集（title/pinned/archived/status 的 nil 指针 = 不更新；
// updated_at 恒刷新）。
func (s *Store) UpdateSessionMeta(ctx context.Context, id string, meta model.SessionMetaPatch) error {
	return s.sessions.UpdateMeta(ctx, id, meta)
}

// nonZeroTime 把零值时间替换为当前时刻（写入路径的默认值语义）。
func nonZeroTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// CountSessions 会话行数（迁移校验用）。
func (s *Store) CountSessions(ctx context.Context) (int64, error) {
	return s.sessions.CountByCond(ctx, &dao.SessionCond{})
}
