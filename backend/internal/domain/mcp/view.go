package mcp

import "encoding/json"

// ——— MCP 域的读/写视图（v3.2 刀 5）———
//
// 由域返回的数据形状：只有字段、没有 json tag。对外形态（`dtocapability.Mcp*`）由用例层
// `service/svccapability` 投影——改一个 JSON 字段名不该动领域代码，领域测试也不该断言线上格式。
//
// 字段名与对外 DTO 一一对应（投影是纯搬运，不重命名）。

// WriteRequest 定义写面的**原文对象**：键的语义（`null` = 删键）由 `internal/platform/definition.RawPatch` 解释。
type WriteRequest map[string]json.RawMessage

// ToolCallRequest 工具调用请求。
type ToolCallRequest struct {
	// Arguments 工具入参对象（原样透传；nil/缺省 = 无参数）。
	Arguments json.RawMessage
	// TimeoutMS 调用超时（毫秒；0 = 默认 30s，上限 5min）。
	TimeoutMS int
}

// PromptGetRequest prompt 取值请求（MCP 规定参数值为字符串）。
type PromptGetRequest struct {
	Arguments map[string]string
}

// ToolEntry 一条工具目录条目（探测结果里也用它）。
type ToolEntry struct {
	Name        string
	Description string
	Source      string
	McpServer   string
	Schema      json.RawMessage
}

// WriteResult 新建/覆盖 MCP server 定义的结果。
type WriteResult struct {
	ID                   string
	Path                 string
	Created              bool
	UnresolvedSecretKeys []string
	NextAction           string
}

// DeleteResult 删除 MCP server 定义的结果。
type DeleteResult struct {
	ID           string
	Deleted      bool
	Path         string
	ToolsRemoved int
	NextAction   string
}

// ProbeResult 一次探测的结果。
type ProbeResult struct {
	ID            string
	Transport     string
	ServerName    string
	ServerVersion string
	ProbedAt      string
	Added         int
	Removed       int
	Skipped       int
	ToolCount     int
	Tools         []ToolEntry
}

// ToolCallResult 一次工具调用的结果。
type ToolCallResult struct {
	ServerID          string
	Tool              string
	IsError           bool
	Content           []json.RawMessage
	Text              string
	StructuredContent json.RawMessage
	ServerName        string
	ServerVersion     string
	DurationMS        int64
}

// ResourcesResult 资源与模板列表。
type ResourcesResult struct {
	ServerID       string
	Resources      []ResourceItem
	Templates      []ResourceTemplate
	TemplatesError string
	ServerName     string
	ServerVersion  string
}

// ResourceItem 一条资源。
type ResourceItem struct {
	URI         string
	Name        string
	Title       string
	Description string
	MimeType    string
	Size        *int64
}

// ResourceTemplate 一条资源模板。
type ResourceTemplate struct {
	URITemplate string
	Name        string
	Title       string
	Description string
	MimeType    string
}

// ResourceReadResult 一次资源读取。
type ResourceReadResult struct {
	ServerID string
	URI      string
	Contents []ResourceContent
}

// ResourceContent 资源内容的一段。
type ResourceContent struct {
	URI      string
	MimeType string
	Text     string
	Blob     string
}

// PromptsResult prompt 列表。
type PromptsResult struct {
	ServerID   string
	Prompts    []PromptItem
	ServerName string
}

// PromptItem 一条 prompt。
type PromptItem struct {
	Name        string
	Title       string
	Description string
	Arguments   []PromptArgument
}

// PromptArgument 一个 prompt 参数。
type PromptArgument struct {
	Name        string
	Title       string
	Description string
	Required    bool
}

// PromptGetResult 一次 prompt 取值。
type PromptGetResult struct {
	ServerID    string
	Name        string
	Description string
	Messages    []PromptMessage
}

// PromptMessage prompt 里的一条消息。
type PromptMessage struct {
	Role    string
	Content json.RawMessage
}
