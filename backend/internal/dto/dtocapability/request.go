package dtocapability

import "encoding/json"

// McpToolCallReq 一次 MCP 工具调用请求（POST /mcps/:id/tools/:tool/call）。
type McpToolCallReq struct {
	// Arguments 工具入参对象（原样透传；nil/缺省 = 无参数）。
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// TimeoutMS 调用超时（毫秒；0 = 默认 30s，上限 5min）。
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// McpResourceReadReq resources/read 请求体。
type McpResourceReadReq struct {
	URI string `json:"uri"`
}

// McpPromptGetReq prompts/get 请求体（MCP 规定参数值为字符串）。
type McpPromptGetReq struct {
	Arguments map[string]string `json:"arguments,omitempty"`
}

// SkillToggleReq 启停一个 skill（§13.4 S4）。只认 enabled 一个开关：启停是 overlay，
// 正文/来源都由定义文件承载（平台不在这里替用户改内容）。
type SkillToggleReq struct {
	Enabled bool `json:"enabled"`
}

// ——— 写面（M2）：请求体是**原文 JSON 对象**，键的"出现与否"本身是语义 ———

// SkillWriteReq 新建/覆盖一个 skill 定义（POST /skills、PUT /skills/:id）。
//
// 为什么是 map 而不是带指针字段的结构体：PUT 的语义是"只改请求体里**出现过的**键"，而结构体
// 分不清"没传 name"与"传了 name:”"；`"k": null` 还要求能表达"删掉这个键"。键的出现与否在这里
// 是语义，不是编码细节，所以保留原始 JSON。
//
// 强类型视图（capability.Skill）由 manager 把 patch **合并进现有定义后**解出——先合并再校验，
// 因为正文扫描闸门必须看到合并后的正文（`PUT {"name":"x"}` 不该让正文凭空变空而绕过扫描）。
type SkillWriteReq map[string]json.RawMessage

// McpWriteReq 新建/覆盖一个 MCP server 定义（POST /mcps、PUT /mcps/:id）。语义同上。
//
// 密钥仍是**引用**（`store:`/`env:`）——写面只搬运引用，明文密钥永远不会经这里落盘。
type McpWriteReq map[string]json.RawMessage
