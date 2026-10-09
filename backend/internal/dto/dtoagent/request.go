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

// AgentCreateReq 注册自定义 agent（对既有 runtime 的命名配置，不写代码即新增）。
type AgentCreateReq struct {
	ID       string   `json:"id" binding:"required"`
	Name     string   `json:"name"`
	Runtimes []string `json:"runtimes" binding:"required"`
	Provider string   `json:"provider,omitempty"`
	// Mcps MCP server id 白名单（nil = 未声明 → 全量；`[]` = 禁用全部）。非法 id 不在此拒绝
	// （与定义文件/导入一致：写了不存在的 id 在注入时 WARN 跳过），校验只判类型。
	Mcps             *[]string             `json:"mcps,omitempty"`
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy,omitempty"`
}

// AgentUpdateReq PATCH agent 元数据（指针字段：nil 不更新；`null` = 清除该字段）。
type AgentUpdateReq struct {
	Name             *string               `json:"name"`
	Runtimes         *[]string             `json:"runtimes"`
	Provider         *string               `json:"provider"`
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy"`
	// Mcps 原始 JSON 而不是 `*[]string`：只有原始形态能区分「键缺失」（不改）与 `null`
	// （清空白名单 = 回落未声明）。typed 指针会把两者都变成 nil，"恢复全量"就再也表达不出来。
	// 取值域见文件头三态说明；解析在 manager 写面（`decodeAgentMcpsPatch`）。
	Mcps json.RawMessage `json:"mcps"`
}
