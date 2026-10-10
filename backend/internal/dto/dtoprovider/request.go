package dtoprovider

// ProviderPatchReq PATCH provider 档案（**只更新，不再 upsert**，见设计 D-4）：
// type/note 指针更新；key 非 nil 即覆盖/清除（空串清除）；不存在 → `provider_not_found`(40401)。
//
// **没有 `model` 字段**（E4/D-1）：它历史上是个静默陷阱（字段名叫 model、UI 里也显示，
// 但改它不产生任何行为）。默认模型归 `models.is_default`，而「这条凭据能提供哪些模型」
// 归 `models` 里 `provider_id` 指向它的那些行——provider 只是模型挂靠的凭据。
// 历史值原样留在正文里（`mate provider export` 还能导出），只是写面不再接受它。
//
// 收紧 upsert 的理由：它让「打错 id 的新建」与「更新」不可分辨，而这两件事的失败模式完全不同
// （前者是被发现，后者是应该成功）。有了 POST 之后，upsert 只会让人绕过 id 校验。
type ProviderPatchReq struct {
	Type *string `json:"type"`
	Note *string `json:"note"`
	Key  *string `json:"key"`
	// Projections 逐 runtime 的**运行时参数**声明（H1）：`{"<runtime>": {"api":"…","baseURL":"…"}}`。
	//
	// 语义与其它指针字段一致：**nil = 不改**；给了就以它为准（该 runtime 没有的键不参与，所以
	// "只改 opencode 那一份"是安全的）。未知键原样保留（平台只校验它认识的字段）。
	Projections map[string]map[string]any `json:"projections"`
}

// ProviderCreateReq POST /providers：新建一条档案（id 必填且过词法校验）。
//
// 已存在 → `provider_exists`(40901)，**绝不覆盖**（与 skills/mcps/automations 的 POST 同形）。
// `key` 为 nil 或空串 = 先建一条"没有密钥"的档案（自检会把它报成一条可执行的 fail）。
type ProviderCreateReq struct {
	ID   string  `json:"id"`
	Type string  `json:"type"`
	Note *string `json:"note"`
	Key  *string `json:"key"`
	// Projections 见 ProviderPatchReq：逐 runtime 的运行时参数（自定义模型要连通就得写它）。
	Projections map[string]map[string]any `json:"projections"`
}

// ProviderProbeReq POST /providers/:id/probe 的请求体。
//
// `Upstream=true` 是对**第三方**发一次带密钥的请求（用户显式动作）；缺省/`false` = 只跑本地段
// （零网络请求）。`GET` 或页面加载**永不**触发上游段——那会把用户密钥发到第三方。
type ProviderProbeReq struct {
	Upstream bool `json:"upstream"`
}
