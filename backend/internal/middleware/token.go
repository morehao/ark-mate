// Package middleware 承载控制面 HTTP 中间件。
//
// 与 ark-iam 一致：中间件独立成包，路由装配处按需挂载，控制器不感知鉴权细节。
package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// CodeUnauthorized 未授权错误码（REST 信封 code；HTTP 状态同时为 401，
// 便于前端与反向代理统一处理）。
const CodeUnauthorized = 40101

const bearerPrefixLen = len("bearer ")

// Token 单操作员令牌鉴权中间件（A6）：
//   - `Authorization: Bearer <token>`（大小写不敏感、允许多余空白）；
//   - WS/EventSource 等无法设置请求头的场景接受 `?token=<token>`；
//   - 恒定时间比较（避免逐字符计时侧信道）；失败返回 HTTP 401 + REST 信封（code=40101）。
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
		c.AbortWithStatusJSON(http.StatusUnauthorized, gincontext.DtoRender{
			Code:      CodeUnauthorized,
			RequestID: gincontext.GetRequestID(c),
			Msg:       "未授权：缺少或无效的访问令牌（Authorization: Bearer <FORGE_TOKEN> 或 ?token=）",
			Data:      gin.H{},
		})
	}
}
