package ctrcontrol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-mate/internal/dto/dtoidentity"
)

// TestTenantsSwitch_ReturnsOIDCModeAndCurrentTenant 验证：带身份 → mode=oidc + 当前租户为候选，
// 且原样回显请求的 tenantId（服务端不改令牌，只给前端信息）。
func TestTenantsSwitch_ReturnsOIDCModeAndCurrentTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ctr := NewControlCtr(nil)
	r.POST("/switch", func(c *gin.Context) {
		id := &contract.Identity{UserID: "u1", TenantID: "t1"}
		c.Request = c.Request.WithContext(contract.WithIdentity(c.Request.Context(), id))
		ctr.TenantsSwitch(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/switch", strings.NewReader(`{"tenantId":"t2"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d（body=%s）", w.Code, w.Body.String())
	}
	var env struct {
		Code int                           `json:"code"`
		Data dtoidentity.TenantsSwitchResp `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if env.Data.Mode != "oidc" {
		t.Fatalf("mode 应为 oidc，得到 %q", env.Data.Mode)
	}
	if env.Data.CurrentTenantID != "t1" {
		t.Fatalf("currentTenantId 应为 t1，得到 %q", env.Data.CurrentTenantID)
	}
	if env.Data.RequestedTenantID != "t2" {
		t.Fatalf("requestedTenantId 应回显 t2，得到 %q", env.Data.RequestedTenantID)
	}
	if len(env.Data.Tenants) != 1 || env.Data.Tenants[0].TenantID != "t1" {
		t.Fatalf("骨架期候选应含当前租户 t1：%+v", env.Data.Tenants)
	}
}

// TestTenantsSwitch_NoIdentityFailsClosed 无身份 → 401。
func TestTenantsSwitch_NoIdentityFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ctr := NewControlCtr(nil)
	r.POST("/switch", ctr.TenantsSwitch)
	req := httptest.NewRequest(http.MethodPost, "/switch", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无身份应 401，得到 %d", w.Code)
	}
}

// TestTenantsSwitch_EmptyBodyNoTenant 空 body 且 identity 无 tenant_id → 候选为空、不报错
// （RequireTenant 中间件在路由层拦 403；控制器本身对空 tenant 只给空候选，不 panic）。
func TestTenantsSwitch_EmptyBodyNoTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ctr := NewControlCtr(nil)
	r.POST("/switch", func(c *gin.Context) {
		id := &contract.Identity{UserID: "u1"} // 无 TenantID
		c.Request = c.Request.WithContext(contract.WithIdentity(c.Request.Context(), id))
		ctr.TenantsSwitch(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/switch", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("空 body 应仍 200（控制器不判 403），得到 %d", w.Code)
	}
	var env struct {
		Data dtoidentity.TenantsSwitchResp `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data.Tenants) != 0 {
		t.Fatalf("无 tenant_id 时候选应为空：%+v", env.Data.Tenants)
	}
}
