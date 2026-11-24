// Package identitymap 是控制面 SaaS 编排的**身份映射**关注点（L7，决策 v38）。
//
// 职责：把已验证的 `contract.Identity`（ark-iam 令牌身份）落到平台侧 `control_tenants`/`control_users`
// ——即「懒建租户平台属性 + 幂等开通用户映射」。它是**唯一的开通入口**：首次见到某
// `(tenant_id, user_id)` 时落一行，重复登录复用（验收 #6）。
//
// 关键口径（DM-2 / C1，设计 `.dsh/docs/specs/2026-11-24-ark-iam-rp-integration-design.md`）：
//   - **身份真源唯一 = ark-iam**：本包**不判登录资格**、不校验密码；只把令牌 claim 投影成平台行。
//   - **平台行是「映射 + 开通快照」，不是所有权真相**：鉴权与资源归属读实时令牌，不读这些行。
//   - **tenant_id 来自令牌**：为空（ark-iam 自然人→租户映射不唯一）时**不写**平台行——需租户的接口
//     由上层返回 403（本包不替它决定 HTTP），这里只跳过开通。
//
// 本包不 import `middleware/authn`（那是 L8 入站带，L7 不可反向 import）：入参直接用外部
// `contract.Identity`（来自 ark-iam SDK，外部依赖，非本仓层级）。
package identitymap

import (
	"context"
	"time"

	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/persistence/store"
)

// Store 是本包依赖的持久层窄端口（生产传 *store.Store；测试可替身）。
// 只暴露开通所需方法，避免把整个 store 塞进编排。
type Store interface {
	EnsureControlTenant(ctx context.Context, in store.ControlTenantUpsert) error
	UpsertControlUser(ctx context.Context, in store.ControlUserUpsert) error
}

// Service 是身份映射/开通编排。
type Service struct {
	store Store
	now   func() time.Time
}

// New 构造 Service。now 为 nil 时用 time.Now（测试注入固定时钟）。
func New(st Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: st, now: now}
}

// OnboardResult 报告一次开通的结果（供审计/测试）。
type OnboardResult struct {
	// Onboarded 是否落库（tenant_id 为空时 false —— 未开通，需租户接口由上层判 403）。
	Onboarded bool
	// TenantID / UserID 本次身份的作用域（原样回显，便于记账）。
	TenantID string
	UserID   string
}

// Onboard 把一枚已验证身份幂等开通到平台。
//
//   - tenant_id 为空 → 不写库、返回 Onboarded=false（不落空租户行，DM-2/C1）；
//   - 否则 ensure 租户（只刷 updated_at，不覆盖运维配的 isolation/quota）+ 幂等 upsert 用户行。
//
// 该方法是 O(2) 次写（tenant ensure + user upsert），放在登录/首请求的**冷路径**调用即可。
func (s *Service) Onboard(ctx context.Context, id *contract.Identity) (OnboardResult, error) {
	if id == nil {
		return OnboardResult{}, nil
	}
	if id.TenantID == "" {
		return OnboardResult{Onboarded: false, UserID: id.UserID}, nil
	}
	nowMilli := s.now().UnixMilli()

	if err := s.store.EnsureControlTenant(ctx, store.ControlTenantUpsert{
		TenantID: id.TenantID,
		NowMilli: nowMilli,
	}); err != nil {
		return OnboardResult{}, err
	}

	userType := model.ControlUserTypeNormal
	if id.IsMachine {
		userType = model.ControlUserTypeService
	}
	if err := s.store.UpsertControlUser(ctx, store.ControlUserUpsert{
		TenantID:  id.TenantID,
		UserID:    id.UserID,
		PersonID:  id.PersonID,
		UserType:  userType,
		ActedAt:   nowMilli,
		CreatedAt: nowMilli,
	}); err != nil {
		return OnboardResult{}, err
	}

	return OnboardResult{Onboarded: true, TenantID: id.TenantID, UserID: id.UserID}, nil
}

// OnboardErr 是 Onboard 的 error-only 适配（供只关心成败的调用方，如控制器冷路径开通）。
// 语义与 Onboard 完全一致；tenant_id 为空等「跳过」情形返回 nil（跳过不是错误）。
func (s *Service) OnboardErr(ctx context.Context, id *contract.Identity) error {
	_, err := s.Onboard(ctx, id)
	return err
}
