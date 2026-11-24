package ctrcontrol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-mate/internal/dto/dtoidentity"
)

// TestMe_InjectsIdentity 验证：中间件把 Identity 写进 ctx 后，/control/me 投影出 DTO。
func TestMe_InjectsIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ctr := NewControlCtr()
	r.GET("/me", func(c *gin.Context) {
		// 模拟 authn 中间件：把身份写进 Request.Context()。
		id := &contract.Identity{
			PersonID: "p1", UserID: "u1", TenantID: "t1", ClientID: "ark_mate_web",
			Scopes: []string{"openid", "profile"},
		}
		c.Request = c.Request.WithContext(contract.WithIdentity(c.Request.Context(), id))
		ctr.Me(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d（body=%s）", w.Code, w.Body.String())
	}
	var env struct {
		Code int                `json:"code"`
		Data dtoidentity.MeResp `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if env.Data.UserID != "u1" || env.Data.TenantID != "t1" || env.Data.PersonID != "p1" {
		t.Fatalf("身份投影不符：%+v", env.Data)
	}
	if len(env.Data.Scopes) != 2 {
		t.Fatalf("scopes 应透传：%+v", env.Data.Scopes)
	}
}

// TestMe_NoIdentityFailsClosed 无身份（中间件没挂/未通过）→ 401，不回空身份。
func TestMe_NoIdentityFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ctr := NewControlCtr()
	r.GET("/me", ctr.Me)
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无身份应 401，得到 %d", w.Code)
	}
}
