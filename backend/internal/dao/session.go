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
			"agent_id": c.AgentID,
			"cwd":      c.Cwd,
			// 归属列跟着普通持久化走：会话记录一旦建立就不重新解析归属，所以这里传的与
			// 记录里的值必然相同（与 capability_fingerprint 同一条理由）。
			"workspace_id":        c.WorkspaceID,
			"workspace_temporary": model.FlagValue(c.WorkspaceTemporary),
			"provider_id":         c.ProviderID,
			"model_id":            c.ModelID,
			"runtime_hint":        c.RuntimeHint,
			"provider_session_id": c.ProviderSessionID,
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
// 不碰 provider_id / model_id / runtime_hint / provider_session_id ——
// 这四个是会话级模型选择与底座恢复承载，transcript header 里没有，
// 若按 Upsert 的全列覆盖语义写入零值，重复导入会把用户已选的模型清空。
func (d *SessionDao) UpsertForImport(ctx context.Context, c *model.SessionEntity) error {
	err := d.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"agent_id":            c.AgentID,
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
	if patch.ProviderID != nil {
		set["provider_id"] = *patch.ProviderID
	}
	if patch.ModelID != nil {
		set["model_id"] = *patch.ModelID
	}
	return d.DB(ctx).Model(&model.SessionEntity{}).Where("id = ?", id).Updates(set).Error
}

// TouchLastSeq 抬高会话水位（幂等：仅当传入 seq 更大时更新，避免回退已推进的水位）。
func (d *SessionDao) TouchLastSeq(ctx context.Context, id string, seq int64) error {
	return d.DB(ctx).Model(&model.SessionEntity{}).
		Where("id = ? AND last_seq < ?", id, seq).
		Updates(map[string]any{"last_seq": seq, "updated_at": model.NowTS()}).Error
}

// SetLastSeq 无条件设置会话水位（导入路径专用：本会话事件全部插入后一次性对齐）。
func (d *SessionDao) SetLastSeq(ctx context.Context, id string, seq int64) error {
	return d.DB(ctx).Model(&model.SessionEntity{}).
		Where("id = ?", id).
		Updates(map[string]any{"last_seq": seq, "updated_at": model.NowTS()}).Error
}
