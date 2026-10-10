package dtomodel

// ——— 平台模型清单（v11，设置 · 模型）的对外面 ———
//
// 与 `response.go` 里的运行时目录面（`ModelCatalogResp`，读面）刻意分开：这里管的是**平台声明**，
// 那边是**运行时事实**。两者在 `GET /models` 的对账响应里相遇（G9），但它们的来源、写入方与
// 生命周期完全不同——混在一个结构里会让"这条到底是谁说的"变得不可判定。

// ModelResp 一条平台模型条目。
type ModelResp struct {
	ID         string `json:"id"`
	Runtime    string `json:"runtime"`
	ProviderID string `json:"provider_id"`
	// ModelID 平台写出去给底座的模型 id（**不是**运行时给的不透明 value，见 Value）。
	ModelID string `json:"model_id"`
	// Name 平台侧显示名（可选）。它只影响平台呈现与配置里的 name 字段，**改不动 ACP 候选项里的
	// 路由前缀**（那恒为路由 key，见事实 #27）。
	Name string `json:"name,omitempty"`
	// Extra 该条目的额外配置（原样物化进配置；形状由各 runtime 的生成器解释）。
	Extra map[string]any `json:"extra,omitempty"`
	// Hidden 平台侧隐藏（**不下发**：底座的目录里仍然会出现它——隐藏不是"删掉它"）。
	Hidden bool `json:"hidden"`
	// Pinned 平台侧置顶（只改平台列表顺序，不下发）。
	Pinned bool `json:"pinned"`
	// IsDefault 是否该 runtime 的默认模型（每个 runtime 至多一条）。
	IsDefault bool `json:"is_default"`
	// Value 平台按该 runtime 的规则**生成**的不透明值（dsh = JSON.stringify([provider,model])、
	// opencode = provider/model）。会话级下发（T2）与对账都用它。
	//
	// 它**不进清单的存储**：清单只存生成 value 的输入（runtime/provider_id/model_id）。
	// 这是本方案里最容易写错的一处——把运行时给的 value 存回清单，等于把"事实"当成了"意图"。
	Value string `json:"value"`
	// Revision 载体版本（行 CAS 判据）。
	Revision int64 `json:"revision"`
	// UpdatedAt 最后一次成功提交的时刻（RFC3339Nano）。
	UpdatedAt string `json:"updated_at,omitempty"`
	// Effect 写面回执（只在写面响应里出现；列表里为空）。
	Effect *ModelEffect `json:"effect,omitempty"`
}

// ModelEffect 模型清单写面的生效面回执（T4 的落地形态在模型面上的那一份）。
//
// 模型面有**两条**通道，而它们不是替代关系（ADR-11）：
//
//	config_write（T5）  新进程启动时读配置 → 引用该 runtime 的会话在轮次边界换代际。
//	session_config（T2）新会话创建时由平台下发 → **立即**，且首版 dsh 侧 T5 只读时它是唯一兑现路径。
type ModelEffect struct {
	// Channels 本条改动走的通道（config_write 恒在；改了默认模型时还有 session_config）。
	Channels []string `json:"channels"`
	// Effect 生效时刻：next_session（下一次建会话时下发）| next_process（新 runtime 进程读配置）。
	// 两条通道同时在时取**最早**的那个（用户关心的是"最快什么时候能用上"）。
	Effect string `json:"effect"`
	// AppliesToRuntimes 受影响的 runtime（恒为这一条所属的 runtime）。
	AppliesToRuntimes []string `json:"applies_to_runtimes"`
	// AffectedConversations 由**可能落到该 runtime** 的 agent 服务的会话数（换代际的影响面）。
	AffectedConversations int `json:"affected_conversations"`
	// Deferred 其中本轮不能就地换代际的会话数（有在途轮次 / 有未应答权限或提问）。
	Deferred int `json:"deferred"`
	// Note 一句话人话说明（含"什么时候生效"的如实措辞）。
	Note string `json:"note,omitempty"`
}

