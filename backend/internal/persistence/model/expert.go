package model

import "time"

// TableNameExpert 专家定义表（v31）。与 skills/mcp_servers 同形：body 是原始 JSON、revision 是
// 写面 CAS 判据、updated_at 是列表排序依据。
const (
	TableNameExpert = "experts"
)

// ExpertEntity 是一条专家定义行。
//
// `Body` 是**唯一权威**：定义原始 JSON 原样保存，未知键因此得以承载——落盘纪律（未知键保留、
// revision CAS、变更审计）与 skills/mcp_servers 完全一致，故复用 `DefinitionDao` 的 CAS 原语。
// `Revision`/`UpdatedAt` 是仅有的两个提升列；`enabled`/`skills`/`mcps` 等都在 body 里。
type ExpertEntity struct {
	ID string `gorm:"column:id;primaryKey"`
	// Body 定义原始 JSON（含未知键）。
	Body string `gorm:"column:body;not null"`
	// Revision 写面 CAS 判据：从 1 起，每次成功提交 +1。
	Revision int64 `gorm:"column:revision;not null"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (ExpertEntity) TableName() string { return TableNameExpert }

// ExpertEntityList 是 ExpertEntity 切片类型（gormdao 泛型参数要求具名切片）。
type ExpertEntityList []ExpertEntity
