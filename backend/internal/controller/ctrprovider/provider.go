// Package ctrprovider 承载provider 档案控制器（密钥只写不读回）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrprovider

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/service/svcruntime"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ProviderCtr provider 档案控制器（密钥只写不读回）。
type ProviderCtr interface {
	List(c *gin.Context)
	Create(c *gin.Context)
	Update(c *gin.Context)
	Delete(c *gin.Context)
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
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}

// Create 新建一条 provider 档案（id 已存在 → 40901 `provider_exists`，绝不覆盖）。
func (h *providerCtr) Create(c *gin.Context) {
	var req dtoprovider.ProviderCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateProvider(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Update 覆盖一条已存在的档案（type/model/note 子集更新；key 非 nil 覆盖/清除；
// 不存在 → 40401 `provider_not_found`——**不再 upsert**，见设计 D-4）。
func (h *providerCtr) Update(c *gin.Context) {
	var patch dtoprovider.ProviderPatchReq
	if err := c.ShouldBindJSON(&patch); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.UpdateProvider(c.Request.Context(), c.Param("id"), patch)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Delete 删除一条档案（幂等；被引用 → 40901 `provider_in_use` + 引用名单）。
func (h *providerCtr) Delete(c *gin.Context) {
	resp, err := h.svc.DeleteProvider(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
