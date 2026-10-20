// Package row 是模型供应商域的**行值类型**（叶子包，理由见 `session/queue` 的包注释）。
//
// 它与 `provider.ProviderItem` / `provider.ModelItem` 的分工：`Item` / `Model` 是两张表的
// **完整行形状**（纯值），`ProviderItem` / `ModelItem` 是对外读面投影（带可用性、引用、容量解读
// 等现算字段）。store 适配器只认行值，投影留在域里做。
package row

import "time"

// Item 一条 provider 档案（一张 `providers` 行的完整形状）。
//
// **它不再是定义载体**：键集合 `{name, protocol, base_url, note}` 是平台拥有的封闭字段集，
// 所以是列（v13 列化）。密钥是**独立写路径**的列（`Key`）：内容走 revision CAS + 审计，
// 密钥只 bump revision——两条路刻意不合成一次提交（见 `store/provider.go` 的说明）。
type Item struct {
	ID       string
	Name     string
	Protocol string
	BaseURL  string
	Note     string
	// Key 供应商密钥的明文（v20 起；空串 = 没有密钥）。
	//
	// 域内**不读它**：密钥面经 `Secrets` 端口解析（四层回落），行值里这一列只是形状的一部分，
	// 让适配器的映射不需要第二套类型。
	Key      string
	Revision int64
	// CreatedAt 创建时刻（存量行是 `updated_at` 的近似值，不假装它是创建时间）。
	CreatedAt time.Time
	// UpdatedAt 最后一次成功提交的时刻。
	UpdatedAt time.Time
}

// Model 一条平台模型条目（一张 `models` 行的完整形状）。
//
// **没有 runtime 维度**：一份清单投给所有已注册底座。默认模型也不在行上——它是用户的偏好，
// 住 `meta.default_model`。
type Model struct {
	ID         string
	ProviderID string
	// Model 发给上游的模型名（不是平台模型 id）。
	Model string
	Name  string
	Note  string
	// InputModalities 输入模态（`text` | `text,image`）。
	InputModalities string
	// MaxInputTokens 上下文输入上限（nil = 未声明，adapter 不下发该字段）。
	MaxInputTokens *int64
	// MaxOutputTokens 单次输出上限（nil = 未声明）。
	MaxOutputTokens *int64
	Revision        int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Columns 是 provider 行的**内容列**（身份列 `ID` + 四个平台字段），写面提交用。
//
// 与 `Item` 分开：提交是「按列 CAS」，行里的 Key / revision / 时间不在内容语义里。
type Columns struct {
	ID       string
	Name     string
	Protocol string
	BaseURL  string
	Note     string
}

// ModelColumns 模型条的**全部列**（含身份列）：新建时用。
type ModelColumns struct {
	ID              string
	ProviderID      string
	Model           string
	Name            string
	Note            string
	InputModalities string
	// MaxInputTokens / MaxOutputTokens 平台声明的容量（nil = 未声明）。
	// 指针而不是 int64：nil 与 0 在这条链路上是**两件事**（0 不是合法容量）。
	MaxInputTokens  *int64
	MaxOutputTokens *int64
}

// ModelContent 模型行里**可变的内容列**（身份列 `provider_id`/`model` 不在其中——
// 改身份等于换一条资源，不在 PATCH 语义里）。
//
// 容量在这里是**解析后的最终值**（调用方已把"不改"折成原值、"清除"折成 nil）：本层不做指针语义，
// 它只写库。
type ModelContent struct {
	Name            string
	Note            string
	InputModalities string
	MaxInputTokens  *int64
	MaxOutputTokens *int64
}
