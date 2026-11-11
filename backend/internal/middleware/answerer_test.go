package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestAnswererToken 令牌强校验：缺头/错值 → 401；对值 → 放行；未配置（空串）→ no-op 放行。
//
// 钉的是「/elicitations/ask 是本机回环上的一个 POST 面，回环不等于安全」这条（设计 §8.1/§10.5）：
// 令牌是 OriginPolicy 之外的 defense-in-depth，缺了它任何本机进程都能伪造 answerer 提问。
func TestAnswererToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newEngine := func(expected string) *gin.Engine {
		r := gin.New()
		r.POST("/ask", AnswererToken(expected), func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"code": 0})
		})
		return r
	}
	do := func(r *gin.Engine, token string) int {
		req := httptest.NewRequest(http.MethodPost, "/ask", nil)
		if token != "" {
			req.Header.Set(AnswererTokenHeader, token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// 配置了令牌：缺头拒绝。
	if code := do(newEngine("secret-token"), ""); code != http.StatusUnauthorized {
		t.Fatalf("缺令牌头应 401，got %d", code)
	}
	// 错值拒绝。
	if code := do(newEngine("secret-token"), "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("错令牌应 401，got %d", code)
	}
	// 对值放行。
	if code := do(newEngine("secret-token"), "secret-token"); code != http.StatusOK {
		t.Fatalf("对令牌应放行 200，got %d", code)
	}
	// 未配置（空串）→ no-op 放行（测试路径 / 未注入令牌的部署）。
	if code := do(newEngine(""), ""); code != http.StatusOK {
		t.Fatalf("未配置令牌应 no-op 放行 200，got %d", code)
	}
}
