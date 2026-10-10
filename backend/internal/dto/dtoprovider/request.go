package dtoprovider

// ProviderPatchReq PATCH /providers/:id（**只更新，不再 upsert**）：指针语义，nil = 不改。
//
// v12 的变化：`type` 与 `projections` 一起删掉，换成 `protocol` + `base_url`（ADR-3、ADR-7）。
// 理由是 v11 的 `type` 是个自由串——它唯一的消费者是"环境变量名推导函数"，于是"写错一个词"
// 既不报错也不生效；而真正的 wire 协议与端点是**资源本身**的字段，不该藏在逐底座的投影里。
//
// `api_key`：nil = 不动；`""` = 清除（删 ref + 删密文）；非空 = 覆盖。**密钥只写不读回。**
type ProviderPatchReq struct {
	Name     *string `json:"name"`
	Protocol *string `json:"protocol"`
	BaseURL  *string `json:"base_url"`
	APIKey   *string `json:"api_key"`
	Note     *string `json:"note"`
}

// ProviderCreateReq POST /providers：新建一条供应商（id 必填且过词法校验）。
//
// 已存在 → `provider_exists`(40901)，**绝不覆盖**（与 skills/mcps/automations 的 POST 同形）。
// `api_key` 为 nil 或空串 = 先建一条"没有密钥"的供应商（自检会把它报成一条可执行的 fail）。
type ProviderCreateReq struct {
	ID       string  `json:"id"`
	Name     *string `json:"name"`
	Protocol string  `json:"protocol"`
	BaseURL  *string `json:"base_url"`
	APIKey   *string `json:"api_key"`
	Note     *string `json:"note"`
}

// ProviderProbeReq POST /providers/:id/probe 的请求体。
//
// `Upstream=true` 是对**第三方**发一次带密钥的请求（用户显式动作）；缺省/`false` = 只跑本地段
// （零网络请求）。`GET` 或页面加载**永不**触发上游段——那会把用户密钥发到第三方。
type ProviderProbeReq struct {
	Upstream bool `json:"upstream"`
}
