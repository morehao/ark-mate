// Package dtoidentity 是控制面身份面的对外契约（`GET /control/me`）。
//
// 承载「当前请求已验证身份」的投影，供前端展示/调试与端到端核对（设计见
// `.dsh/docs/specs/2026-11-24-ark-iam-rp-integration-design.md` §接口设计）。
// 字段口径对齐 ark-iam `contract.Identity`（身份真源唯一 = ark-iam）。
package dtoidentity

// MeResp 是 `/control/me` 的响应体：当前令牌身份的只读投影。
//
// 刻意**不含**任何凭据（access_token / secret）——它只是身份事实，前端无需持有令牌原文。
type MeResp struct {
	// PersonID 自然人 ID（机器凭证为空）。
	PersonID string `json:"personId"`
	// UserID = ark-iam user.id（审计主键）。
	UserID string `json:"userId"`
	// TenantID 租户作用域（令牌未携带时为空 → 需租户接口会 403）。
	TenantID string `json:"tenantId"`
	// ClientID 令牌 client_id。
	ClientID string `json:"clientId"`
	// IsMachine 是否机器凭证。
	IsMachine bool `json:"isMachine"`
	// Scopes 标准 scope 声明（已拆分）。
	Scopes []string `json:"scopes"`
}
