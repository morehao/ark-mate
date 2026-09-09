package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// SearchHandler 处理跨会话消息检索（M3 ②：全局消息搜索 + 锚点跳转）。
type SearchHandler struct {
	svc Service
}

// Messages 消息级检索：GET /search/messages?q=&limit=（q 必填；limit 默认 30、上限 200）。
func (h *SearchHandler) Messages(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		gincontext.Fail(c, gerror.Error{Code: 40001, Msg: "搜索关键词 q 不能为空 [invalid_search_query]"})
		return
	}
	limit := 30
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			gincontext.Fail(c, gerror.Error{Code: 40001, Msg: "limit 必须为正整数 [invalid_search_limit]"})
			return
		}
		limit = n
	}
	resp, err := h.svc.SearchMessages(c.Request.Context(), q, limit)
	if err != nil {
		gincontext.Fail(c, h.svc.RestErr(err))
		return
	}
	gincontext.Success(c, resp)
}
