package dtomodel

// ——— 应用层模型清单（v12，设置 · 模型）的对外面 ———
//
// 与 `model_catalog.go` 里的运行时目录面（`ModelCatalogResp`，读面）刻意分开：这里管的是**平台声明**，
// 那边是**运行时事实**。两者在 `GET /models` 的同步状态里相遇，但它们的来源、写入方与生命周期
// 完全不同——混在一个结构里会让"这条到底是谁说的"变得不可判定。

// ModelResp 一条模型条目（**不带 runtime 维度**：同一份清单投给所有已注册底座）。
type ModelResp struct {
	ID string `json:"id"`
	// ProviderID 挂靠的供应商 id。
	ProviderID string `json:"provider_id"`
	// Model 发给上游的模型名（**不是**运行时给的不透明 value，也不是平台 id）——线上键名就叫
	// `model`（v24 从 `model_id` 改名）。它**不是** `/_id` 家族：那一族一律指"指向某表 `id`"，
	// 而这一列是外部世界的名字。
	Model string `json:"model"`
	// Name 展示名（可选；空 = 前端回落显示 Model）。
	Name string `json:"name,omitempty"`
	// Note 备注（不驱动行为）。
	Note string `json:"note,omitempty"`
	// InputModalities 该模型能接受的输入模态（**总是带值**，默认 `["text"]`）。
	//
	// 两态：`["text"]` | `["text","image"]`。它是**平台声明**（意图），不是底座目录里那条模型
	// 本来支持什么（事实）——两者在 `input` 上会打架，而平台的声明**总是写进底座声明**
	// （ADR-12 的代价：会覆盖 catalog，所以界面必须让用户能看见并一键改）。
	InputModalities []string `json:"input_modalities,omitempty"`
	// MaxInputTokens / MaxOutputTokens **平台声明的容量**（v25；`null` = 未声明）。
	//
	// **没有 `omitempty`**：这两个字段的"没声明"是一个**要说出来的事实**——界面据此标注"容量由环境
	// 兜底（不是平台声明）"，而省略键会让"未声明"与"这台服务版本旧、根本没这个字段"长得一样。
	MaxInputTokens  *int64 `json:"max_input_tokens"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
	// Revision 写面乐观锁计数（每次成功提交 +1）；**不是内容版本**——同一内容写两次它照样前进，
	// 而"那一版到底是什么"的内容指纹在审计 digest 里（ADR-13）。
	Revision int64 `json:"revision"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano）。
	UpdatedAt string `json:"updated_at,omitempty"`
	// Effect 写面回执（只在写面响应里出现；列表里为空）。
	Effect *ModelEffect `json:"effect,omitempty"`
}

// ModelEffect 模型清单写面的生效面回执。
//
// v12 起只有**一条**通道（把整份清单交给各 adapter），所以它不再有 v11 那种
// `config_write` / `session_config` 的并列——写面提交后触发一次同步，逐 runtime 的结论在
// `GET /models` 的 `sync[]` 里。这里只回答"这次改动会影响哪些会话、什么时候"。
type ModelEffect struct {
	// Effect 生效时刻：next_session（下一次会话创建/换代际）| next_process（新底座进程读配置）。
	Effect string `json:"effect"`
	// AffectedSessions 由**可能落到任一已注册底座**的 agent 服务的会话数（换代际的影响面）。
	AffectedSessions int `json:"affected_sessions"`
	// Deferred 其中本轮不能就地换代际的会话数（有在途轮次 / 有未应答权限或提问）。
	Deferred int `json:"deferred"`
	// Note 一句话人话说明（含"什么时候生效"的如实措辞）。
	Note string `json:"note,omitempty"`
}

