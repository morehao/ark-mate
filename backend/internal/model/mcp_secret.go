package model

import "time"

// TableNameMcpSecret MCP 密钥表（v20 起）。
const (
	TableNameMcpSecret = "mcp_secrets"
)

// McpSecretEntity 一行 MCP 密钥：引用串 → 明文值。
//
// 为什么是**独立表**而不是塞进 `mcp_servers.body`（v20 设计 §2.1）：body 会进
// `definition_revisions` 与导入导出载体——密钥写进去就等于"审计表里躺着明文"。
// 独立表让"密钥"这件事只有一处落点（同 v20 的 `providers.key`）。
//
// 键是**引用串本身**（不是 server id + 键名）：同一个引用可以被多个 server 共用，
// "谁引用了它"由定义体决定——这与旧 vault 的引用语义逐字相同，所以迁移是纯搬运。
type McpSecretEntity struct {
	// Ref 完整引用串（`store:<ns>/<name>`）；它就是主键。
	Ref string `gorm:"column:ref;primaryKey"`
	// Value 明文值（空串 = 值被清空，行保留以便 revision 单调）。
	Value string `gorm:"column:value;not null;default:''"`
	// Revision 写面 CAS 判据（同 ProviderEntity.Revision）。
	Revision int64 `gorm:"column:revision;not null"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (McpSecretEntity) TableName() string { return TableNameMcpSecret }

// McpSecretEntityList 是 McpSecretEntity 切片类型（gormdao 泛型参数要求具名切片）。
type McpSecretEntityList []McpSecretEntity
