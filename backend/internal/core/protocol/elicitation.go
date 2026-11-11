package protocol

// 结构化提问通道（形式上对齐 ACP elicitation/*，规范仍标 unstable）：
// agent 在 prompt 中途请求宿主向用户渲染一张表单（单选/多选/文本/数字/布尔，由 JSON
// Schema 驱动）或引导打开一个 URL，并**阻塞等待**用户提交/放弃（accept/decline/cancel）。
// 本包只承载载荷本身；协议侧的编解码与归属推断在连接边界。

// ElicitationSchema 是表单字段的 JSON Schema 表示（属性为原始 schema 对象，
// 平台不做逐字段校验，原样透传给 UI 渲染控件）。
type ElicitationSchema struct {
	Title       string
	Description string
	Properties  map[string]any // 属性名 → JSON Schema 属性对象（含 type/enum/oneOf/items…）
	Required    []string
}

// ElicitationRequest.ScopeSource 取值（归属来源）。
const (
	// ScopeWire：scope 由 wire（协议顶层 sessionId/_meta）提供。
	ScopeWire = "wire"
	// ScopeInferred：scope 由宿主按「连接上唯一在途 run」推断（协议未建模时的过渡实现）。
	ScopeInferred = "inferred_inflight"
	// ScopeAmbiguous：`_meta` 未给 scope 且连接上无唯一在途 run（多会话并发在途 / 会话外
	// request-scope）→ 无法归属。宿主仍按取消回应（不能把 agent 挂住），但归属来源标为它并留痕，
	// 使「为什么这张提问卡没出现」在诊断里可查，而不是无痕消失。
	ScopeAmbiguous = "ambiguous"
)

// ElicitationRequest 是 agent 发起的一次结构化提问请求（宿主需渲染并征询用户）。
type ElicitationRequest struct {
	// SessionID 会话归属：协议层先按 `_meta` 填（ScopeWire），未命中留空由宿主
	// （orchestrator）按连接上下文注入（ScopeInferred）。
	SessionID string
	// ScopeSource 归属来源（wire | inferred_inflight）：由注入方填写，供 REST/WS 载荷与排障展示。
	ScopeSource string
	Mode        string // "form" | "url"
	Message     string
	Schema      *ElicitationSchema // Mode=form
	URL         string             // Mode=url
	URLID       string             // Mode=url：协议侧 elicitationId（透传）
	// Timed 限时问答标记（answerer 桥接的 `askTimed` / `ask_user_question` timed 模式）。
	//
	// 为真时，`ElicitationManager.RegisterAndWait` 把「调用方断开」当作「平台侧转 pending、
	// 卡片保留可回答」而不是取消：底座在它自己的计时器到期时会中止这一路桥接、拿到 pending
	// 让模型先继续，用户随后仍可作答，回答作为**新一轮**进入（ADR-11）。非限时（ACP
	// elicitation/create、阻塞式 answerer ask）为假，断开 = 取消。
	Timed bool
}

// ElicitationOutcome 是宿主对一次结构化提问的决策。
type ElicitationOutcome struct {
	// Action ∈ accept | decline | cancel；accept 可携带用户提交的 content。
	Action  string
	Content map[string]any
}
