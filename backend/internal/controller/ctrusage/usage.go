// Package ctrusage 承载用量统计控制器。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrusage

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/dto/dtousage"
	"github.com/morehao/ark-mate/internal/service/svcusage"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// UsageCtr 用量统计控制器。
type UsageCtr interface {
	Summary(c *gin.Context)
}

var _ UsageCtr = (*usageCtr)(nil)

// UsageHandler 用量统计 REST（M3 首项：GET /usage/summary）。
type usageCtr struct {
	svc svcusage.UsageSvc
}

// NewUsageCtr 构造 UsageCtr。
func NewUsageCtr(svc svcusage.UsageSvc) UsageCtr {
	return &usageCtr{svc: svc}
}

// Summary 聚合用量统计：query from/to（RFC3339 或 YYYY-MM-DD，半开区间）、group_by。
//
// `agent_id` 照读不误（已下线，见 `dtousage.UsageQueryReq.AgentID`）：拦下它的判据在
// manager 侧与 `group_by` 同一处，控制器只做搬运。
func (h *usageCtr) Summary(c *gin.Context) {
	q := dtousage.UsageQueryReq{
		From:    c.Query("from"),
		To:      c.Query("to"),
		GroupBy: c.Query("group_by"),
		AgentID: c.Query("agent_id"),
	}
	resp, err := h.svc.UsageSummary(c.Request.Context(), q)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