// ModelCreateReq POST /models 的请求体。
type ModelCreateReq struct {
	Runtime    string         `json:"runtime" binding:"required"`
	ProviderID string         `json:"provider_id" binding:"required"`
	ModelID    string         `json:"model_id" binding:"required"`
	Name       *string        `json:"name"`
	Extra      map[string]any `json:"extra"`
	Hidden     *bool          `json:"hidden"`
	Pinned     *bool          `json:"pinned"`
	IsDefault  *bool          `json:"is_default"`
}

// ModelPatchReq PATCH /models/:id：指针更新（nil = 不改）。
type ModelPatchReq struct {
	Name      *string        `json:"name"`
	Extra     map[string]any `json:"extra"`
	Hidden    *bool          `json:"hidden"`
	Pinned    *bool          `json:"pinned"`
	IsDefault *bool          `json:"is_default"`
}

// ModelDeleteResp DELETE /models/:id 的结果。
//
// `Deleted` 幂等：本来就不存在 → false 且 code=0。
// 若目标是该 runtime 的默认模型 → **拒绝删除**（40501 `model_is_default`）并回带 `NextAction`：
// 不静默把默认置空——那会让"平台的默认模型"在一个用户看不见的时刻消失。
type ModelDeleteResp struct {
	Deleted bool `json:"deleted"`
	// NextAction 被拒时的可执行出路（先指定另一个默认，再删这一条）。
	NextAction string `json:"next_action,omitempty"`
}

// RuntimeConfigResp 某 runtime 的**模型配置物化状态**（T5）。
//
// 它是 `GET /models` 的 `config` 段与 `POST /models/apply` 的响应共用的形状——两处必须同形，
// 否则"保存之后它到底写了什么"会有两套说法。
//
// `Pending` 是**诚实性字段**：平台已声明、配置文件已（或未）写出，但底座要**新进程**才读得到。
// 所以它恒为 true 直到有新进程把内容读进去——界面不许把 `changed=true` 说成"已生效"。
type RuntimeConfigResp struct {
	Runtime string `json:"runtime"`
	// Declared 该 runtime 是否声明了模型配置落点（false = T5 不可达）。
	Declared bool `json:"declared"`
	// Tested 落点是否经真实底座实测；false = **只读**（生成候选但不落盘）。
	Tested bool `json:"tested"`
	// Writable 现在能不能真的写（Declared && Tested）。
	Writable bool `json:"writable"`
	// Channel 交付通道（config_dir_read | cli_patch_arg | env_content）。
	Channel string `json:"channel,omitempty"`
	// Path 配置文件绝对路径（未声明时为空）。
	Path string `json:"path,omitempty"`
	// Digest 当前落盘（或候选）内容的摘要。
	Digest string `json:"digest,omitempty"`
	// GeneratedAt 最近一次物化的时刻（RFC3339Nano）。
	GeneratedAt string `json:"generated_at,omitempty"`
	// Changed 本次调用是否真的改变了落盘内容。
	Changed bool `json:"changed"`
	// Pending true = 要等新进程才生效（**不许把它说成"已生效"**）。
	Pending bool `json:"pending"`
	// ModelCount 本次生成用到的清单条数（0 = 平台没声明任何模型 → 不写任何东西）。
	ModelCount int `json:"model_count"`
	// Note 人话说明（含"未实测只读""空清单不写"这类如实措辞）。
	Note string `json:"note,omitempty"`
}

// ModelApplyReq `POST /models/apply` 的请求体。
type ModelApplyReq struct {
	// Runtime 要重新物化的 runtime；空 = 全部已声明落点的 runtime。
	Runtime string `json:"runtime"`
}

// ModelApplyResp `POST /models/apply` 的响应：逐 runtime 的物化结果。
//
// **成功路径里没有 `advertised`**：那要新进程才拿得到，此刻报它就是假装已生效。
type ModelApplyResp struct {
	Applied []RuntimeConfigResp `json:"applied"`
}
