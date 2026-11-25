package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/control/admission"
	"github.com/morehao/ark-mate/internal/control/slo"
	"github.com/morehao/ark-mate/internal/controller/ctrcontrol"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/middleware/authn"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// registerControl 控制面身份面（决策 v38：ark-iam RP 认证）。
//
// 鉴权：`/control/me` 需要已认证身份。`auth == nil`（桌面本地态降级 / 测试夹具）时**不挂**鉴权
// 中间件——此时控制器自身对「取不到身份」返回 401（fail-closed），不影响路由存在性。
// `auth != nil` 时把验签中间件挂在该组上：未带 Bearer / 令牌无效 → 401，密钥集不可用 → 503。
//
// `/control/oidc/bc-logout` 是 back-channel logout 接收端（设计 §C2）：它**自身验签**（用同一套
// JWKS），不走 authn 中间件；`sloRcvr == nil` 时不挂（未启用认证 / 无持久层 jti 表）。
func registerControl(v1 *gin.RouterGroup, ctr ctrcontrol.ControlCtr, auth *authn.Authenticator, sloRcvr *slo.Receiver) {
	group := v1.Group("/control")
	if auth != nil {
		group.Use(auth.Middleware())
	}
	group.GET("/me", ctr.Me)

	// 需租户的接口组（决策 v38、DM-4）：在 authn 之后追加 `admission.RequireTenant`——令牌带
	// tenant_id 才放行，缺则 403（`control.tenant_scope_missing`，用 code.Forbidden）。放在独立子组，
	// 因为 `/control/me` 本身**不要求** tenant（设计：含 tenant_id 缺失的令牌此处照常返回空 tenantId）。
	tenantGroup := group.Group("")
	tenantGroup.Use(requireTenant())
	// 租户切换（设计 §接口设计）：返回候选租户 + 触发前端重走 OIDC，不在服务端改令牌。
	tenantGroup.POST("/tenants/switch", ctr.TenantsSwitch)

	// SLO 接收端：独立、免 authn 中间件（注册在 v1 本体上，不继承本组的 authn 中间件）。
	// 路由**恒定注册**（路由表与接线无关，便于契约守卫稳定）；receiver 为 nil（未启用认证 / 无持久层）
	// 时处理器返回 400——该路径在这些部署下本就不该被调用。
	v1.POST("/control/oidc/bc-logout", bcLogoutHandler(sloRcvr))
}

// requireTenant 是「需租户接口」的准入中间件（决策 v38、DM-4）：把控制面判定原语
// `admission.RequireTenant` 适配成 gin 中间件。取不到身份（中间件没挂/未通过）→ 401；
// 身份缺 tenant_id → 403 `control.tenant_scope_missing`。
//
// 放在 router 包：control/admission 是 L7 编排带，不能 import gin（gin 围栏只放行 router/controller/
// middleware）；而 router 是 L9 交付带，可向上 import admission 并持有 gin，正好做这层薄适配。
func requireTenant() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := authn.IdentityFromGin(c)
		if id == nil {
			gincontext.FailWithStatus(c, http.StatusUnauthorized, code.Error(code.Unauthorized, "未认证"))
			c.Abort()
			return
		}
		if err := admission.RequireTenant(id); err != nil {
			gincontext.FailWithStatus(c, http.StatusForbidden, code.Error(code.Forbidden, "缺少租户作用域"))
			c.Abort()
			return
		}
		c.Next()
	}
}

// bcLogoutHandler 把 slo.Receiver.Handle 适配成 gin 处理器。
//
// 顺序/状态码由 slo.Receiver.Handle（ark-iam 实现）决定：验签失败 400、撤销失败 500、成功 200。
// 成功时**不回 body**（与 ark-iam contract/logout.ServeHTTP 一致）。
func bcLogoutHandler(rcvr *slo.Receiver) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rcvr == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "logout receiver not configured"})
			return
		}
		token := c.PostForm("logout_token")
		status, msg := rcvr.Handle(c.Request.Context(), token)
		if status == http.StatusOK {
			c.Status(http.StatusOK)
			return
		}
		// 错误回 JSON 信封风格但保持 SLO 语义：{error}（对齐 ark-iam writeError）。
		c.JSON(status, gin.H{"error": msg})
	}
}
