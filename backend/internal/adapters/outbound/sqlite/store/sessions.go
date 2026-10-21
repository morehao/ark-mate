package store

import (
	"context"
	"fmt"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session/sessionrow"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/dao"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件是 sessions 表的**业务写面/读面**：列词汇（`dao.SessionCond`）与泛型方法在 DAO，
// "覆盖时哪些列要保、按什么排序、水位怎么推"在这里。

// sessionListOrder 会话列表的展示顺序（最近更新在前）。
const sessionListOrder = "updated_at DESC"

// UpsertSession 写入（覆盖）一条会话记录。last_seq 采用触顶语义（MAX），避免误清零。
//
// 两个不能丢的语义：
//   - last_seq 取**触顶**（MAX 旧值, 新值）：并发/重放时不得把已推进的水位写回去；
//   - created_at 也随写入更新（与历史实现一致，导入场景以 transcript header 为准）。
func (s *Store) UpsertSession(ctx context.Context, sess sessionrow.Item) error {
	row := sessionEntityOf(sess)
	row.CreatedAt = nonZeroTime(row.CreatedAt)
	row.UpdatedAt = nonZeroTime(row.UpdatedAt)
	return s.sessions.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"cwd": row.Cwd,
			// 归属列跟着普通持久化走：会话记录一旦建立就不重新解析归属，所以这里传的与
			// 记录里的值必然相同（与 capability_fingerprint 同一条理由）。
			"workspace_id":        row.WorkspaceID,
			"workspace_temporary": model.FlagValue(row.WorkspaceTemporary),
			"model_id":            row.ModelID,
			"runtime_id":          row.RuntimeID,
			"runtime_session_id":  row.RuntimeSessionID,
			"title":               row.Title,
			"pinned":              model.FlagValue(row.Pinned),
			"archived":            model.FlagValue(row.Archived),
			"status":              row.Status,
			"origin":              row.Origin,
			"capability_json":     row.CapabilityJSON,
			// **钉住的指纹**按传入值覆盖：迁移要把它改写成新代际，普通持久化传的是记录里的
			// 同一个值（会话记录一旦建立就不再重新解析），故这里不会产生"悄悄漂移"。
			"capability_fingerprint": row.CapabilityFingerprint,
			"last_seq":               gorm.Expr("MAX("+model.TableNameSession+".last_seq, ?)", row.LastSeq),
			"created_at":             model.FormatTimeOrNow(row.CreatedAt),
			"updated_at":             model.FormatTimeOrNow(row.UpdatedAt),
		}),
	}).Create(&row).Error
}

// upsertSessionForImportTx 是 transcript 导入专用的窄 upsert（**必须在导入事务内**调用）。
//
// 与 UpsertSession 的差异是**有意的**：冲突时只覆盖导入来源确实携带的列，
// 不碰 model_id / runtime_id / runtime_session_id —— 它们是会话级模型选择与底座恢复承载，
// transcript header 里没有，若按全列覆盖语义写入零值，重复导入会把用户已选的模型清空。
func (s *Store) upsertSessionForImportTx(ctx context.Context, tx *gorm.DB, sess *model.SessionEntity) error {
	err := s.sessions.WithTx(tx).DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"cwd":                 sess.Cwd,
			"workspace_temporary": model.FlagValue(sess.WorkspaceTemporary),
			"title":               sess.Title,
			"pinned":              model.FlagValue(sess.Pinned),
			"archived":            model.FlagValue(sess.Archived),
			"status":              sess.Status,
			"last_seq":            gorm.Expr("MAX("+model.TableNameSession+".last_seq, ?)", sess.LastSeq),
			"created_at":          model.FormatTimeOrNow(sess.CreatedAt),
			"updated_at":          model.FormatTimeOrNow(sess.UpdatedAt),
		}),
	}).Create(sess).Error
	if err != nil {
		return fmt.Errorf("store: upsert session %q: %w", sess.ID, err)
	}
	return nil
}

// GetSession 读取单个会话。
func (s *Store) GetSession(ctx context.Context, id string) (sessionrow.Item, bool, error) {
	row, err := s.sessions.GetByID(ctx, id)
	if err != nil {
		return sessionrow.Item{}, false, err
	}
	if row == nil {
		return sessionrow.Item{}, false, nil
	}
	return sessionItemOf(*row), true, nil
}

// ListSessions 列出全部会话（updated_at 降序；archived=nil 表示不过滤）。
func (s *Store) ListSessions(ctx context.Context, archived *bool) ([]sessionrow.Item, error) {
	rows, err := s.sessions.GetListByCond(ctx, &dao.SessionCond{
		BaseCond: &gormdao.BaseCond{OrderField: sessionListOrder},
		Archived: archived,
	})
	if err != nil {
		return nil, err
	}
	return sessionItemsOf(rows), nil
}

