package dtomodel

// ConfigOptionValueResp 一个可选值（运行时配置项候选项，如模型）。
//
// Value 是**不透明字符串**：必须原样回传给创建/PATCH 接口，前端不得解析其内部结构
// （dsh 为 JSON.stringify([provider, model])，opencode 为 "provider/model"）。
type ConfigOptionValueResp struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Group/GroupName 分组（agent 以分组形态下发时才有；opencode 为扁平列表时两字段为空）。
	Group     string `json:"group,omitempty"`
	GroupName string `json:"group_name,omitempty"`
	// Unavailable 平台观测：该值最近被运行时拒绝过（如凭证缺失）。仅作提示，最终以运行时为准。
	Unavailable       bool   `json:"unavailable,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// ConfigOptionResp 一个会话配置项（ACP session config option 的直投影）。
// Category 取值：model / thought_level / mode / model_config（协议标准语义；前端按它渲染，
// 不得按 option id 匹配——各家 id 不同）。
type ConfigOptionResp struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description,omitempty"`
	Category     string                  `json:"category,omitempty"`
	Type         string                  `json:"type"`
	CurrentValue string                  `json:"current_value"`
	Grouped      bool                    `json:"grouped,omitempty"`
	Values       []ConfigOptionValueResp `json:"values"`
}

// AuthGuidanceResp 运行时声明的鉴权方式（空目录时给用户一条确切的补救路径）。
type AuthGuidanceResp struct {
	MethodID    string   `json:"method_id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Terminal    bool     `json:"terminal,omitempty"`
	Command     []string `json:"command,omitempty"`
}

// ModelCatalogResp 模型目录响应：**全部来自运行时自报**（ACP session config options），
// 平台不内置任何模型清单。Source 标注取样来源：
//   - session：直接读该 agent 的活跃会话；
//   - resume：恢复该 agent 最近一条历史会话读取（读完即 close，不新建会话）；
//   - cache：内存缓存（TTL 内）；
//   - none：运行时没有可用来源（Options 为空，带 Note/鉴权引导）。
type ModelCatalogResp struct {
	Agent   string `json:"agent"`
	Runtime string `json:"runtime"`
	Source  string `json:"source"`
	// FetchedAt 取样时间（RFC3339；source=cache 时为首次取样时间）。
	FetchedAt string `json:"fetched_at,omitempty"`
	// Options 运行时配置项全量投影（模型目录为主；运行时若还给出别的选项，也一并原样透出，仅作展示）。
	Options []ConfigOptionResp `json:"options"`
	// AuthGuidance 运行时声明的鉴权方式（如 opencode 的终端登录命令）。
	AuthGuidance []AuthGuidanceResp `json:"auth_guidance,omitempty"`
	// Note 空目录/降级说明（人类可读，可直接展示给用户）。
	Note string `json:"note,omitempty"`
}
