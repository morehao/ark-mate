package interaction

// ——— 结构化提问（elicitation）的**领域值类型**（C2：底座协议不再进入本域）———
//
// 与权限面同构：这些形状描述"平台向用户提一次问、用户怎么答"，协议侧在连接边界翻译。

// 归属来源（`ElicitationRequest.ScopeSource` 的取值；对外载荷 `scope_source` 逐字用它）。
const (
	// ScopeWire scope 由 wire（SDK 顶层 sessionId/_meta）提供。
	ScopeWire = "wire"
	// ScopeInferred scope 由宿主按「连接上唯一在途 run」推断。
	ScopeInferred = "inferred_inflight"
	// ScopeAmbiguous `_meta` 未给 scope 且连接上无唯一在途 run（多会话并发在途 / 会话外
	// request-scope）→ 无法归属。宿主仍按取消回应（不能把 agent 挂住），但归属来源标为它并留痕，
	// 使「为什么这张提问卡没出现」在诊断里可查，而不是无痕消失。
	ScopeAmbiguous = "ambiguous"
)

// ElicitationSchema 是 form 模式的 JSON Schema（属性名 → 属性对象，含 type/enum/oneOf/items…）。
type ElicitationSchema struct {
	Title       string
	Description string
	Properties  map[string]any
	Required    []string
}

// ElicitationRequest 是一次结构化提问。
type ElicitationRequest struct {
	// SessionID 会话归属：先按 wire `_meta` 填（ScopeWire），未命中留空由宿主按连接上下文注入
	// （ScopeInferred）。
	SessionID string
	// ScopeSource 归属来源（wire | inferred_inflight）：由注入方填写，供载荷与排障展示。
	ScopeSource string
	// Mode "form" | "url"。
	Mode    string
	Message string
	// Schema Mode=form 时的结构化表单。
	Schema *ElicitationSchema
	// URL Mode=url 时的目标地址。
	URL string
	// URLID Mode=url 时 agent 侧的 elicitationId（elicitation/complete 的反查锚点）。
	URLID string
}

// ElicitationOutcome 是宿主对一次结构化提问的决策。
type ElicitationOutcome struct {
	// Action ∈ accept | decline | cancel；accept 可携带用户提交的 content。
	Action  string
	Content map[string]any
}

// 提问决策的 action 取值（对外契约的取值集，与协议 wire 同值）。
const (
	ElicitationAccept  = "accept"
	ElicitationDecline = "decline"
	ElicitationCancel  = "cancel"
)
