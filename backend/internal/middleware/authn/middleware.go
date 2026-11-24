package authn

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// identityContextKey 是已验证身份在 gin Keys 里的存取键（入站带内使用）。
// 下游 controller 用 `IdentityFromGin` 取；纯 ctx 层用 `contract.IdentityFrom(ctx)`。
const identityContextKey = "authn.identity"

// Middleware 返回验签 gin 中间件：从 `Authorization: Bearer` 取令牌 → 本地验签 →
// 把 `contract.Identity` 注入请求上下文。放行时同时写入：
//   - `c.Request.Context()`（经 `contract.WithIdentity`），供下传到纯 ctx 层；
//   - gin Keys（`identityContextKey`），供 controller 就地读取。
//
// 错误分类（设计 §5）：
//   - 缺令牌 / 验签失败（签名/过期/iss/aud/kid/格式）→ 401 `code.Unauthorized`；
//   - 密钥集不可用（`contract.ErrKeySourceUnavailable`）→ 503 `code.BadGateway`
//     （**不伪装成 401**：那是「本服务取不到 JWKS」，不是「调用方令牌坏」，排障要能区分）。
func (a *Authenticator) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := BearerToken(c.GetHeader("Authorization"))
		if raw == "" {
			c.Abort()
			gincontext.FailWithStatus(c, http.StatusUnauthorized,
				code.Error(code.Unauthorized, "缺少 Bearer 令牌"))
			return
		}

		id, err := a.Verify(c.Request.Context(), raw)
		if err != nil {
			if IsKeySourceUnavailable(err) {
				c.Abort()
				gincontext.FailWithStatus(c, http.StatusServiceUnavailable,
					code.Error(code.BadGateway, "认证密钥集暂不可用"))
				return
			}
			c.Abort()
			gincontext.FailWithStatus(c, http.StatusUnauthorized,
				code.Error(code.Unauthorized, "令牌无效"))
			return
		}

		// 注入纯 ctx（下传 service 层用）与 gin Keys（controller 就地读）。
		c.Request = c.Request.WithContext(contract.WithIdentity(c.Request.Context(), id))
		c.Set(identityContextKey, id)
		c.Next()
	}
}

// IdentityFromGin 从 gin 上下文取回中间件注入的身份；未注入时返回 nil。
func IdentityFromGin(c *gin.Context) *contract.Identity {
	if c == nil {
		return nil
	}
	v, ok := c.Get(identityContextKey)
	if !ok {
		return nil
	}
	id, _ := v.(*contract.Identity)
	return id
}
