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
