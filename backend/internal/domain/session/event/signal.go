package event

// ——— 底座回传给平台的**运行期信号**（领域语言）———
//
// 为什么需要它：会话域的事件出口（`Runner.EventSink`）此前直接吃 `infra/acp.Event`——于是
// 「领域认识协议」。这一族把那份协议降级成**适配器的输入**：ACP 客户端把线上事件翻译成这里
// 的信号，领域只认这组类型。
//
// 判据只有一条：**这里的类型不得出现任何协议类型的字段**。`Payload` 是 `any` 只为止损——
// 配置项/命令两类载荷目前仍是底座原生结构（`[]acp.ConfigOption` 等），但领域**不命名**它们，
// 只把它当作不透明值转发；等配置项那一刀（C2 第三块）再把它们也换成域类型。
//
// 事件 type（`Bus*` 常量）与载荷键**不在本包**：那是对外契约，冻结在 `session/events.go`
// 与前端类型里，搬它会改 wire。

// Kind 运行期信号的种类（与 `acp.Kind*` 一一对应，但取值是平台自己的词汇）。
type Kind string

const (
	// KindMessage 模型输出的文本增量。
	KindMessage Kind = "message"
	// KindThought 思考文本增量。
	KindThought Kind = "thought"
	// KindToolCall 一次工具调用被发起。
	KindToolCall Kind = "tool_call"
	// KindToolUpdate 工具调用的状态/结果更新（可能只带 status）。
	KindToolUpdate Kind = "tool_update"
	// KindUserMessage 底座回显的用户消息（平台**丢弃**：用户输入以宿主侧提交为唯一来源）。
	KindUserMessage Kind = "user_message"
	// KindUsage 一次会话用量快照。
	KindUsage Kind = "usage"
	// KindPermission 一次权限决策留痕（决策后上抛）。
	KindPermission Kind = "permission"
	// KindConfigOptions 运行时推送的配置项全量快照。
	KindConfigOptions Kind = "config_options"
	// KindCommands 运行时通告的可用命令全量快照。
	KindCommands Kind = "commands"
)

// Signal 一条运行期信号：种类 + 原生会话 id + 类型化载荷。
//
// `SessionID` 是**底座侧**的原生会话 id：翻译发生在适配器，双 id 解析（原生 → 平台 uuid）
// 仍由领域做——那是平台事实，不是协议事实。
type Signal struct {
	Kind      Kind
	SessionID string
	Payload   any
}

// MessageDelta 消息文本增量。
type MessageDelta struct {
	Text string
	// MessageID 该 chunk 所属逻辑消息（同一 id 的 chunk 属于同一条消息）。
	MessageID string
}

// ThoughtDelta 思考文本增量。
type ThoughtDelta struct {
	Text      string
	MessageID string
}

// 工具调用状态（平台归一的两个终态；其余取值由底座原样透传）。
const (
	// ToolStatusCompleted 工具调用完成。
	ToolStatusCompleted = "completed"
	// ToolStatusFailed 工具调用失败。
	ToolStatusFailed = "failed"
)

// ToolCall 工具调用信息（等价于 ACP 的 tool_call / tool_call_update 载荷）。
type ToolCall struct {
	ID     string
	Title  string
	Status string
	// Kind 底座给的工具种类（read/edit/execute/…），可空。
	Kind  string
	Input any
	// Output 调用结果（仅 tool_call_update 携带时非空）。
	Output any
	// ContentText 结果里的**纯文本**拼接。
	//
	// 为什么必须有它：dsh 从不下发 rawOutput——工具结果正文只在 content 里。缺了它，
	// 「这条调用为什么没成」就无从判断（被沙箱拒绝的那一帧 status 是 `completed`）。
	ContentText string
}

// Permission 一次权限决策留痕（决策后上抛）。
//
// **json tag 与 ACP 的 wire 形状逐字相同**：本结构会被原样落进事件载荷（`session.permission`
// 的 payload），改一个键就是改对外契约与历史行。
type Permission struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Allowed   bool   `json:"allowed"`
	Rejected  bool   `json:"rejected,omitempty"`
	OptionID  string `json:"option_id,omitempty"`
	Always    bool   `json:"always,omitempty"`
}

// ConfigOptions 配置项全量快照（`Options` 是不透明载荷，见包注释的止损说明）。
type ConfigOptions struct {
	SessionID string
	Options   any
}

// Commands 可用命令全量快照（`Commands` 是不透明载荷）。
type Commands struct {
	SessionID string
	Commands  any
}
