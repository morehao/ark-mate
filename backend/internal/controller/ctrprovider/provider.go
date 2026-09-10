// Package ctrprovider 承载provider 档案控制器（密钥只写不读回）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrprovider

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/dto/dtoprovider"
	"github.com/morehao/ark-forge/internal/service/svcruntime"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// ProviderCtr provider 档案控制器（密钥只写不读回）。
type ProviderCtr interface {
	List(c *gin.Context)
	Update(c *gin.Context)
}

var _ ProviderCtr = (*providerCtr)(nil)

// ProviderHandler 处理 Provider 档案 REST（P2 注入面：密钥只写不读回）。
type providerCtr struct {
	svc svcruntime.RuntimeSvc
}

// NewProviderCtr 构造 ProviderCtr。
func NewProviderCtr(svc svcruntime.RuntimeSvc) ProviderCtr {
	return &providerCtr{svc: svc}
}

// List 列出 provider 档案（key 绝不读回，只给 has_key）。
func (h *providerCtr) List(c *gin.Context) {
	items, err := h.svc.ListProviders()
	if err != nil {
		gincontext.Fail(c, gerror.Error{Code: 1, Msg: err.Error()})
		return
	}
	gincontext.Success(c, items)
}

// Update upsert provider 档案（type/model 子集更新；key 非 nil 覆盖/清除）。
func (h *providerCtr) Update(c *gin.Context) {
	var patch dtoprovider.ProviderPatchReq
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
