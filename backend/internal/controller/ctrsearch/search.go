// Package ctrsearch 承载跨会话消息检索控制器。
//
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
//
// P4a 正文第四十三刀：消息检索是**读模型**（无请求校验、无错误码出口、无响应装配，实现与域的
// 锁和状态绑死），按本仓口径由控制器直接持有**会话域平面**——用例层再包一层只会得到一行转发。
package ctrsearch

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/service/svcsearch"
	"github.com/morehao/ark-mate/internal/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// SearchCtr 跨会话消息检索控制器。
type SearchCtr interface {
	Messages(c *gin.Context)
}

var _ SearchCtr = (*searchCtr)(nil)

// SearchHandler 处理跨会话消息检索（M3 ②：全局消息搜索 + 锚点跳转）。
type searchCtr struct {
	svc svcsearch.SearchSvc
}

// NewSearchCtr 构造 SearchCtr（只收**已构造的用例**；v3.1 §3.2.1：控制器不 import domain）。
func NewSearchCtr(svc svcsearch.SearchSvc) SearchCtr {
	return &searchCtr{svc: svc}
}

// Messages 消息级检索：GET /search/messages?q=&limit=（q 必填；limit 默认 30、上限 200）。
func (h *searchCtr) Messages(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		gincontext.Fail(c, code.Error(code.InvalidRequest, "搜索关键词 q 不能为空 [invalid_search_query]"))
		return
	}
	limit := 30
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			gincontext.Fail(c, code.Error(code.InvalidRequest, "limit 必须为正整数 [invalid_search_limit]"))
			return
		}
		limit = n
	}
	resp, err := h.svc.Messages(c.Request.Context(), q, limit)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
