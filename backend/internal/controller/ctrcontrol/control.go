// Package ctrcontrol 是控制面身份面的控制器（`GET /control/me`）。
//
// 决策 v38：桌面与 SaaS 均接 ark-iam。本控制器只做**投影**：把中间件已验证并注入请求上下文的
// `contract.Identity` 转成对外 DTO，不碰持久层、不判登录资格（身份真源唯一 = ark-iam）。
//
// 身份来源：`middleware/authn` 的中间件把 `contract.Identity` 写进 `c.Request.Context()`
// （`contract.WithIdentity`）；本控制器用 `contract.IdentityFrom(c.Request.Context())` 取回——
// 走纯 ctx（不依赖 gin 私有键），与「handler 传 c.Request.Context()」的分层纪律一致。
package ctrcontrol

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/dto/dtoidentity"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ControlCtr 控制面身份面控制器。
type ControlCtr interface {
	Me(c *gin.Context)
}

var _ ControlCtr = (*controlCtr)(nil)

type controlCtr struct{}

// NewControlCtr 构造 ControlCtr。
func NewControlCtr() ControlCtr { return &controlCtr{} }

// Me 返回当前请求已验证身份的投影（`GET /control/me`）。
//
// 需挂在 authn 中间件之后：取不到身份说明中间件没挂或未通过（理论上已被中间件挡成 401，
// 这里再兜一道 fail-closed，返回 401 而不是空身份）。
func (h *controlCtr) Me(c *gin.Context) {
	id := contract.IdentityFrom(c.Request.Context())
	if id == nil {
		gincontext.FailWithStatus(c, http.StatusUnauthorized, code.Error(code.Unauthorized, "未认证"))
		return
	}
	gincontext.Success(c, dtoidentity.MeResp{
		PersonID:  id.PersonID,
		UserID:    id.UserID,
		TenantID:  id.TenantID,
		ClientID:  id.ClientID,
		IsMachine: id.IsMachine,
		Scopes:    id.Scopes,
	})
}
