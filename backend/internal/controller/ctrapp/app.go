// Package ctrapp 承载「应用定义」（一期 A+Slot）控制器（REST `/apps` 六接口）。
//
// 控制器只依赖 `internal/service/svcapp` 的窄端口，不感知持久化/领域实现；错误一律经
// `code.FromError` 落到统一响应信封（{code,requestID,msg,data}）。形状照 `ctrcapability`。
package ctrapp

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/dto/dtoapp"
	"github.com/morehao/ark-mate/internal/service/svcapp"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// AppCtr 应用定义控制器（`/apps` 六接口）。
type AppCtr interface {
	ListApps(c *gin.Context)
	CreateApp(c *gin.Context)
	GetApp(c *gin.Context)
	UpdateApp(c *gin.Context)
	DeleteApp(c *gin.Context)
	SetAppEnabled(c *gin.Context)
	InstallApp(c *gin.Context)
	UninstallApp(c *gin.Context)
	// 应用市场（M-A3）。
	ListMarket(c *gin.Context)
	RefreshMarket(c *gin.Context)
	UpgradeApp(c *gin.Context)
}

var _ AppCtr = (*appCtr)(nil)

// appCtr 是实现：持用例接口 + 市场清单窄端口（可为 nil，表示未装配市场能力）。
type appCtr struct {
	svc    svcapp.AppSvc
	market MarketSource
}

// NewAppCtr 构造 AppCtr：只收**已构造的用例**与市场清单窄端口（装配根建一次）。
// market 可为 nil（未装配市场能力：ListMarket 回空数组、RefreshMarket no-op）。
func NewAppCtr(svc svcapp.AppSvc, market MarketSource) AppCtr {
	return &appCtr{svc: svc, market: market}
}

// ListApps 列出全部应用（含禁用；列表不返回 settings_schema 正文，只带 has_page 派生位）。
func (h *appCtr) ListApps(c *gin.Context) {
	items, err := h.svc.ListApps(c.Request.Context())
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}

// CreateApp 新建一个应用定义：body = 应用定义（`id` 必填）。id 已存在 → app_exists(40901)；
// 校验/白名单不过 → app_invalid(40001)，零写入。
func (h *appCtr) CreateApp(c *gin.Context) {
	var req dtoapp.AppWriteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.WriteApp(c.Request.Context(), "", req, true)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// GetApp 读一条应用详情（含 settings_schema 全量）；不存在 → app_not_found(40401)。
func (h *appCtr) GetApp(c *gin.Context) {
	resp, err := h.svc.GetApp(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// UpdateApp 覆盖写一个应用定义：**只改 body 里出现的键**（未出现的保留原值，null 删键）；
// 路径 id 与 body id 不一致 → 40000；CAS 不中 → definition_changed(40901)。
func (h *appCtr) UpdateApp(c *gin.Context) {
	var req dtoapp.AppWriteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.WriteApp(c.Request.Context(), c.Param("id"), req, false)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// DeleteApp 删除一个应用定义（幂等：不存在 → deleted:false 且 code=0）。
func (h *appCtr) DeleteApp(c *gin.Context) {
	resp, err := h.svc.DeleteApp(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// SetAppEnabled 启停一个应用（禁用只影响新选择，不影响已建会话）。
func (h *appCtr) SetAppEnabled(c *gin.Context) {
	var req dtoapp.AppToggleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.SetAppEnabled(c.Request.Context(), c.Param("id"), req.Enabled)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// InstallApp 从本地来源目录安装一个应用包（POST /apps/install）。
//
// body: {dir, enable}。装包复用 apppkg 校验（清单/完整性/min core version），enable=true 时装完即启用。
// 未接运行期能力 → app_runtime_unavailable；包不合法/被篡改/版本不兼容 → app_invalid。
func (h *appCtr) InstallApp(c *gin.Context) {
	var req dtoapp.AppInstallReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.InstallApp(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// UninstallApp 卸载一个应用（DELETE /apps/:id/uninstall）：先停运行期（dispose+停 host）+ 清包目录，再删定义。幂等。
func (h *appCtr) UninstallApp(c *gin.Context) {
	resp, err := h.svc.UninstallApp(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
