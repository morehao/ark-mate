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
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/dto/dtoidentity"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/glog"
)

// Onboarder 是「把已验证身份幂等开通到平台」的窄端口（生产传 control/identitymap.Service）。
//
// 定义在控制器本地（而非 import internal/control）有两个好处：① 控制器不耦合具体编排实现；
// ② 测试可传桩。nil = 不开通（桌面本地态 / 纯投影场景）。
type Onboarder interface {
	OnboardErr(ctx context.Context, id *contract.Identity) error
}

// ControlCtr 控制面身份面控制器。
type ControlCtr interface {
	Me(c *gin.Context)
}

var _ ControlCtr = (*controlCtr)(nil)

type controlCtr struct {
	// onboarder 冷路径开通（nil = 跳过）。`/control/me` 是已登录用户的高频读取面，但也正好是
	// 「首请求即触发开通」的天然落点——不在鉴权中间件热路径里写库（守住 G-T2），放在这条已认证的
	// 读面里做一次幂等 upsert，失败只记日志不影响本响应（开通失败不该让「看自己是谁」也失败）。
	onboarder Onboarder
}

// NewControlCtr 构造 ControlCtr。onboarder 可为 nil（不开通）。
func NewControlCtr(onboarder Onboarder) ControlCtr { return &controlCtr{onboarder: onboarder} }

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
	// 冷路径幂等开通：把身份落到平台映射（已登录用户、低频；不阻塞响应——失败仅记日志）。
	if h.onboarder != nil {
		if err := h.onboarder.OnboardErr(c.Request.Context(), id); err != nil {
			glog.Warnf(c.Request.Context(), "[ctrcontrol] 身份开通失败 user=%s tenant=%s: %v", id.UserID, id.TenantID, err)
		}
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
