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
// 一条模型条目说清「挂在哪条供应商下、发给上游的是哪个模型名」——它**不再**说"给哪个底座用"：
// 一份清单同时投给所有已注册底座（ADR-1、B3）。v11 的身份三元组 `(runtime, provider_id, model)`
// 因此塌成两列：`(provider_id, model)` **唯一**（库级唯一索引真表达得了，见 v12 迁移）。
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
	// Model 发给上游的模型名（**不是**平台模型 id，那是 ID 列）。
	//
	// v24 从 `model_id` 改名为 `model`（定案见 `.dsh/docs/specs/2026-10-15-provider-model-identity-design.md`
	// §3 辛）：`model_id` 在本仓一词三义（这一列 / `models.id` / `sessions.model_id`），而 `model`
	// 是生态标准词（OpenAI 请求体 `{"model": …}`、Continue `models[].model`、QwenWork
	// `byok_custom_models.model` 自注"上游真实模型名"）。改名之后 `_id` 一律指"指向某表 `id`"：
	// `provider_id` → `providers.id`、`sessions.model_id` → `models.id`。
	Model string `gorm:"column:model;not null"`
	// Name 展示名（空 = 界面回落 Model）。
	Name string `gorm:"column:name;not null;default:''"`
	// Note 备注（不驱动行为）。
	Note string `gorm:"column:note;not null;default:''"`
	// InputModalities 输入模态（两态：`text` | `text,image`；**默认 `text`**）。
	//
	// 它是平台生成的 route 声明里 `models[].input` 的来源，由**逗号分隔**存成一列
	// （两态取值空间很小，第二张表/JSON 列都不划算）。**总是带值**：`text` 是明确值而不是
	// "没设"，所以 dsh adapter 总是写 `input`（ADR-12 的代价段：会显式覆盖底座 catalog）。
	InputModalities string `gorm:"column:input_modalities;not null;default:'text'"`
	// MaxInputTokens 上下文输入上限（**平台声明**；nil = 未声明）。
	//
	// v25 加（路由键隔离的连带决定）：隔离后每条路由都是平台手写的声明，底座内置目录再也命中不了
	// ——容量不再是"恰好命中目录时侥幸拿到的真值"，只能由平台说。**可空、无 DEFAULT**：0 当不了
	// "未声明"的哨兵（底座 schema 是 `min(1)`，写 0 会被拒），而 `NOT NULL DEFAULT 0` 会逼着
	// 每一行携带一个假值。nil = adapter **不下发**该字段。
	//
	// 列名刻意**不抄底座的词**（那边叫 `contextWindow`/`maxTokens`）：底座词表不进内核（A9 的精神），
	// 映射只住在 adapter 里。
	MaxInputTokens *int64 `gorm:"column:max_input_tokens"`
	// MaxOutputTokens 单次输出上限（平台声明；nil = 未声明）。语义同 MaxInputTokens。
	MaxOutputTokens *int64 `gorm:"column:max_output_tokens"`
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