// ModelCreateReq POST /models 的请求体。
//
// `runtime` **不在**资源字段表里（A2）：请求体里出现它 → 40001。它不是一个"被忽略的旧字段"，
// 而是一个会让人以为模型分底座的陷阱——静默收下就等于承认它有意义。
type ModelCreateReq struct {
	ProviderID string  `json:"provider_id" binding:"required"`
	Model      string  `json:"model" binding:"required"`
	Name       *string `json:"name"`
	Note       *string `json:"note"`
	// InputModalities 输入模态（`["text"]` | `["text","image"]`）；nil = 默认 `["text"]`。
	// 非法取值 → 40501 并列出合法取值（枚举型键的既有纪律：不静默回落）。
	InputModalities *[]string `json:"input_modalities"`
	// MaxInputTokens / MaxOutputTokens 平台声明的容量（v25；省略 = 不声明）。
	// 取值必须 ≥ 1（0 不是合法容量）；非法取值 → 40501。
	MaxInputTokens  *int64 `json:"max_input_tokens"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
	// IsDefault true = 把这条设为**唯一**默认模型（写进 meta 的单一值，ADR-21）。
	IsDefault *bool `json:"is_default"`
	// Runtime 仅为**显式拒收**而存在（A2）。它不是资源字段，任何非 nil 取值都报 40001。
	Runtime *string `json:"runtime"`
}

// ModelPatchReq PATCH /models/:id：指针更新（nil = 不改）。
type ModelPatchReq struct {
	Name            *string   `json:"name"`
	Note            *string   `json:"note"`
	InputModalities *[]string `json:"input_modalities"`
	// MaxInputTokens / MaxOutputTokens 平台声明的容量（v25）。**三态**，因为"未声明"是一个要能回得去
	// 的状态：
	//
	//	省略（nil） = 不改
	//	≥ 1         = 声明成这个数
	//	0           = **清除声明**（落库为 NULL，回到"由环境兜底"）
	//
	// 为什么用 0 当"清除"的哨兵：0 本来就不是合法容量（底座 schema 是 `min(1)`，写 0 会被它的校验门
	// 拒掉），所以它不会与任何真实取值撞车；负数 → 40501 `invalid_param`。
	// 为什么不用 `null`：encoding/json 里 `null` 与"键缺失"都落成 nil，两者分不开。
	MaxInputTokens  *int64 `json:"max_input_tokens"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
	IsDefault       *bool  `json:"is_default"`
	// Runtime 见 ModelCreateReq：显式拒收（A2）。
	Runtime *string `json:"runtime"`
}

// ModelDeleteResp DELETE /models/:id 的结果。
//
// `Deleted` 幂等：本来就不存在 → false 且 code=0。
//
// v12 的**行为变更**（B4）：删掉那条正好是默认模型的条目**不再被拒绝**——默认是**偏好**，
// 偏好跟着资源消失比拦住用户更合理；`DefaultCleared` 如实回带"顺手清了默认"。
// （v11 是 40501 `model_is_default` 拒绝。）
type ModelDeleteResp struct {
	Deleted bool `json:"deleted"`
	// DefaultCleared 本次删除连带清空了默认模型（用户可见的副作用，必须说出来）。
	DefaultCleared bool `json:"default_cleared,omitempty"`
}

// ModelsResp `GET /models` 的响应。
//
// **用户面只有 `models` / `default_model` / `effective` / `effective_at` 四个字段**（ADR-22）：
// 设置 · 模型 那一节不许出现底座词汇，所以逐 runtime 的明细一律落在 `sync[]` 里，标为**机器面**。
// 前端**不得直接渲染** `sync[].runtime` / `where` / `problems` 的值——A13 的守卫只拦字面量，
// 所以这条边界还要由 A14 的结构断言 + code review 一起兜。
type ModelsResp struct {
	// Models 平台清单（**不分页**：清单上限 200 条，全量返回的依据见设计 §数据模型）。
	Models []ModelResp `json:"models"`
	// DefaultModel 唯一默认模型的**平台模型 id**（空 = 未设）。
	//
	// 交付时由编排层把它派生成每个 runtime 各自的默认——存储层不带底座维度，投影层才带。
	DefaultModel string `json:"default_model"`
	// Effective 用户面**聚合结论**：applied | pending | failed。
	Effective string `json:"effective"`
	// EffectiveAt 用户能理解的时间点：now | next_session | next_launch。
	// 取**保守值**（所有 runtime 里最晚的那个）——见 ADR-22 的代价说明。
	EffectiveAt string `json:"effective_at"`
	// Sync ⚠️ **机器面 / 诊断面**：逐 runtime 明细。前端不得直接渲染这里的 runtime/where/problems。
	Sync []RuntimeSyncResp `json:"sync"`
	// Note 人话说明（清单为空 / 没有可同步的底座等）。
	Note string `json:"note,omitempty"`
}

