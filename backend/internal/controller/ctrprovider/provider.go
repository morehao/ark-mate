// Package ctrprovider 承载provider 档案控制器（密钥只写不读回）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrprovider

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/service/svcprovider"
	"github.com/morehao/ark-mate/pkg/code"
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
// 两个字段分工是刻意的：
//
//	actions —— 档案面四动作（增删改查），装配侧交出（今天 = `*manager.Service`）；
//	probe   —— 自检用例，**本控制器自己造**（`svcprovider.NewProbe(archives, catalog)`）。
//
// 为什么自检不跟档案四动作走同一条转发：它的**实现**已经搬进 `service/svcprovider`（P4a），
// 而档案四条还在 `manager` 里。把两者绑成一个字段，就等于让"实现搬到哪一层"重新变成不可见的。
type providerCtr struct {
	actions svcprovider.ProviderSvc
	probe   svcprovider.Prober
}

// NewProviderCtr 构造 ProviderCtr：交出档案四动作 + 自检的两个事实端口，拿回一个控制器。
//
// `actions` / `archives` / `catalog` 都由装配侧交出（今天分别是 `*manager.Service`、
// `*manager.Service.ProviderPlane()`、`*manager.Service`），而**自检用例在这里建成**——
// 除控制器外的层不许引用 `service/svc*`（`internal/archtest/svc_layer_consumer_test.go` 钉着）。
//
// 三个入参恰是三个端口，没有第四个"顺便给一份 Service"的口子：自检只用到供应商域的读面
// 与模型目录的读面，多给一份 Service 就等于把用例层重新绑回实现。
func NewProviderCtr(actions svcprovider.ProviderSvc, archives svcprovider.Ports, catalog svcprovider.Catalog) ProviderCtr {
	return &providerCtr{actions: actions, probe: svcprovider.NewProbe(archives, catalog)}
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
	var req dtoprovider.ProviderCreateReq
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
	var patch dtoprovider.ProviderPatchReq
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
	var req dtoprovider.ProviderProbeReq
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
