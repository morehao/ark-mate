package model

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameMeta 计数 / 导入标记 / 任意键值。
	TableNameMeta = "meta"
)

// 关于 autoCreateTime/autoUpdateTime：gorm 按字段名约定会把 CreatedAt/UpdatedAt
// 自动当成「插入/更新时取当前时间」，在 Create/Updates 时隐式覆盖调用方传入的值。
// 本仓的时间全部由调用方显式赋值（含「零值落空串」语义），故一律显式关闭。
//
// MetaEntity 是 meta 表的一行（任意键值：导入标记、目录级偏好、读模型重建标记等）。
type MetaEntity struct {
	Key   string `gorm:"column:key;primaryKey"`
	Value string `gorm:"column:value;not null"`
}

// TableName 实现 gormdao.Entity。
func (MetaEntity) TableName() string { return TableNameMeta }

// MetaEntityList 是 MetaEntity 切片类型（gormdao 泛型参数要求具名切片）。
type MetaEntityList []MetaEntity
