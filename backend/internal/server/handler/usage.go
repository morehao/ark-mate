package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/dto/dtousage"
	"github.com/morehao/ark-forge/internal/service/svcusage"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// UsageHandler 用量统计 REST（M3 首项：GET /usage/summary）。
type UsageHandler struct {
	svc svcusage.UsageSvc
}

// Summary 聚合用量统计：query from/to（RFC3339 或 YYYY-MM-DD，半开区间）、agent_id、group_by。
func (h *UsageHandler) Summary(c *gin.Context) {
	q := dtousage.UsageQueryReq{
		From:    c.Query("from"),
		To:      c.Query("to"),
		AgentID: c.Query("agent_id"),
		GroupBy: c.Query("group_by"),
	}
	resp, err := h.svc.UsageSummary(c.Request.Context(), q)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}
