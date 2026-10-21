// Package ctrmodel 承载供应商域控制器：模型目录 / 平台清单写面（`ModelCtr`）与供应商档案 /
// 连接自检（`ProviderCtr`，2026-10-20 从 `ctrprovider` 并入）。
//
// 控制器只依赖 internal/app/* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrmodel

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/contract/dtomodel"
	"github.com/morehao/ark-mate/internal/app/model"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ProviderCtr provider 档案控制器（密钥只写不读回）。
type ProviderCtr interface {
	List(c *gin.Context)
	Create(c *gin.Context)
	Update(c *gin.Context)
	Delete(c *gin.Context)
	Probe(c *gin.Context)
}

var _ ProviderCtr = (*providerCtr)(nil)

// providerCtr 处理 Provider 档案 REST（P2 注入面：密钥只写不读回）。
//
// 两个字段都是装配根构造、由构造函数注入的用例，两个都是 `service/svcmodel` 包内声明的接口：
//
//	actions —— 档案面四动作（`model.NewProvider(plane)`）；
//	probe   —— 自检（`model.NewProbe(ports, catalog)`）。
//
// 控制器只收已构造的接口；除控制器外的层不许引用 `service/svc*`
// （`internal/archtest/svc_layer_consumer_test.go`）。
type providerCtr struct {
	actions model.ProviderSvc
	probe   model.Prober
}

// NewProviderCtr 构造 ProviderCtr（只收**已构造的两个用例**：档案动作与自检）。
func NewProviderCtr(actions model.ProviderSvc, probe model.Prober) ProviderCtr {
	return &providerCtr{actions: actions, probe: probe}
}

// List 列出供应商（密钥绝不读回，只给 has_key）。
func (h *providerCtr) List(c *gin.Context) {
	items, err := h.actions.ListProviders(c.Request.Context())
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}

// Create 新建一条 provider 档案（id 已存在 → 40901 `provider_exists`，绝不覆盖）。
func (h *providerCtr) Create(c *gin.Context) {
	var req dtomodel.ProviderCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.actions.CreateProvider(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Update 覆盖一条已存在的档案（type/model/note 子集更新；key 非 nil 覆盖/清除；
// 不存在 → 40401 `provider_not_found`——**不再 upsert**，见设计 D-4）。
func (h *providerCtr) Update(c *gin.Context) {
	var patch dtomodel.ProviderPatchReq
	if err := c.ShouldBindJSON(&patch); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.actions.UpdateProvider(c.Request.Context(), c.Param("id"), patch)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Probe 连接自检：本地段无条件（零出网）；上游段仅当请求体带 `{"upstream":true}`
// ——它会把用户密钥发到第三方，必须是用户的一次显式动作，绝不被 GET 或页面加载顺手触发。
func (h *providerCtr) Probe(c *gin.Context) {
	var req dtomodel.ProviderProbeReq
	// 允许空 body（= 只跑本地段）。
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
			return
		}
	}
	resp, err := h.probe.ProbeProvider(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Delete 删除一条档案（幂等；被引用 → 40901 `provider_in_use` + 引用名单）。
func (h *providerCtr) Delete(c *gin.Context) {
	resp, err := h.actions.DeleteProvider(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
