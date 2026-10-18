// Package ctrusage 承载用量统计控制器。
//
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
//
// P4a 正文第四十三刀：用量统计是**读模型**（校验、分组口径与响应装配全在域平面里，
// 见 `domain/session/usage.go`），按本仓口径由控制器直接持有**会话域平面**。
package ctrusage

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/service/svcusage"
	"github.com/morehao/ark-mate/internal/dto/dtousage"
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

// NewUsageCtr 构造 UsageCtr（只收**已构造的用例**；v3.1 §3.2.1：控制器不 import domain）。
func NewUsageCtr(svc svcusage.UsageSvc) UsageCtr {
	return &usageCtr{svc: svc}
}

// Summary 聚合用量统计：query from/to（RFC3339 或 YYYY-MM-DD，半开区间）、group_by。
//
// `agent_id` 照读不误（已下线，见 `dtousage.UsageQueryReq.AgentID`）：拦下它的判据在**域平面**里
// 与 `group_by` 同一处（`rejectRemovedUsageDimension`），控制器只做搬运——
// **静默丢弃才是错的**：那会让用户以为「按底座筛选生效了」。
func (h *usageCtr) Summary(c *gin.Context) {
	q := dtousage.UsageQueryReq{
		From:     c.Query("from"),
		To:       c.Query("to"),
		TZOffset: c.Query("tz_offset"),
		GroupBy:  c.Query("group_by"),
		AgentID:  c.Query("agent_id"),
	}
	resp, err := h.svc.Summary(c.Request.Context(), q)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
