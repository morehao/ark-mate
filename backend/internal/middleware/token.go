// Package middleware 承载控制面 HTTP 中间件。
//
// 与 ark-iam 一致：中间件独立成包，路由装配处按需挂载，控制器不感知鉴权细节。
package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

const bearerPrefixLen = len("bearer ")

// Token 单操作员令牌鉴权中间件（A6）：
//   - `Authorization: Bearer <token>`（大小写不敏感、允许多余空白）；
//   - WS/EventSource 等无法设置请求头的场景接受 `?token=<token>`；
//   - 恒定时间比较（避免逐字符计时侧信道）；失败返回 HTTP 401 + REST 信封（code=code.Unauthorized）。
func Token(token string) gin.HandlerFunc {
	want := []byte(token)
	return func(c *gin.Context) {
		got := ""
		if h := c.GetHeader("Authorization"); h != "" {
			if len(h) > bearerPrefixLen && strings.EqualFold(h[:bearerPrefixLen], "bearer ") {
				got = strings.TrimSpace(h[bearerPrefixLen:])
			}
		}
		if got == "" {
			got = strings.TrimSpace(c.Query("token"))
		}
		if got != "" && subtle.ConstantTimeCompare([]byte(got), want) == 1 {
			c.Next()
			return
		}
		// 401 需要一个非 200 的 HTTP 状态（客户端/代理不经解析信封即可识别未授权），
		// 但信封本身仍由 golib 单点构造：Abort 负责中断后续 handler，FailWithStatus
		// 负责「换状态码 + 拼信封」（v1.32.16 起提供）。此前手拼 gincontext.DtoRender
		// 等于复制一份信封约定，golib 调整字段时本处不会跟着变。
		c.Abort()
		gincontext.FailWithStatus(c, http.StatusUnauthorized,
			code.Error(code.Unauthorized, "未授权：缺少或无效的访问令牌（Authorization: Bearer <FORGE_TOKEN> 或 ?token=）"))
	}
}
