package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ModelsHandler 处理模型目录 REST（设计文档 §5：GET /models）。
type ModelsHandler struct {
	svc Service
}

// List 返回模型目录：?provider=<type> 或 ?agent=<id> 过滤；两者均空 = 聚合全部受支持类型。
// 三段式降级（live→缓存→快照），响应带 source 标注数据来源。
func (h *ModelsHandler) List(c *gin.Context) {
	resp, err := h.svc.ListModels(c.Request.Context(), c.Query("provider"), c.Query("agent"))
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}