// UpdateSessionMeta 更新会话元数据子集（title/pinned/archived/status 的 nil 指针 = 不更新；
// updated_at 恒刷新）。
//
// 空补丁直接返回：不产生 `UPDATE ... SET updated_at = ...` 这种只碰时间戳的写。
func (s *Store) UpdateSessionMeta(ctx context.Context, id string, patch sessionrow.MetaPatch) error {
	if patch.Empty() {
		return nil
	}
	set := map[string]any{"updated_at": model.NowTS()}
	if patch.Title != nil {
		set["title"] = *patch.Title
	}
	if patch.Pinned != nil {
		set["pinned"] = model.FlagValue(*patch.Pinned)
	}
	if patch.Archived != nil {
		set["archived"] = model.FlagValue(*patch.Archived)
	}
	if patch.Status != nil {
		set["status"] = *patch.Status
	}
	if patch.ModelID != nil {
		set["model_id"] = *patch.ModelID
	}
	return s.sessions.UpdateMap(ctx, id, set)
}

// SetSessionModelID **只**写 `model_id`（v26 存量回填专用：不碰 updated_at）。
//
// 为什么不复用 `UpdateSessionMeta`：那个函数**恒刷新 `updated_at`**（对"用户改元数据"是对的），
// 而回填是一次**数据迁移**——它不该把用户的会话列表按「最近更新」重排一遍（列表是按
// `updated_at DESC` 排的）。一次性地改写几百行的 updated_at = 用户看到自己的会话顺序整片变了，
// 而那不是任何一次用户动作引起的。
//
// 空串 = "没有模型引用"（`sessions.model_id` 是 `NOT NULL DEFAULT ”`，所以"置空"就是空串，
// 不是 NULL——列本身不允许 NULL）。
func (s *Store) SetSessionModelID(ctx context.Context, id, modelID string) error {
	return s.sessions.UpdateMap(ctx, id, map[string]any{"model_id": modelID})
}

// touchLastSeq 抬高会话水位（幂等：仅当传入 seq 更大时更新，避免回退已推进的水位）。
//
// v21 起传入的是**会话内位次**（0 起、稠密）——单调判据逐字不变（同一会话内位次仍然只增不减），
// 只是比较的双方从"全库号"换成了"本会话号"。
//
// 保留手写 UPDATE（而不是 `UpdateMap`）：这里的"仅当更大"是 `id` 之外的第二个谓词。
func (s *Store) touchLastSeq(ctx context.Context, d *dao.SessionDao, id string, seq int64) error {
	return d.DB(ctx).Model(&model.SessionEntity{}).
		Where("id = ? AND last_seq < ?", id, seq).
		Updates(map[string]any{"last_seq": seq, "updated_at": model.NowTS()}).Error
}

// touchLastSeqTx 是事务内版本（事件落库与水位推进必须原子）。
func (s *Store) touchLastSeqTx(ctx context.Context, tx *gorm.DB, id string, seq int64) error {
	return s.touchLastSeq(ctx, s.sessions.WithTx(tx), id, seq)
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

// ——— 行 ↔ 聚合映射（C1：领域自持 sessionrow.Item）———

func sessionItemOf(r model.SessionEntity) sessionrow.Item {
	return sessionrow.Item{
		ID: r.ID, Cwd: r.Cwd, WorkspaceID: r.WorkspaceID, WorkspaceTemporary: r.WorkspaceTemporary,
		ModelID: r.ModelID, RuntimeID: r.RuntimeID, RuntimeSessionID: r.RuntimeSessionID,
		Title: r.Title, Pinned: r.Pinned, Archived: r.Archived, Status: r.Status, Origin: r.Origin,
		CapabilityJSON: r.CapabilityJSON, CapabilityFingerprint: r.CapabilityFingerprint,
		NativeMode: r.NativeMode, LastSeq: r.LastSeq, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func sessionItemsOf(rows []model.SessionEntity) []sessionrow.Item {
	out := make([]sessionrow.Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, sessionItemOf(r))
	}
	return out
}

func sessionEntityOf(s sessionrow.Item) model.SessionEntity {
	return model.SessionEntity{
		ID: s.ID, Cwd: s.Cwd, WorkspaceID: s.WorkspaceID, WorkspaceTemporary: s.WorkspaceTemporary,
		ModelID: s.ModelID, RuntimeID: s.RuntimeID, RuntimeSessionID: s.RuntimeSessionID,
		Title: s.Title, Pinned: s.Pinned, Archived: s.Archived, Status: s.Status, Origin: s.Origin,
		CapabilityJSON: s.CapabilityJSON, CapabilityFingerprint: s.CapabilityFingerprint,
		NativeMode: s.NativeMode, LastSeq: s.LastSeq, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}
