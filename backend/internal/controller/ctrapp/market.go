package ctrapp

// ——— 应用市场控制器（M-A3，`GET /apps/market` / `POST /apps/market/refresh` / `POST /apps/:id/upgrade`）———
//
// 市场清单由 infra 的 `appmarket` 拉取（远端/缓存/内置），而「装态派生」在 svcapp.MarketApps。
// 控制器**不 import infra/appmarket**（分层）：它只认装配根注入的窄端口 `MarketSource`——由装配根把
// `appmarket.Market` 适配成 `[]dtoapp.MarketListing`（翻译 appmarket.Listing → DTO），控制器再交给
// svcapp.MarketApps 合并出装态。这样 appmarket 的结构不跨层泄漏。

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/dto/dtoapp"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// MarketSource 是市场清单读取的窄端口（装配根用 appmarket.Market 适配；nil = 无市场能力）。
type MarketSource interface {
	// List 返回市场清单条目（远端/缓存/内置三级兜底已在其内部完成）。
	List(ctx context.Context) ([]dtoapp.MarketListing, error)
	// Refresh 清空清单缓存，使下次 List 强制重拉（前端「刷新」按钮）。
	Refresh()
}

// ListMarket 返回应用市场清单（合并本地装态派生）。GET /apps/market → data 为**顶层数组**。
//
// 流程：MarketSource.List 拿清单（appmarket 已做远端/缓存/内置兜底）→ svcapp.MarketApps 逐条派生
// installed/enabled/updatable/installable/state → 返回数组。无市场能力（未装配）→ 返回空数组（不报错，页面不白屏）。
func (h *appCtr) ListMarket(c *gin.Context) {
	ctx := c.Request.Context()
	var listings []dtoapp.MarketListing
	if h.market != nil {
		var err error
		listings, err = h.market.List(ctx)
		if err != nil {
			gincontext.Fail(c, code.FromError(err))
			return
		}
	}
	items, err := h.svc.MarketApps(ctx, listings)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}

// RefreshMarket 强制刷新市场清单缓存（POST /apps/market/refresh）。无市场能力 → no-op 成功。
func (h *appCtr) RefreshMarket(c *gin.Context) {
	if h.market != nil {
		h.market.Refresh()
	}
	gincontext.Success(c, gin.H{"refreshed": true})
}

// UpgradeApp 更新已装应用（POST /apps/:id/upgrade）。
//
// 本期（C8 版本治理未接线）**明确返回「接线中」而不假装成功**：更新要重装包 + 走签名/版本治理链，
// 属 C8；这里返 app_runtime_unavailable 并明示「更新能力接线中」，前端据此提示而非误以为已更新。
func (h *appCtr) UpgradeApp(c *gin.Context) {
	gincontext.Fail(c, code.Error(code.NotSupported,
		"更新能力接线中（应用包更新走 C8 版本治理链路，本期未接线）"))
}
