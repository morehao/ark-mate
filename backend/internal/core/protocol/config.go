package protocol

// ACP session config option 的语义分类（protocol: ConfigOptionCategory）。
// 仅作 UX 提示，**不得**作为正确性依赖：agent 可以不提供 category，也可能不提供该选项。
// 取值由消费方按 category 匹配（而非硬编码 option id——各家 id 不同）。
const (
	CategoryModel        = "model"
	CategoryThoughtLevel = "thought_level"
	CategoryMode         = "mode"
	CategoryModelConfig  = "model_config"
)

// 选项控件类型（protocol: ConfigOptionType）。
const (
	OptionTypeSelect  = "select"
	OptionTypeBoolean = "boolean"
)

// ConfigOptionValue 一个可选值。Value 是**不透明字符串**，必须原样回传
// （历史实测：dsh 为 JSON.stringify([provider, model])，另一底座（已下线）为
// "provider/model"），展示一律用 Name，不得解析 Value 结构。
type ConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Group/GroupName 该值所属分组标识与标题（agent 未分组时为空）。
	// ACP 允许 options 为扁平列表或分组列表；分组信息只影响展示，不影响取值。
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
// 语义化取值的唯一入口：不按 id 匹配。
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
// 用于「目录内校验」；最终判据仍是运行时 set 的成败（协议保证 value 必须取自该列表）。
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
