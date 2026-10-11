package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameSchemaMigration schema 版本记录（迁移内核自管）。
	TableNameSchemaMigration = "schema_migrations"
)

// SchemaMigrationEntity 是 schema_migrations 表的一行（迁移内核自管的版本记录）。
type SchemaMigrationEntity struct {
	Version   int       `gorm:"column:version;primaryKey"`
	Name      string    `gorm:"column:name;not null"`
	AppliedAt time.Time `gorm:"column:applied_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (SchemaMigrationEntity) TableName() string { return TableNameSchemaMigration }

// SchemaMigrationEntityList 是 SchemaMigrationEntity 切片类型（gormdao 泛型参数要求具名切片）。
type SchemaMigrationEntityList []SchemaMigrationEntity
