package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameAgent agent 定义（M6-6；与 TableNameSkill 同形）。
	TableNameAgent = "agents"
)

// AgentEntity 是一条 agent 定义行（M6-6；列与语义同 SkillEntity）。
//
// **只存用户对种子的覆盖**：内置种子（`builtinAgents`：dsh/opencode）永不落行，装载时先铺种子
// 再用行里的**非零字段**覆盖（`applyDefinition` 的部分合并语义逐字保留）。所以"DB 里有几行"
// ≠ "平台有几个 agent"——`List` 出来的行里可能只有 `{"id":"x","name":"..."}`。
//
// Body 存的是 **typed marshal 的结果**（与今天 `persistAgentDef` 写文件的内容逐字节同源），
// 不是"原始文件字节"：agent 的写面今天就走 typed 结构（`agentDefinitionFile`），DB 化只换载体
// 不换形状——未知键在 agent 侧从来就没被保留过，这一条不在 M6-6 的语义变更范围里。
type AgentEntity struct {
	ID string `gorm:"column:id;primaryKey"`
	// Body 定义 JSON（typed marshal；见类型注释）。
	Body string `gorm:"column:body;not null"`
	// Revision 写面 CAS 判据（同 SkillEntity.Revision）。
	Revision int64 `gorm:"column:revision;not null"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (AgentEntity) TableName() string { return TableNameAgent }

// AgentEntityList 是 AgentEntity 切片类型（gormdao 泛型参数要求具名切片）。
type AgentEntityList []AgentEntity
