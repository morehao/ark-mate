package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNamePermissionMemory 会话级权限记忆。
	TableNamePermissionMemory = "permission_memories"
)

// PermissionMemoryEntity 是 permission_memories 表的一行（会话级权限记忆，allow/reject 双侧对称）。
type PermissionMemoryEntity struct {
	ID        string `gorm:"column:id;primaryKey"`
	SessionID string `gorm:"column:session_id;not null"`
	Title     string `gorm:"column:title;not null"`
	Kind      string `gorm:"column:kind;not null"`
	// Decision 决策方向。取值见 DecisionAllow / DecisionReject。
	Decision  string    `gorm:"column:decision;not null"`
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (PermissionMemoryEntity) TableName() string { return TableNamePermissionMemory }

// PermissionMemoryEntityList 是 PermissionMemoryEntity 切片类型（gormdao 泛型参数要求具名切片）。
type PermissionMemoryEntityList []PermissionMemoryEntity
