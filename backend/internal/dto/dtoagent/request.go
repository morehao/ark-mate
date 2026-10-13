package dtoagent

import "encoding/json"

// 关于 `Mcps` 的三态（创建与 PATCH 都遵此，与 `agentDefinitionFile.Mcps` 的指针语义一一对应）：
//
//	键缺失（未出现）→ 不改（PATCH）/ 未声明（创建）
//	null            → 清空白名单 = 回落「未声明」（全量按 runtime transport 过滤）
//	[]              → 白名单为空 = **本 agent 禁用 MCP 注入**
//	["a","b"]       → 白名单
//
// 「未声明」与「空白名单」必须是两个值：前者是"默认全给"，后者是"一个都别给"，
// 混起来会让"我想关掉 MCP"变成"没改"。

// AgentCreateReq 注册自定义 agent（对**当前底座**的命名配置，不写代码即新增）。
type AgentCreateReq struct {
	ID       string `json:"id" binding:"required"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	// Runtimes / RoutingPolicy **只用于显式拒绝**（40001）。
	//
	// 平台不再按 agent 选底座（构建期固定，见 pkg/runtime/build.go），而 JSON 绑定默认**忽略未知键**
	// ——直接删掉这两个字段会让 `{"runtimes":["<某个底座>"]}` 被收下然后什么都不发生。那正是本仓
	// 明令要防的骗人字段（`default_model` 就是这个先例）。因此保留同名 raw 字段，出现即报错 + 指路。
	Runtimes      json.RawMessage `json:"runtimes"`
	RoutingPolicy json.RawMessage `json:"routing_policy"`
	// Mcps MCP server id 白名单（nil = 未声明 → 全量；`[]` = 禁用全部）。非法 id 不在此拒绝
	// （与定义文件/导入一致：写了不存在的 id 在注入时 WARN 跳过），校验只判类型。
	Mcps             *[]string             `json:"mcps,omitempty"`
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy,omitempty"`
	// DefaultModel **刻意保留一个字段只为显式拒绝**（E8）：它是 `models.is_default` 的派生投影，
	// 没有写入方。不能靠"静默忽略"——agent 的 body 会原样保留未知键，静默收下会让它变成一个
	// 「写了、存了、什么都不发生」的字段（业务风险 B1 要防的正是这种骗人）。带了就 40001 + 指路。
	DefaultModel json.RawMessage `json:"default_model"`
}

// AgentUpdateReq PATCH agent 元数据（指针字段：nil 不更新；`null` = 清除该字段）。
type AgentUpdateReq struct {
	Name     *string `json:"name"`
	Provider *string `json:"provider"`
	// Runtimes / RoutingPolicy 同 AgentCreateReq：**只用于显式拒绝**（40001）。
	Runtimes         json.RawMessage       `json:"runtimes"`
	RoutingPolicy    json.RawMessage       `json:"routing_policy"`
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy"`
	// Mcps 原始 JSON 而不是 `*[]string`：只有原始形态能区分「键缺失」（不改）与 `null`
	// （清空白名单 = 回落未声明）。typed 指针会把两者都变成 nil，"恢复全量"就再也表达不出来。
	// 取值域见文件头三态说明；解析在 manager 写面（`decodeAgentMcpsPatch`）。
	Mcps json.RawMessage `json:"mcps"`
	// DefaultModel 同 AgentCreateReq：**只用于显式拒绝**（40001 + 指路「去设置 · 模型 改」）。
	DefaultModel json.RawMessage `json:"default_model"`
}