// RuntimeSyncResp 某 runtime 的一次同步结论（机器面）。
type RuntimeSyncResp struct {
	Runtime string `json:"runtime"`
	// Status applied | unchanged | unsupported | untested | failed。
	Status string `json:"status"`
	// Where 人话落点（给排障看的「同步到哪了」）。
	Where string `json:"where"`
	// Effect immediate | next_run | next_session | next_process（**清单**通道的生效时刻）。
	Effect string `json:"effect"`
	// Pending true = 平台已交付但底座还没读到（新进程才读）——**不许把它说成"已生效"**。
	Pending bool `json:"pending"`
	// Credential 凭据交付面的结论，与清单**分开报**（ADR-14）。
	Credential CredentialSyncResp `json:"credential"`
	// Advertised 底座事实上报的候选（尽力采样；拿不到为 null）。
	Advertised []string `json:"advertised,omitempty"`
	// Missing 声明了但没出现在 advertised 里的。
	Missing []string `json:"missing,omitempty"`
	// Problems 可执行的一句话清单。
	Problems []string `json:"problems"`
}

// CredentialSyncResp 凭据交付面的结论（机器面）。
type CredentialSyncResp struct {
	// Channel native_doc | env | none。
	Channel string `json:"channel"`
	// Status applied | unchanged | unsupported | untested | failed。
	Status string `json:"status"`
	// Effect 同 RuntimeSyncResp.Effect 的取值域。
	Effect string `json:"effect"`
	// Tested 该通道是否已实测；**false 时界面必须显示「待实测」而不是「已生效」**。
	Tested bool `json:"tested"`
	// Where 人话落点（绝不回显密钥）。
	Where string `json:"where"`
}

// AdvertisedModel 运行时**实际广告**的一个模型候选（事实）。它与 `ModelResp`（意图）来源不同、
// 生命周期不同，所以类型也分开——混在一起会让"这条到底是谁说的"不可判定。
//
// 它**不进用户面**：只在机器面（`sync[]`、`mate doctor`）出现。
type AdvertisedModel struct {
	// Value 运行时**不透明串**（形态由各 adapter 自己声明，见 `pkg/runtime/model_value.go`）。
	Value string `json:"value"`
	Name  string `json:"name,omitempty"`
	Group string `json:"group,omitempty"`
	// GroupName 分组名（agent 以分组形态下发时才有）。
	GroupName string `json:"group_name,omitempty"`
	// Unavailable 平台观测：该值最近被运行时拒绝过（如凭证缺失）。
	Unavailable       bool   `json:"unavailable,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// ModelSyncReq `POST /models/sync` 的请求体。
type ModelSyncReq struct {
	// Runtime 要重新同步的 runtime；空 = 全部已注册。
	Runtime string `json:"runtime"`
}

// ModelSyncResp `POST /models/sync` 的响应：逐 runtime 的同步结论。
//
// **成功路径里没有 `advertised`**：那要新进程才拿得到，此刻报它就是假装已生效。
type ModelSyncResp struct {
	Applied []RuntimeSyncResp `json:"applied"`
}
