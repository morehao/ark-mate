package store

import (
	"context"
	"errors"

	"github.com/morehao/ark-mate/internal/persistence/dao"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
)

// ——— 产物溯源登记（v34）的业务口径 ———
//
// 本文件是 `artifacts` 表的读写面。字节不在这里（file 类产物的字节仍走 filestore，用 upload_id 指向）。
// 排序/过滤的组装在 store（dao 只留列词汇，见 guards.md v3.3）。

// ErrArtifactNotFound 产物不存在（供用例层 fail-closed 出口映射 40401）。
var ErrArtifactNotFound = errors.New("store: artifact not found")

// InsertArtifact 插入一条产物行。CreatedAt/UpdatedAt 零值补当前时刻。
func (s *Store) InsertArtifact(ctx context.Context, a model.ArtifactEntity) error {
	now := model.NowTSTime()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = now
	}
	return s.artifacts.Insert(ctx, &a)
}

// ArtifactByID 按 id 取一行；不存在 → (零值, false, nil)。
func (s *Store) ArtifactByID(ctx context.Context, id string) (model.ArtifactEntity, bool, error) {
	a, err := s.artifacts.GetByID(ctx, id)
	if err != nil {
		return model.ArtifactEntity{}, false, err
	}
	if a == nil {
		return model.ArtifactEntity{}, false, nil
	}
	return *a, true, nil
}

// ListArtifactsBySession 取某会话的全部产物（created_at DESC）。
//
// 分页由调用方按 limit/offset 传（单人单机规模足够；要下推再做成 LIMIT/OFFSET）。
func (s *Store) ListArtifactsBySession(ctx context.Context, sessionID string) ([]model.ArtifactEntity, error) {
	var rows model.ArtifactEntityList
	err := s.artifacts.DB(ctx).Model(&model.ArtifactEntity{}).
		Where("session_id = ?", sessionID).
		Order("created_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// UpdateArtifactDirty 设置 dirty 标记（惰性计算后回写）。不存在 → ErrArtifactNotFound。
func (s *Store) UpdateArtifactDirty(ctx context.Context, id string, dirty int) error {
	return s.artifacts.DB(ctx).Model(&model.ArtifactEntity{}).
		Where("id = ?", id).
		Update("dirty", dirty).Error
}

// 断言：ArtifactCond 满足 gormdao.Cond（编译期钉住）。
var _ gormdao.Cond = (*dao.ArtifactCond)(nil)
