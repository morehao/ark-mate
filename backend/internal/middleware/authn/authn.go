// Package authn 是控制面的**认证接入层**（RP 侧）：把 ark-iam（OP/IdP）签发的 access_token
// 在本地验签成 `contract.Identity`，并以 gin 中间件形式注入请求上下文。
//
// 位置说明：本包落在 `internal/middleware/authn`（L8 入站横切带），不新开顶层目录。
// 原因：`internal/*` 新增顶层目录须在 `archtest.topLevelRoles` + 设计文档 §2.2 登记层带
// （`TestTopLevelDirsHaveRoles` 双向判据），属独立的架构决定；P0 阶段把认证验签作为**入站横切**
// 复用既有 `middleware` 带最贴合。设计见 `.dsh/docs/specs/2026-11-24-ark-iam-rp-integration-design.md`
// （§3 模块划分把认证归属控制面 authn 的关注点，落地位置按本仓分层约束取 middleware 入站带）。
// 未来租户映射/准入/SLO 等控制面模块落地时，消费本包的 verifier 与中间件。
//
// 设计：`.dsh/docs/specs/2026-11-24-ark-iam-rp-integration-design.md`（DM-1/DM-2、C2）。
// 要点：
//   - **复用 ark-iam 做 OP，本进程做 RP**：验签只用 `sdk/rp`（JWKS 预取 + 内存 kid 查表），
//     请求热路径**不访问 ark-iam**（G-T2 / 反目标 2）。
//   - **身份真源唯一 = ark-iam**：本层只验签 + 投影，不判登录资格、不查平台表（DM-2）。
//   - **fail-closed**：密钥集不可用返回 `contract.ErrKeySourceUnavailable`，中间件据此回 503，
//     绝不伪装成 401（否则排障时会把「密钥没取到」误读成「令牌坏了」）。
package authn

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-iam/sdk/rp"
)

// Options 构造 Authenticator 所需的最小事实。
type Options struct {
	// Issuer 是 OP 的 issuer（如 `http://localhost:8081/oidc`）。验签时 `iss` 精确匹配。
	Issuer string
	// Audience 是本 RP 的 OIDC `client_id`（令牌 aud 必须命中它，拒绝其它 client 的令牌）。
	Audience string
	// Keys 是密钥来源；nil 时用 issuer 走 `rp.NewKeys` 网络预取（生产/联调）。
	// 显式注入（如 `rp.NewKeysFromSet`）用于测试与「OP 同进程注入公钥」的部署形态。
	Keys rp.KeySource
}

// Authenticator 是可并发复用的验签器。构造一次，随进程存活。
type Authenticator struct {
	issuer  string
	verify  *rp.Verifier
	closers []func()
}

// New 构造 Authenticator：装配 `rp.Verifier`，必要时预取 JWKS（fail-closed）。
//
// 返回的 Authenticator 用 `Close` 释放密钥集的后台刷新；进程生命周期内常驻。
func New(ctx context.Context, opts Options) (*Authenticator, error) {
	if strings.TrimSpace(opts.Issuer) == "" {
		return nil, fmt.Errorf("authn: issuer 必填")
	}
	if strings.TrimSpace(opts.Audience) == "" {
		return nil, fmt.Errorf("authn: audience(client_id) 必填")
	}

	ks := opts.Keys
	var closers []func()
	if ks == nil {
		keys, err := rp.NewKeys(ctx, opts.Issuer)
		if err != nil {
			// fail-closed：取不到 JWKS 就别把服务放出去（设计 §5「起不来就别暴露」）。
			return nil, fmt.Errorf("authn: 预取 JWKS 失败: %w", err)
		}
		ks = keys
		closers = append(closers, keys.Close)
	}

	v := rp.NewVerifier(ks, rp.WithIssuer(opts.Issuer), rp.WithAudiences(opts.Audience))
	return &Authenticator{issuer: opts.Issuer, verify: v, closers: closers}, nil
}

// Close 释放密钥集后台刷新（幂等由 Keys.Close 保证）。
func (a *Authenticator) Close() {
	if a == nil {
		return
	}
	for _, c := range a.closers {
		c()
	}
}

// Verify 验签一枚裸 access_token，返回 ark-iam 侧的 `contract.Identity`。
//
// 错误分类（调用方据此定 HTTP 状态）：
//   - `contract.ErrKeySourceUnavailable` → 密钥集不可用（应回 503，不是 401）。
//   - 其余（签名/过期/iss/aud/kid/格式）→ 一律视为未认证（401）。
func (a *Authenticator) Verify(ctx context.Context, rawToken string) (*contract.Identity, error) {
	if a == nil || a.verify == nil {
		return nil, contract.ErrKeySourceUnavailable
	}
	return a.verify.Verify(ctx, rawToken)
}

// IsKeySourceUnavailable 报告错误是否属于「密钥集不可用」（→ 503 而非 401）。
func IsKeySourceUnavailable(err error) bool {
	return errors.Is(err, contract.ErrKeySourceUnavailable)
}

// BearerToken 从 Authorization 头提取 Bearer 令牌（大小写不敏感前缀）。
// 返回空串表示未携带。
func BearerToken(authorization string) string {
	const prefix = "bearer "
	if len(authorization) < len(prefix) {
		return ""
	}
	if !strings.EqualFold(authorization[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(authorization[len(prefix):])
}
