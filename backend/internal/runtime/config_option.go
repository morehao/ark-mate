package runtime

import "github.com/morehao/ark-mate/internal/adapters/outbound/acp"

// ——— 会话配置项的平台值类型（C2：内核契约去协议化）———
//
// 这些形状此前直接是 `acp.ConfigOption`：内核的 `Runtime` 接口、模型目录缓存、会话状态机都拿它
// 当语言，于是"换一个非 ACP 底座"要先改内核。现在它们是**平台值**，ACP 侧的对应结构只在两个
// 边界上出现：`configOptionsFromACP`（本文件，协议 → 平台）与 `adapters/outbound/dsh`（写面下发）。
//
// **json tag 与协议逐字相同**：`session.config_options` 事件与 `/models` 目录会把它们直接序列化
// 给前端（既有契约）；tag 分叉等于改 wire。由 archtest 的
// `TestConfigOptionValueTypesMatchACP` 逐字段/逐 tag 钉住。

// 配置项的语义分类（协议 `ConfigOptionCategory`；仅作 UX 提示，**不得**作为正确性依赖）。
const (
	CategoryModel        = "model"
	CategoryThoughtLevel = "thought_level"
	CategoryMode         = "mode"
	CategoryModelConfig  = "model_config"
)

// 选项控件类型（协议 `ConfigOptionType`）。
const (
	OptionTypeSelect  = "select"
	OptionTypeBoolean = "boolean"
)

// ConfigOptionValue 一个可选值。
//
// Value 是**不透明字符串**，必须原样回传（各底座形态不同，如 `JSON.stringify([provider, model])`
// 或 `provider/model`）；展示一律用 Name，**不得解析 Value 结构**。
type ConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Group/GroupName 该值所属分组标识与标题（未分组时为空；分组只影响展示）。
	Group     string `json:"group,omitempty"`
	GroupName string `json:"group_name,omitempty"`
}

// ConfigOption 一个会话配置项的归一化投影（分组已展平进 Values，保留 Group 字段）。
type ConfigOption struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	Category     string              `json:"category,omitempty"`
	Type         string              `json:"type"`
	CurrentValue string              `json:"current_value"`
	Values       []ConfigOptionValue `json:"values"`
	// Grouped 原选项是否以分组形态下发（Group/GroupName 是否可用）。
	Grouped bool `json:"grouped,omitempty"`
}

// FindConfigOption 按 category 取选项（category 为空或未命中 → false）。
//
// 语义化取值的唯一入口：**不按 id 匹配**（各家 id 不同，只有 category 是协议统一的）。
func FindConfigOption(opts []ConfigOption, category string) (ConfigOption, bool) {
	if category == "" {
		return ConfigOption{}, false
	}
	for _, o := range opts {
		if o.Category == category {
			return o, true
		}
	}
	return ConfigOption{}, false
}

// CurrentConfigValue 取某 category 的当前值（未命中 → 空串）。
func CurrentConfigValue(opts []ConfigOption, category string) string {
	if o, ok := FindConfigOption(opts, category); ok {
		return o.CurrentValue
	}
	return ""
}

// HasConfigValue 判断 value 是否是该 category 选项当前广告的可选值之一。
//
// 只用于「目录内校验」；最终判据仍是运行时 set 的成败（缓存可能滞后，预判会造假阴性）。
func HasConfigValue(opts []ConfigOption, category, value string) bool {
	o, ok := FindConfigOption(opts, category)
	if !ok {
		return false
	}
	for _, v := range o.Values {
		if v.Value == value {
			return true
		}
	}
	return false
}

// ConfigOptionsOfACP 把协议选项折成平台值（**适配器边界的唯一转换入口**）。
//
// 公开而不是只在包内用：`drivers/runtimeconn` 取样时手里只有 `*acp.Session`，它必须借这一处
// 转换——转换逻辑抄第二遍就会在两处各漏一个字段。
func ConfigOptionsOfACP(in []acp.ConfigOption) []ConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]ConfigOption, 0, len(in))
	for _, o := range in {
		out = append(out, configOptionOfACP(o))
	}
	return out
}

// configOptionOfACP 单条转换。
func configOptionOfACP(o acp.ConfigOption) ConfigOption {
	out := ConfigOption{
		ID: o.ID, Name: o.Name, Description: o.Description, Category: o.Category,
		Type: o.Type, CurrentValue: o.CurrentValue, Grouped: o.Grouped,
	}
	if len(o.Values) > 0 {
		out.Values = make([]ConfigOptionValue, 0, len(o.Values))
		for _, v := range o.Values {
			out.Values = append(out.Values, ConfigOptionValue{
				Value: v.Value, Name: v.Name, Description: v.Description,
				Group: v.Group, GroupName: v.GroupName,
			})
		}
	}
	return out
}
