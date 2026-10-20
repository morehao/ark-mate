package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameSkill 能力定义（skill）——sqlite 模式下它是能力面的**事实源**（M6-1）。
	TableNameSkill = "skills"
)

// ——— 能力定义事实源（M6-1；表名见 TableNameSkill / TableNameMcpServer） ———

// SkillEntity 是一条 skill 定义行。
//
// `Body` 是**唯一权威**：定义原始 JSON 原样保存，未知键因此得以承载——落盘纪律（C9）的
// **意图保留、机制换掉**：从"读 map → 只改目标键 → 原子替换文件"换成"事务 + 行 revision CAS"
// （计划 §1 P-2/P-3）。
//
// `Revision`/`UpdatedAt` 是仅有的两个**提升列**。刻意**不加** `enabled`/`scope`/`runtimes`
// 等派生列：它们都在 `body` 里，双写必然漂移；能力定义规模是几十条，查询在 Go 侧做即可。
type SkillEntity struct {
	ID string `gorm:"column:id;primaryKey"`
	// Body 定义原始 JSON（含未知键）。
	Body string `gorm:"column:body;not null"`
	// Revision 写面 CAS 判据：从 1 起，每次成功提交 +1。
	//
	// 它就是今天 `checkUnchanged`（mtime+size）所处的位置——对外表现仍是同一个
	// `definition_changed`(40901)，**名字与码都不变**。
	Revision int64 `gorm:"column:revision;not null"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (SkillEntity) TableName() string { return TableNameSkill }

// SkillEntityList 是 SkillEntity 切片类型（gormdao 泛型参数要求具名切片）。
type SkillEntityList []SkillEntity
