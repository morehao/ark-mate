// Package slo 是控制面 SaaS 编排的**单点登出（SLO）接收端**（L7，决策 v38、设计 §C2/DM-3）。
//
// 职责：接收 ark-iam（OP）经反向通道推来的 `logout_token`，**本地验签**（仅凭 JWKS，不访问
// ark-iam Redis）→ jti 去重 → 撤销本地会话 → 记 jti。校验/去重/撤销顺序的单一实现下沉在
// ark-iam 的 `contract/logout`（`sdk/rp/logout` 是它的别名），本包只做两件本仓特有的事：
//  1. 把 jti 去重**落表**（`control_logout_jtis`，多副本必需，DM-5）；
//  2. 把「撤销本地会话」接成本仓的 `SessionRevoker`（当前无服务端会话登记 → 由调用方注入）。
//
// 关键（C2）：back-channel 是「撤会话」，不是「吊销令牌」；它只覆盖「已打开页面」的即时登出，
// 裸 token 的撤权窗口由 access_token 短 TTL 兜底（见设计 C2-B）。
package slo

import (
	"context"
	"time"

	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-iam/sdk/rp/logout"
)

// JTIStore 是 jti 去重的持久化存储（生产用 store.JTIStore；测试可替身）。
// 与 ark-iam `contract/logout.JTIStore` 同形，便于直接注入 receiver。
type JTIStore interface {
	Seen(ctx context.Context, jti string) bool
	Record(ctx context.Context, jti string, expiresAt time.Time) error
}

// Revoker 是收到合法 logout_token 后撤销本地会话的回调（按 sid）。
//
// 传 nil = 只验签不做本地撤销（无服务端会话登记时的降级：页面侧靠前端静默续期失败跳登录）。
// 设计上「撤销本地会话 = 停该 sid 的 WS fan-out + 让后续请求失效」；本仓会话为每连接持有，
// 尚无集中会话表，故当前默认 nil，集中会话登记随 SaaS session_index 落地时接入。
type Revoker = logout.SessionRevoker

// Receiver 包装 ark-iam 的 logout 接收端，补齐本仓的 jti 落表与撤销回调。
type Receiver struct {
	inner *logout.Receiver
}

// Options 构造 Receiver 的依赖。
type Options struct {
	// KeySource 是与 /control/me 同一份 JWKS 密钥来源（仅验签用，不访问 OP）。
	KeySource logout.JWKS
	// Issuer OP 的 issuer（精确匹配 iss）。
	Issuer string
	// ClientID 本应用 client_id（logout_token aud 必须命中）。
	ClientID string
	// JTIStore jti 去重落表实现（nil → ark-iam 默认内存实现，仅单副本单测可用）。
	JTIStore JTIStore
	// Revoker 撤销本地会话回调（nil → 只验签不撤销）。
	Revoker Revoker
}

// New 构造接收端。issuer/clientID/keySource 缺失时所有请求 400（ark-iam fail-closed）。
func New(opts Options) *Receiver {
	ropts := []logout.Option{
		logout.WithIssuer(opts.Issuer),
		logout.WithClientID(opts.ClientID),
	}
	if opts.KeySource != nil {
		ropts = append(ropts, logout.WithKeySource(opts.KeySource))
	}
	if opts.JTIStore != nil {
		ropts = append(ropts, logout.WithJTIStore(opts.JTIStore))
	}
	if opts.Revoker != nil {
		ropts = append(ropts, logout.WithSessionRevoker(opts.Revoker))
	}
	return &Receiver{inner: logout.NewReceiver(ropts...)}
}

// Handle 处理一枚 logout_token，返回 HTTP 状态码与错误消息（200/400/500）。
//
// 顺序由 ark-iam 实现保证：验签 → jti 去重 → 撤销本地会话 → 成功后记 jti。
// 撤销失败返回 500 且**不记 jti**，让 OP 重投生效。
func (r *Receiver) Handle(ctx context.Context, tokenStr string) (int, string) {
	if r == nil || r.inner == nil {
		return 400, "logout receiver not configured"
	}
	return r.inner.Handle(ctx, tokenStr)
}

// RevokeBySID 是「按会话标识撤销」回调的通用签名（供装配根组合集中会话登记时使用）。
// 当前未接线（无集中会话表），保留为未来挂载点。
type RevokeBySID func(ctx context.Context, sessionID string) error

// RevokerFrom 把「按 sid 撤销」函数适配成 receiver 需要的 `Revoker`（读 claims.SessionID）。
func RevokerFrom(revoke RevokeBySID) Revoker {
	if revoke == nil {
		return nil
	}
	return func(ctx context.Context, claims *contract.LogoutTokenClaims) error {
		if claims == nil || claims.SessionID == "" {
			return nil
		}
		return revoke(ctx, claims.SessionID)
	}
}
