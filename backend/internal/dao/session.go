package dao

import (
	"context"
	"fmt"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SessionCond 会话查询条件。
type SessionCond struct {
	*gormdao.BaseCond
	// Archived 为 nil 表示不过滤归档状态。
	Archived *bool
}

// BuildCondition 实现 gormdao.Cond。
func (c *SessionCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.Archived != nil {
		db.Where(tableName+".archived = ?", model.FlagValue(*c.Archived))
	}
}

// SessionDao 访问 sessions 表（会话权威元数据）。
type SessionDao struct {
	*gormdao.Dao[model.SessionEntity, model.SessionEntityList, string]
}

// NewSessionDao 构造 SessionDao（getDB 必填，见包注释）。
func NewSessionDao(getDB gormdao.DBGetter) *SessionDao {
	return &SessionDao{Dao: newDao[model.SessionEntity, model.SessionEntityList, string]("SessionDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *SessionDao) WithTx(tx *gorm.DB) *SessionDao {
	return &SessionDao{Dao: d.Dao.WithTx(tx)}
}

// Upsert 写入（覆盖）一条会话记录。
//
// 两个不能丢的语义：
//   - last_seq 取**触顶**（MAX 旧值, 新值）：并发/重放时不得把已推进的水位写回去；
//   - created_at 也随写入更新（与历史实现一致，导入场景以 transcript header 为准）。
func (d *SessionDao) Upsert(ctx context.Context, c *model.SessionEntity) error {
	return d.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"cwd": c.Cwd,
			// 归属列跟着普通持久化走：会话记录一旦建立就不重新解析归属，所以这里传的与
			// 记录里的值必然相同（与 capability_fingerprint 同一条理由）。
			"workspace_id":        c.WorkspaceID,
			"workspace_temporary": model.FlagValue(c.WorkspaceTemporary),
			"model_id":            c.ModelID,
			"runtime_id":          c.RuntimeID,
			"runtime_session_id":  c.RuntimeSessionID,
			"title":               c.Title,
			"pinned":              model.FlagValue(c.Pinned),
			"archived":            model.FlagValue(c.Archived),
			"status":              c.Status,
			"origin":              c.Origin,
			"capability_json":     c.CapabilityJSON,
			// **钉住的指纹**按传入值覆盖：迁移（M3-6）要把它改写成新代际，普通持久化传的是
			// 记录里的同一个值（会话记录一旦建立就不再重新解析），故这里不会产生"悄悄漂移"。
			"capability_fingerprint": c.CapabilityFingerprint,
			"last_seq":               gorm.Expr("MAX("+model.TableNameSession+".last_seq, ?)", c.LastSeq),
			"created_at":             model.FormatTimeOrNow(c.CreatedAt),
			"updated_at":             model.FormatTimeOrNow(c.UpdatedAt),
		}),
	}).Create(c).Error
}

// UpsertForImport 是 transcript 导入专用的窄 upsert。
//
// 与 Upsert 的差异是**有意的**：冲突时只覆盖导入来源确实携带的列，
// 不碰 provider_id / model_id / runtime_id / runtime_session_id ——
// 这四个是会话级模型选择与底座恢复承载，transcript header 里没有，
// 若按 Upsert 的全列覆盖语义写入零值，重复导入会把用户已选的模型清空。
func (d *SessionDao) UpsertForImport(ctx context.Context, c *model.SessionEntity) error {
	err := d.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"cwd":                 c.Cwd,
			"workspace_temporary": model.FlagValue(c.WorkspaceTemporary),
			"title":               c.Title,
			"pinned":              model.FlagValue(c.Pinned),
			"archived":            model.FlagValue(c.Archived),
			"status":              c.Status,
			"last_seq":            gorm.Expr("MAX("+model.TableNameSession+".last_seq, ?)", c.LastSeq),
			"created_at":          model.FormatTimeOrNow(c.CreatedAt),
			"updated_at":          model.FormatTimeOrNow(c.UpdatedAt),
		}),
	}).Create(c).Error
	if err != nil {
		return fmt.Errorf("store: upsert session %q: %w", c.ID, err)
	}
	return nil
}

// List 列出会话（updated_at 降序；archived 为 nil 表示不过滤）。
func (d *SessionDao) List(ctx context.Context, archived *bool) (model.SessionEntityList, error) {
	return d.GetListByCond(ctx, &SessionCond{
		BaseCond: &gormdao.BaseCond{OrderField: "updated_at DESC"},
		Archived: archived,
	})
}

// UpdateMeta 更新会话元数据子集（nil 指针 = 不更新；updated_at 恒刷新）。
//
// 空补丁直接返回：不产生 `UPDATE ... SET updated_at = ...` 这种只碰时间戳的写。
func (d *SessionDao) UpdateMeta(ctx context.Context, id string, patch model.SessionMetaPatch) error {
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
	return d.DB(ctx).Model(&model.SessionEntity{}).Where("id = ?", id).Updates(set).Error
}

// SetModelID **只**写 `model_id` 一列（v26 的存量回填专用：不碰 updated_at）。
//
// 为什么不复用 `UpdateMeta`：那个函数**恒刷新 `updated_at`**（对"用户改元数据"是对的），
// 而回填是一次**数据迁移**——它不该把用户的会话列表按「最近更新」重排一遍（列表是按
// `updated_at DESC` 排的）。一次性地改写几百行的 updated_at = 用户看到自己的会话顺序整片变了，
// 而那不是任何一次用户动作引起的。
//
// 空串 = "没有模型引用"（`sessions.model_id` 是 `NOT NULL DEFAULT ”`，所以"置空"就是空串，
// 不是 NULL——列本身不允许 NULL）。
func (d *SessionDao) SetModelID(ctx context.Context, id, modelID string) error {
	return d.DB(ctx).Model(&model.SessionEntity{}).Where("id = ?", id).
		Update("model_id", modelID).Error
}

// TouchLastSeq 抬高会话水位（幂等：仅当传入 seq 更大时更新，避免回退已推进的水位）。
//
// v21 起传入的是**会话内位次**（0 起、稠密）——单调判据逐字不变（同一会话内位次仍然只增不减），
// 只是比较的双方从"全库号"换成了"本会话号"。
func (d *SessionDao) TouchLastSeq(ctx context.Context, id string, seq int64) error {
	return d.DB(ctx).Model(&model.SessionEntity{}).
		Where("id = ? AND last_seq < ?", id, seq).
		Updates(map[string]any{"last_seq": seq, "updated_at": model.NowTS()}).Error
}

// SetLastSeq 无条件设置会话水位（导入路径专用：本会话事件全部插入后一次性对齐）。
// 语义同 TouchLastSeq：传入的是**会话内位次**（v21 起）。
func (d *SessionDao) SetLastSeq(ctx context.Context, id string, seq int64) error {
	return d.DB(ctx).Model(&model.SessionEntity{}).
		Where("id = ?", id).
		Updates(map[string]any{"last_seq": seq, "updated_at": model.NowTS()}).Error
}
