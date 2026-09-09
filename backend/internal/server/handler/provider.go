package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// ProviderHandler 处理 Provider 档案 REST（P2 注入面：密钥只写不读回）。
type ProviderHandler struct {
	svc Service
}

// List 列出 provider 档案（key 绝不读回，只给 has_key）。
func (h *ProviderHandler) List(c *gin.Context) {
	items, err := h.svc.ListProviders()
	if err != nil {
		gincontext.Fail(c, gerror.Error{Code: 1, Msg: err.Error()})
		return
	}
	gincontext.Success(c, items)
}

// Update upsert provider 档案（type/model 子集更新；key 非 nil 覆盖/清除）。
func (h *ProviderHandler) Update(c *gin.Context) {
	var patch ProviderPatchReq
	if err := c.ShouldBindJSON(&patch); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.UpdateProvider(c.Request.Context(), c.Param("id"), patch)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// Models 返回某 provider 档案的可选模型目录（live 优先，兜底快照；密钥绝不读回）。
func (h *ProviderHandler) Models(c *gin.Context) {
	resp, err := h.svc.ListProviderModels(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}
