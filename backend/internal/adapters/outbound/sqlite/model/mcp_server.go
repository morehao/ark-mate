package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameMcpServer MCP server 定义（与 TableNameSkill 同形，见 McpServerEntity）。
	TableNameMcpServer = "mcp_servers"
)

// McpServerEntity 是一条 MCP server 定义行（列与语义同 SkillEntity）。
//
// 与 SkillEntity 分表而不是加 kind 列：两张表的读取面、写面与规模都各走各的，
// 合成一张"定义表 + kind"会让每个查询都带一个恒真的过滤条件，且 kind 与 id 的
// 唯一性约束会变成"跨 kind 唯一"这种没人想要的东西。
type McpServerEntity struct {
	ID string `gorm:"column:id;primaryKey"`
	// Body 定义原始 JSON（含未知键）。
	Body string `gorm:"column:body;not null"`
	// Revision 写面 CAS 判据（同 SkillEntity.Revision）。
	Revision int64 `gorm:"column:revision;not null"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (McpServerEntity) TableName() string { return TableNameMcpServer }

// McpServerEntityList 是 McpServerEntity 切片类型（gormdao 泛型参数要求具名切片）。
type McpServerEntityList []McpServerEntity
