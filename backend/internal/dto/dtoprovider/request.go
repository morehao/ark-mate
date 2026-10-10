package dtoprovider

// ProviderPatchReq PATCH provider 档案（**只更新，不再 upsert**，见设计 D-4）：
// type/model/note 指针更新；key 非 nil 即覆盖/清除（空串清除）；不存在 → `provider_not_found`(40401)。
//
// 收紧 upsert 的理由：它让「打错 id 的新建」与「更新」不可分辨，而这两件事的失败模式完全不同
// （前者是被发现，后者是应该成功）。有了 POST 之后，upsert 只会让人绕过 id 校验。
type ProviderPatchReq struct {
	Type  *string `json:"type"`
	Model *string `json:"model"`
	Note  *string `json:"note"`
	Key   *string `json:"key"`
}

// ProviderCreateReq POST /providers：新建一条档案（id 必填且过词法校验）。
//
// 已存在 → `provider_exists`(40901)，**绝不覆盖**（与 skills/mcps/automations 的 POST 同形）。
// `key` 为 nil 或空串 = 先建一条"没有密钥"的档案（自检会把它报成一条可执行的 fail）。
type ProviderCreateReq struct {
	ID    string  `json:"id"`
	Type  string  `json:"type"`
	Model *string `json:"model"`
	Note  *string `json:"note"`
	Key   *string `json:"key"`
}

// ProviderProbeReq POST /providers/:id/probe 的请求体。
//
// `Upstream=true` 是对**第三方**发一次带密钥的请求（用户显式动作）；缺省/`false` = 只跑本地段
// （零网络请求）。`GET` 或页面加载**永不**触发上游段——那会把用户密钥发到第三方。
type ProviderProbeReq struct {
	Upstream bool `json:"upstream"`
}
