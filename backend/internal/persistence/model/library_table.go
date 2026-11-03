package model

import "time"

// TableNameLibraryTable table 节点的 schema + 行（v33）。
//
// 首版用**单列 JSON**（`schema_json` + `rows_json`）落一张表：先把「存得进、读得出」打通；
// 逐字段 / 逐行的结构化 CRUD 在用例层对 JSON 解析后做（P3），不为此拆多表。
const (
	// TableNameLibraryTable table 节点的 schema + 行（v33）。
	TableNameLibraryTable = "library_tables"
)

// LibraryTableEntity 是 library_tables 表的一行（与 library_nodes 里 kind=table 的节点 1:1）。
//
// 迁移由 v33 步建表（store/migrate_v33.go），本实体与其逐列一致。
type LibraryTableEntity struct {
	// NodeID → library_nodes.id（主键，1:1）
	NodeID string `gorm:"column:node_id;primaryKey"`
	// SchemaJSON 字段定义 JSON（原样保存，未知键保留）。
	SchemaJSON string `gorm:"column:schema_json;not null"`
	// RowsJSON 行数据 JSON 数组（原样保存）。
	RowsJSON string `gorm:"column:rows_json;not null"`
	// UpdatedAt RFC3339Nano(UTC) 文本。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (LibraryTableEntity) TableName() string { return TableNameLibraryTable }

// LibraryTableEntityList 是 LibraryTableEntity 切片类型。
type LibraryTableEntityList []LibraryTableEntity
