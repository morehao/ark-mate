package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameModel 平台模型清单（v11）：一条模型条目 = (runtime, provider_id, model_id)。
	//
	// 它是**清单的唯一来源**，物化成各 runtime 的配置文件（T5 `config_write`）；
	// 运行时实际广告的目录是「事实」，不落这张表（设计 D-6 / ADR-10）。
	TableNameModel = "models"
)

// ModelEntity 是平台声明的一条模型条目（v12 重建、v13 列化；**不带底座维度**）。
//
// 一条模型条目说清「挂在哪条供应商下、发给上游的是哪个模型 id」——它**不再**说"给哪个底座用"：
// 一份清单同时投给所有已注册底座（ADR-1、B3）。v11 的身份三元组 `(runtime, provider_id, model_id)`
// 因此塌成两列：`(provider_id, model_id)` **唯一**（库级唯一索引真表达得了，见 v12 迁移）。
//
// v13 把仅剩的两个平台字段（`name` / `note`）与新增的 `input_modalities` 提成列，
// `body` 随之消失——理由与 provider 相同：键集合封闭、每次列表渲染都要用（ADR-1）。
// 于是「一行坏 JSON 打不开整页」在结构上不可能（没有 JSON 可比坏，v12 的 P2 收尾）。
//
// 默认模型**不在这里**：它是**用户的偏好**、是单一值，住 `meta.MetaKeyDefaultModel`（ADR-21）。
type ModelEntity struct {
	ID string `gorm:"column:id;primaryKey"`
	// ProviderID 挂靠的供应商 id（指向 providers.id）。引用完整性由 store 的**同一事务**保证
	// （不依赖 FK：本仓 SQLite 的 `foreign_keys=0`，所有 REFERENCES 都是装饰，ADR-4）。
	ProviderID string `gorm:"column:provider_id;not null"`
	// ModelID 发给上游的模型 id（**不是**平台模型 id，那是 ID 列）。
	ModelID string `gorm:"column:model_id;not null"`
	// Name 展示名（空 = 界面回落 ModelID）。
	Name string `gorm:"column:name;not null;default:''"`
	// Note 备注（不驱动行为）。
	Note string `gorm:"column:note;not null;default:''"`
	// InputModalities 输入模态（两态：`text` | `text,image`；**默认 `text`**）。
	//
	// 它是平台生成的 route 声明里 `models[].input` 的来源，由**逗号分隔**存成一列
	// （两态取值空间很小，第二张表/JSON 列都不划算）。**总是带值**：`text` 是明确值而不是
	// "没设"，所以 dsh adapter 总是写 `input`（ADR-12 的代价段：会显式覆盖底座 catalog）。
	InputModalities string `gorm:"column:input_modalities;not null;default:'text'"`
	// Revision 写面 CAS 判据（同 SkillEntity.Revision）。
	Revision int64 `gorm:"column:revision;not null"`
	// CreatedAt 创建时刻（RFC3339Nano(UTC)；存量行是 `updated_at` 近似值，见 ProviderEntity.CreatedAt）。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano(UTC)）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (ModelEntity) TableName() string { return TableNameModel }

// ModelEntityList 是 ModelEntity 切片类型（gormdao 泛型参数要求具名切片）。
type ModelEntityList []ModelEntity
