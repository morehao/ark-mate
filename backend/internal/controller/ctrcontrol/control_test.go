package ctrcontrol

import (
	"context"
	"encoding/json"
	"errors"
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
	ctr := NewControlCtr(nil)
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
	ctr := NewControlCtr(nil)
	r.GET("/me", ctr.Me)
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无身份应 401，得到 %d", w.Code)
	}
}

// fakeOnboarder 记录 OnboardErr 调用次数与入参。
type fakeOnboarder struct {
	calls int
	gotID *contract.Identity
	err   error
}

func (f *fakeOnboarder) OnboardErr(_ context.Context, id *contract.Identity) error {
	f.calls++
	f.gotID = id
	return f.err
}

// TestMe_TriggersOnboarder 有 onboarder 时，/control/me 触发一次冷路径开通（幂等由 identitymap 保证）。
func TestMe_TriggersOnboarder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ob := &fakeOnboarder{}
	ctr := NewControlCtr(ob)
	r := gin.New()
	r.GET("/me", func(c *gin.Context) {
		id := &contract.Identity{UserID: "u1", TenantID: "t1", PersonID: "p1"}
		c.Request = c.Request.WithContext(contract.WithIdentity(c.Request.Context(), id))
		ctr.Me(c)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d（body=%s）", w.Code, w.Body.String())
	}
	if ob.calls != 1 || ob.gotID == nil || ob.gotID.UserID != "u1" {
		t.Fatalf("应触发一次开通：calls=%d gotID=%+v", ob.calls, ob.gotID)
	}
}

// TestMe_OnboarderErrorDoesNotFailRequest 开通失败不影响 /control/me 响应（只记日志）。
func TestMe_OnboarderErrorDoesNotFailRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ob := &fakeOnboarder{err: errors.New("db down")}
	ctr := NewControlCtr(ob)
	r := gin.New()
	r.GET("/me", func(c *gin.Context) {
		id := &contract.Identity{UserID: "u1", TenantID: "t1"}
		c.Request = c.Request.WithContext(contract.WithIdentity(c.Request.Context(), id))
		ctr.Me(c)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("开通失败不应影响 /me 响应，状态码 %d", w.Code)
	}
}
