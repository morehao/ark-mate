package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameProvider provider 档案（供应商凭据）：一条上游凭据的登记（v11）。
	//
	// 它是 `providers.json` 的 DB 载体（设计 §「新表 providers」）：sqlite 模式下档案的事实源，
	// memory 模式下文件仍是事实源，写面按持久层模式分派（`providers_carrier.go`）。
	TableNameProvider = "providers"
)

// ProviderEntity 是一条 provider 档案行（**资源表**形态，v13 列化）。
//
// 它**不再是定义载体**：`skills` / `mcp_servers` / `automations` 的 body 是
// 「用户自由正文」，而 provider 的键集合 `{name, protocol, base_url, note}` 是平台拥有的
// **封闭字段集**且每个键都在热路径上（`protocol` 决定适配、`base_url` 决定请求落点）——
// 封闭 ⇒ 列化（ADR-1/ADR-2）。判据一句话：body 只留给「正文是用户自由文档」的资源。
//
// **刻意没有的两列**（每条都是判据，不是遗漏）：
//   - `body`：键集合封闭，它的缺席由 archtest 守卫钉住；
//   - `key_ref`：它是 `providerRef(id)` 的纯函数副本，落库只会制造第二个事实源——
//     两份值不一致时的表现是「凭据坏了」，会误导排查（ADR-7）。**v20 起它只剩派生量**：
//     档案载体/导出物里仍可能出现（它是 0600 镜像文件的键），但"有没有密钥"的权威判据
//     是本表的 `key` 列（非空即有密钥），不再有别的答案。
//
// **密钥在 v20 起是明文列**（ADR-8 在 v20 被推翻，理由见 `internal/adapters/outbound/sqlite/store/credentials.go`
// 与「决策变更 v20」）：库里、备份里、诊断面里都会有明文，换来的是一条权威与一份可重建镜像。
type ProviderEntity struct {
	// ID 主键（词法受限：它会进凭据引用段 `<ns>/<name>`）。
	ID string `gorm:"column:id;primaryKey"`
	// Name 展示名（空 = 前端回落 id）。
	Name string `gorm:"column:name;not null;default:''"`
	// Protocol 协议（本期只有 openai；库级 CHECK 兜住裸 SQL 路径）。
	Protocol string `gorm:"column:protocol;not null"`
	// BaseURL 上游端点（空 = 缺端点，界面与 doctor 显式标出，且该供应商不进任何 Set）。
	BaseURL string `gorm:"column:base_url;not null;default:''"`
	// Note 备注（不驱动行为）。
	Note string `gorm:"column:note;not null;default:''"`
	// Key 供应商密钥的**明文**（v20 起；空串 = 没有密钥）。
	//
	// 它是 v20 的核心取舍：vault 唯一的真实收益是"密钥不进库 / 不进备份 / 不进诊断面"，
	// 而代价是一整套加密存储、两阶段写与二象性（库里一份引用、盘上一份密文）。v20 换成
	// "库是唯一权威 + 一份 0600 的可重建镜像"，于是**备份与诊断面里会有明文**——这是明确
	// 写进 README 与 docs/deployment.md 的代价。与 `body` 的取舍同源：键集合封闭 ⇒ 列。
	Key string `gorm:"column:key;not null;default:''"`
	// Revision 写面 CAS 判据（同 SkillEntity.Revision）。
	Revision int64 `gorm:"column:revision;not null"`
	// CreatedAt 创建时刻（RFC3339Nano(UTC)）。
	//
	// **存量行是近似值**：v11/v12 都没建这一列，v13 迁移只能取 `updated_at` 并登记
	// （`migration_notes_v13.created_at_approximated`）——如实登记，不假装它是创建时间。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (ProviderEntity) TableName() string { return TableNameProvider }

// ProviderEntityList 是 ProviderEntity 切片类型（gormdao 泛型参数要求具名切片）。
type ProviderEntityList []ProviderEntity
