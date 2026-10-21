package dtomodel

// ConfigOptionValueResp 一个可选值（运行时配置项候选项，如模型）。
//
// Value 是**不透明字符串**：必须原样回传给创建/PATCH 接口，前端不得解析其内部结构
// （形态由各 adapter 自己声明，见 `internal/runtime/model_value.go`：dsh 是
// `JSON.stringify([provider, model])`，别的底座可能是 `provider/model`）。
type ConfigOptionValueResp struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Group/GroupName 分组（**该底座**以分组形态下发时才有；它的广告是扁平列表时两字段为空）。
	Group     string `json:"group,omitempty"`
	GroupName string `json:"group_name,omitempty"`
	// Unavailable 平台观测：该值最近被运行时拒绝过（如凭证缺失）。仅作提示，最终以运行时为准。
	Unavailable       bool   `json:"unavailable,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	// ModelID 这个候选对应**平台清单里的哪一行**（`mdl_…`；空 = 平台没有声明过它）——v26 加。
	//
	// 会话的 `model`（创建/切换）收的是**平台模型行 id**，而选择发生在**运行时目录**上，所以
	// 注解必须跟着候选走：前端一次取数就能"选运行时值、提交平台 id"，不必同时持有两份清单再自己 join。
	//
	// 怎么算出来的：对每条平台行算**派生值**（`modelValueFor`）再与候选逐字比对——**比对派生值 ≠
	// 解析不透明值**，所以这不违反"平台不解析底座词表"。多命中按 fail-closed（不注解 = 客户端选不了它）。
	//
	// **刻意不复用 `Unavailable`**：那个字段的含义是"**底座拒过**这个值"（动态学到的事实），
	// "平台没声明过它"是另一件静态的事；一个字段塞两种含义会让界面上那句「不可用」变成假话。
	ModelID string `json:"model_id,omitempty"`
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
//   - session：直接读活跃会话；
//   - resume：恢复最近一条历史会话读取（读完即 close，不新建会话）；
//   - cache：内存缓存（TTL 内）；
//   - none：运行时没有可用来源（Options 为空，带 Note/鉴权引导）。
type ModelCatalogResp struct {
	Runtime string `json:"runtime"`
	Source  string `json:"source"`
	// ImageInput **当前这条路由能不能收图**——连接期事实（ACP initialize 的
	// `promptCapabilities.image`），nil = 取不到（还没有连接）。
	//
	// 为什么必须是连接期事实（ADR-14）：静态能力基线（`dsh.Caps.Images=false`）是"按源码声明的
	// 保守值"，真值取决于附件仓与**精确路由**；拿静态值当判据，用户会在一个其实能收图的底座上
	// 被告知"不支持"。
	//
	// 注意它**不含模型信息**：它由**该底座自己的路由声明**折算（dsh 是「声明里含 image 才 true」，
	// 而有的底座恒 true——ACP 广告与模型无关、模型级闸门在底座内部）。所以它是"这条路由的收图
	// 能力"的上界，逐条模型的事实仍是平台清单里的 `input_modalities`。
	ImageInput *bool `json:"image_input,omitempty"`
	// FetchedAt 取样时间（RFC3339；source=cache 时为首次取样时间）。
	FetchedAt string `json:"fetched_at,omitempty"`
	// Options 运行时配置项全量投影（模型目录为主；运行时若还给出别的选项，也一并原样透出，仅作展示）。
	Options []ConfigOptionResp `json:"options"`
	// AuthGuidance 运行时声明的鉴权方式（例如底座给出的终端登录命令）。
	AuthGuidance []AuthGuidanceResp `json:"auth_guidance,omitempty"`
	// Note 空目录/降级说明（人类可读，可直接展示给用户）。
	Note string `json:"note,omitempty"`
}
