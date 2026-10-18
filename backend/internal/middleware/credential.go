package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/credential"
	"github.com/morehao/ark-mate/internal/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

const bearerPrefixLen = len("bearer ")

// Credential 凭据鉴权中间件（M1-1）：要求请求携带具备 want 面的凭据。
//
// 与旧 Token 中间件的两处差别：
//  1. **scope 化**：REST 面只认具备 rest 面的凭据；桌面态的 streamToken 打不到 REST
//     （V15 判据：REST 被拒 401、事件流可用）；
//  2. **不再接受 `?token=`**：URL 里的令牌会同时进浏览器历史、反代/网关日志与 Referer 头。
//     事件流那条通道原本只能靠它（浏览器无法为 WS 设握手头），现在改走**带内首帧 attach**
//     （见 controller/ctrws 的握手），于是它已无存在理由——这是 M1-1 的破坏性调整。
//
// 空凭据表 = 不鉴权（历史默认行为，仅绑回环时使用）：此时中间件直接放行，
// 装配处即使挂上也不会误伤（真正的判定是 credential.Set.Required）。
func Credential(creds *credential.Set, want credential.Scope) gin.HandlerFunc {
	if !creds.Required() {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if creds.Authorize(bearerToken(c), want) {
			c.Next()
			return
		}
		// 401 需要一个非 200 的 HTTP 状态（客户端/代理不经解析信封即可识别未授权），
		// 但信封本身仍由 golib 单点构造：Abort 负责中断后续 handler，FailWithStatus
		// 负责「换状态码 + 拼信封」。
		c.Abort()
		gincontext.FailWithStatus(c, http.StatusUnauthorized,
			code.Error(code.Unauthorized, "未授权：缺少或无效的访问令牌（Authorization: Bearer <token>）"))
	}
}

// bearerToken 取 `Authorization: Bearer <token>`（方案名大小写不敏感、允许多余空白）；无则空串。
// 只认请求头——令牌不进 URL（见 Credential 的说明）。
func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if len(h) <= bearerPrefixLen || !strings.EqualFold(h[:bearerPrefixLen], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[bearerPrefixLen:])
}
