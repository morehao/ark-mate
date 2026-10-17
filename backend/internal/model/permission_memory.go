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
	Decision string `gorm:"column:decision;not null"`
	// Scope **授权形态**（v28）。今天只有一个可写值 `session`（= 会话内始终）。
	//
	// "只允许这一次"**不落库**（`Remember` 只被 always 决策调用），所以它没有对应取值——
	// 给它留一个永远不会被写入的枚举值，只会让下一个读它的人以为平台支持那种记忆。
	Scope string `gorm:"column:scope;not null;default:'session'"`
	// GrantInputDigest 当初那次授权**那条工具入参**的摘要（审计用，**不进唯一键**）。
	//
	// 唯一键是 `(session_id,title,kind)`：把入参并进去会把"这类工具始终允许"拆成"这一个参数
	// 组合始终允许"——那是把一条便利功能拆掉。摘要的作用是可复核：出事时能回答
	// "当初那句话，是在哪次具体调用上说的"。
	GrantInputDigest string    `gorm:"column:grant_input_digest;not null;default:''"`
	CreatedAt        time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (PermissionMemoryEntity) TableName() string { return TableNamePermissionMemory }

// PermissionMemoryEntityList 是 PermissionMemoryEntity 切片类型（gormdao 泛型参数要求具名切片）。
type PermissionMemoryEntityList []PermissionMemoryEntity
