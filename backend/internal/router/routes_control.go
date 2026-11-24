package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrcontrol"
	"github.com/morehao/ark-mate/internal/middleware/authn"
)

// registerControl 控制面身份面（决策 v38：ark-iam RP 认证）。
//
// 鉴权：`/control/me` 需要已认证身份。`auth == nil`（桌面本地态降级 / 测试夹具）时**不挂**鉴权
// 中间件——此时控制器自身对「取不到身份」返回 401（fail-closed），不影响路由存在性。
// `auth != nil` 时把验签中间件挂在该组上：未带 Bearer / 令牌无效 → 401，密钥集不可用 → 503。
func registerControl(v1 *gin.RouterGroup, ctr ctrcontrol.ControlCtr, auth *authn.Authenticator) {
	group := v1.Group("/control")
	if auth != nil {
		group.Use(auth.Middleware())
	}
	group.GET("/me", ctr.Me)
}
