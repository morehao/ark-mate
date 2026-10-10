package dao

import (
	"context"
	"errors"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DefinitionDao 是"定义行 + revision CAS"的**共用**实现（M6-1 的 skills/mcp_servers，
// M6-6 的 agents；M6-7 的 automations 也复用它）。
//
// 为什么用泛型而不是每张表各写一遍的 DAO：这些表的**定义列**语义逐字相同
// （计划 §1 P-2 把它们定义为同一形态），而 CAS 的**判据**只有一份——`rowsAffected == 0`
// 就是 `definition_changed`(40901) 的唯一来源。各写一遍，迟早出现"一边改了条件、
// 另一边没改"的静默分叉：那种分叉不会让任何测试变红，只会让并发写在一张表上被挡住、
// 在另一张表上被放过。
//
// 对"定义之外还有运行态列"的表（automations）也成立：泛型方法只碰 body/revision/updated_at
// 三列，`Create` 会插入整行（含运行态列的默认值），`UpdateCAS` **不会**碰运行态列——
// 这正是"写面只 merge 定义、运行态由调度推进"想要的结构性隔离。
//
// 主键是 `id`，但本 DAO **只用自定义方法**，不用 gormdao 的 UpdateByID/UpdateMap/Delete
// ——那些方法走"读实体再整行写回"的语义，会绕过 CAS。
type DefinitionDao[T gormdao.Entity, L ~[]T] struct {
	*gormdao.Dao[T, L, string]
}

// newDefinitionDao 构造共用 DAO（getDB 必填，见包注释）。
func newDefinitionDao[T gormdao.Entity, L ~[]T](daoName string, getDB gormdao.DBGetter) *DefinitionDao[T, L] {
	return &DefinitionDao[T, L]{Dao: newDao[T, L, string](daoName, getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *DefinitionDao[T, L]) WithTx(tx *gorm.DB) *DefinitionDao[T, L] {
	return &DefinitionDao[T, L]{Dao: d.Dao.WithTx(tx)}
}

// Get 读一条定义；不存在 → (nil, false, nil)。
func (d *DefinitionDao[T, L]) Get(ctx context.Context, id string) (*T, bool, error) {
	var row T
	err := d.DB(ctx).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &row, true, nil
}

// List 列出全部定义（id 升序，保证调用方看到的顺序确定）。
func (d *DefinitionDao[T, L]) List(ctx context.Context) (L, error) {
	var rows L
	err := d.DB(ctx).Order("id ASC").Find(&rows).Error
	return rows, err
}

// Count 返回定义行数。
func (d *DefinitionDao[T, L]) Count(ctx context.Context) (int64, error) {
	var n int64
	err := d.DB(ctx).Model(new(T)).Count(&n).Error
	return n, err
}

// CreateCAS 新建一条定义——`expected` 位置留给"仅当不存在"这层 CAS：
// 主键冲突时**不报错**而是返回 false（调用方据此回 40901，与"别人先建了"是同一个对外表现）。
//
// revision 一律从 1 起，不接受调用方传入：版本号是写面的内部记账，不是能力定义的字段。
func (d *DefinitionDao[T, L]) CreateCAS(ctx context.Context, row *T) (bool, error) {
	res := d.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateCAS 覆盖一条定义的 body，**仅当 revision 仍等于 expectedRev**。
//
// 返回 false = 有人在这中间改过（或行已不存在）：调用方把它翻成 `definition_changed`(40901)。
// 这里刻意不做"先读后写"——条件更新是并发下的唯一仲裁者，没有旁路。
func (d *DefinitionDao[T, L]) UpdateCAS(ctx context.Context, id, body string, expectedRev int64, now time.Time) (bool, error) {
	res := d.DB(ctx).Model(new(T)).
		Where("id = ? AND revision = ?", id, expectedRev).
		Updates(map[string]any{
			"body":       body,
			"revision":   expectedRev + 1,
			"updated_at": model.FormatTimeOrNow(now),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteCAS 删除一条定义，**仅当 revision 仍等于 expectedRev**；返回是否删掉。
func (d *DefinitionDao[T, L]) DeleteCAS(ctx context.Context, id string, expectedRev int64) (bool, error) {
	res := d.DB(ctx).Where("id = ? AND revision = ?", id, expectedRev).Delete(new(T))
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// 具体 DAO：写法面只认它们（泛型参数把实体钉死在这里，调用方不必再写类型参数）。

// SkillDao 访问 skills 表。
type SkillDao struct {
	*DefinitionDao[model.SkillDefinition, model.SkillDefinitionList]
}

// NewSkillDao 构造 SkillDao。
func NewSkillDao(getDB gormdao.DBGetter) *SkillDao {
	return &SkillDao{DefinitionDao: newDefinitionDao[model.SkillDefinition, model.SkillDefinitionList]("SkillDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *SkillDao) WithTx(tx *gorm.DB) *SkillDao {
	return &SkillDao{DefinitionDao: d.DefinitionDao.WithTx(tx)}
}

// McpServerDao 访问 mcp_servers 表。
type McpServerDao struct {
	*DefinitionDao[model.McpServerDefinition, model.McpServerDefinitionList]
}

// NewMcpServerDao 构造 McpServerDao。
func NewMcpServerDao(getDB gormdao.DBGetter) *McpServerDao {
	return &McpServerDao{DefinitionDao: newDefinitionDao[model.McpServerDefinition, model.McpServerDefinitionList]("McpServerDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *McpServerDao) WithTx(tx *gorm.DB) *McpServerDao {
	return &McpServerDao{DefinitionDao: d.DefinitionDao.WithTx(tx)}
}

// AutomationDao 访问 automations 表。
//
// 它比 skills/mcp_servers/agents 多两类方法（**运行态列**的读写）——但定义列的 CAS 仍然只有
// 这一份实现：`UpdateCAS` 只碰 body/revision/updated_at，运行态列由本文件末尾的方法单独更新。
type AutomationDao struct {
	*DefinitionDao[model.AutomationDefinition, model.AutomationDefinitionList]
}

// NewAutomationDao 构造 AutomationDao。
func NewAutomationDao(getDB gormdao.DBGetter) *AutomationDao {
	return &AutomationDao{DefinitionDao: newDefinitionDao[model.AutomationDefinition, model.AutomationDefinitionList]("AutomationDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *AutomationDao) WithTx(tx *gorm.DB) *AutomationDao {
	return &AutomationDao{DefinitionDao: d.DefinitionDao.WithTx(tx)}
}

// AgentDao 访问 agents 表。
type AgentDao struct {
	*DefinitionDao[model.AgentDefinition, model.AgentDefinitionList]
}

// NewAgentDao 构造 AgentDao。
func NewAgentDao(getDB gormdao.DBGetter) *AgentDao {
	return &AgentDao{DefinitionDao: newDefinitionDao[model.AgentDefinition, model.AgentDefinitionList]("AgentDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *AgentDao) WithTx(tx *gorm.DB) *AgentDao {
	return &AgentDao{DefinitionDao: d.DefinitionDao.WithTx(tx)}
}

// ProviderDao 访问 providers 表（v11；列与语义同 skills/mcp_servers/agents）。
type ProviderDao struct {
	*DefinitionDao[model.ProviderDefinition, model.ProviderDefinitionList]
}

// NewProviderDao 构造 ProviderDao。
func NewProviderDao(getDB gormdao.DBGetter) *ProviderDao {
	return &ProviderDao{DefinitionDao: newDefinitionDao[model.ProviderDefinition, model.ProviderDefinitionList]("ProviderDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *ProviderDao) WithTx(tx *gorm.DB) *ProviderDao {
	return &ProviderDao{DefinitionDao: d.DefinitionDao.WithTx(tx)}
}

// ModelDao 访问 models 表（v11；平台模型清单）。
//
// 它比 providers 多两类方法（**身份列的过滤**与**每 runtime 默认的置位**），
// 但定义行的 CAS 仍然只有 DefinitionDao 那一份实现（UpdateCAS 只碰 body/revision/updated_at，
// 因此覆盖正文不会顺手动到 is_default）。
type ModelDao struct {
	*DefinitionDao[model.ModelDefinition, model.ModelDefinitionList]
}

// NewModelDao 构造 ModelDao。
func NewModelDao(getDB gormdao.DBGetter) *ModelDao {
	return &ModelDao{DefinitionDao: newDefinitionDao[model.ModelDefinition, model.ModelDefinitionList]("ModelDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *ModelDao) WithTx(tx *gorm.DB) *ModelDao {
	return &ModelDao{DefinitionDao: d.DefinitionDao.WithTx(tx)}
}

// ListAll 列出全部模型条目（id 升序）。
//
// v12 起模型**不带 runtime 维度**，所以"列某 runtime 的清单"不再是一个查询轴：
// 同一份清单投给所有已注册底座，`ListAll` 就是同步时的那一次读取。
func (d *ModelDao) ListAll(ctx context.Context) (model.ModelDefinitionList, error) {
	var rows model.ModelDefinitionList
	err := d.DB(ctx).Order("id ASC").Find(&rows).Error
	return rows, err
}

// ListByProvider 列出挂靠在某条 provider 下的清单（删除保护的引用判据；id 升序）。
func (d *ModelDao) ListByProvider(ctx context.Context, providerID string) (model.ModelDefinitionList, error) {
	var rows model.ModelDefinitionList
	err := d.DB(ctx).Where("provider_id = ?", providerID).Order("id ASC").Find(&rows).Error
	return rows, err
}

// GetByProviderModel 按 `(provider_id, model_id)` 取一条（`model_exists` 判定；不存在 → (nil,false,nil)）。
//
// 它对应的就是 v12 建的唯一索引 `idx_models_provider_model`：**判据交由库级唯一性表达**，
// 而不是"先查后写"（那中间有窗口）。
func (d *ModelDao) GetByProviderModel(ctx context.Context, providerID, modelID string) (*model.ModelDefinition, bool, error) {
	var row model.ModelDefinition
	err := d.DB(ctx).Where("provider_id = ? AND model_id = ?", providerID, modelID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &row, true, nil
}
