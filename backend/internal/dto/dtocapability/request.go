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
