// Package admission 是控制面 SaaS 编排的**准入/租户作用域**判定（L7，决策 v38、设计 DM-4）。
//
// 设计口径（承总纲安全节与子方案 §接口设计「越权/租户」）：
//   - **需租户的接口**：令牌未携带 `tenant_id`（ark-iam 自然人→租户映射不唯一时签发侧不写该
//     claim）时返回 **403**（`control.tenant_scope_missing`），绝不依赖外部错误冒成 500。
//   - **越权**：跨租户/非本人资源统一 `501`… 不，统一 `not_found`（404，不暴露存在性）。
//
// 作用域说明（本期）：这里提供的是**判定原语 + 中间件**，供 `/control/*` 与后续 SaaS 会话/资源
// 入口复用。把「逐入口 owner∈(tenant,user) 全矩阵」铺到既有桌面 controller 上会改动单操作员产品
// 的现有会话归属语义，属 SaaS session_index 落地时的独立工作（设计 §已知未覆盖），不在本切片内。
package admission

import (
	"context"
	"errors"

	"github.com/morehao/ark-iam/contract"
)

// ErrTenantScopeMissing 表示令牌没有 tenant_id（需租户的接口应据此回 403，而非 500）。
var ErrTenantScopeMissing = errors.New("admission: 令牌缺少 tenant_id（需租户接口）")

// RequireTenant 判定该身份可用于「需租户的接口」：tenant_id 非空。
// 空身份或空 tenant_id → ErrTenantScopeMissing。
func RequireTenant(id *contract.Identity) error {
	if id == nil || id.TenantID == "" {
		return ErrTenantScopeMissing
	}
	return nil
}

// FromContext 是 RequireTenant 的 ctx 版本：从已验证身份（`contract.IdentityFrom`）取租户。
func FromContext(ctx context.Context) error {
	return RequireTenant(contract.IdentityFrom(ctx))
}

// OwnerMatches 判定 `(tenantID, userID)` 是否等于本身份的作用域（资源归属校验）。
//
// 用于「带资源/会话 id 的入口」：resource.owner 必须 ∈ (identity.TenantID, identity.UserID)。
// 任一不等即越权 → 调用方应回 `not_found`（不暴露资源是否存在）。
func OwnerMatches(id *contract.Identity, tenantID, userID string) bool {
	if id == nil {
		return false
	}
	return id.TenantID != "" && id.TenantID == tenantID && id.UserID == userID
}
