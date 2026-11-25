package dtoidentity

// TenantCandidate 是一个可切换租户的候选（`POST /control/tenants/switch`）。
type TenantCandidate struct {
	// TenantID = ark-iam tenant_id。
	TenantID string `json:"tenantId"`
	// Name 展示名（平台 control_tenants.display_name；缺失时回退为空，前端按 id 展示）。
	Name string `json:"name"`
}

// TenantsSwitchResp 是 `POST /control/tenants/switch` 的响应（决策 v38、设计 §接口设计）。
//
// 口径（设计明确）：租户切换**不在服务端改令牌**——本接口只**返回候选租户 + 触发前端重新走
// OIDC 带 `tenant` hint**。
//
//   - Mode 恒为 "oidc"：前端据此 `signinRedirect({ extraQueryParams:{ tenant } })`（SDK 的
//     useTenantSwitching 已实现）。服务端不签发/改写令牌。
//   - CurrentTenantID：当前令牌 claim 的 tenant_id（空 = 令牌未携带租户）。
//   - Tenants：候选租户清单。**骨架期**：候选枚举来自 ark-iam（人→租户关系），需服务端目录客户端；
//     当前只回填「当前租户」一条（若令牌带 tenant_id），完整枚举随 M2 目录接入补齐（设计 §已知未覆盖）。
//   - RequestedTenantID：请求体若带 tenantId，原样回显（服务端仅校验非空格式，不改任何状态）。
type TenantsSwitchResp struct {
	Mode              string            `json:"mode"`
	CurrentTenantID   string            `json:"currentTenantId"`
	RequestedTenantID string            `json:"requestedTenantId,omitempty"`
	Tenants           []TenantCandidate `json:"tenants"`
}
