// ——— 模型面控制器（运行时目录 + 平台清单写面）———
//
// 2026-10-20：provider 侧的控制器（`ProviderCtr`）并入本包后，包名沿用 `ctrmodel`——provider
// 与 model 同属一个域（`domain/provider`），但两个控制器接口仍分开：`ProviderCtr`（档案 / 自检）
// 与 `ModelCtr`（目录 / 清单）。
//
// 控制器只依赖 internal/app/* 的窄端口与**依赖事实**（清单载体、供应商域平面），不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrmodel

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/contract/dtomodel"
	"github.com/morehao/ark-mate/internal/app/model"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ModelCtr 模型面控制器：运行时派生的目录（读）+ 平台清单的写面（CRUD + 同步）。
type ModelCtr interface {
	List(c *gin.Context)
	Catalog(c *gin.Context)
	Create(c *gin.Context)
	Sync(c *gin.Context)
	Update(c *gin.Context)
	Delete(c *gin.Context)
}

var _ ModelCtr = (*modelCtr)(nil)

// modelCtr 处理模型目录 REST（GET /models）。
//
// 两个字段都是装配根构造、由构造函数注入的用例：
//
//	declared —— 平台清单的**写面**（`model.NewDeclared`，P4a 第五刀）
//	svc      —— 读清单 / 运行时目录 / 同步（装配点交出）
//
// 底座由构建期固定（v19），"问哪个底座"不是一个参数。
type modelCtr struct {
	svc      model.ModelSvc
	declared model.DeclaredSvc
}

// NewModelCtr 构造 ModelCtr（只收**已构造的两个用例**：写面与读/目录/同步面）。
//
// 除控制器之外的层不许引用 `service/svc*`（`internal/archtest/svc_layer_consumer_test.go`）。
func NewModelCtr(svc model.ModelSvc, declared model.DeclaredSvc) ModelCtr {
	return &modelCtr{svc: svc, declared: declared}
}

// List 返回模型读面：**平台清单 + 聚合生效结论 + 逐 runtime 机器面明细（sync[]）**。
//
// 参数：`?runtime=<id>`（只看那一个 runtime 的机器面明细）、
// `?refresh=1`（保留参数以兼容既有调用；读面本身**无副作用**，不重跑同步）。
func (h *modelCtr) List(c *gin.Context) {
	resp, err := h.svc.ModelsView(c.Request.Context(), c.Query("runtime"), c.Query("refresh") == "1")
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Catalog 返回**运行时派生**的模型目录（会话级模型选择器的数据来源）。
//
// 它与 `List`（平台声明）刻意分开：这里返回的 `options[].values[].value` 是底座的
// **不透明串**，必须原样回传（形态由各 adapter 自己声明，见 `pkg/runtime/model_value.go`：
// dsh 是 `JSON.stringify([provider,model])`，别的底座可能是 `provider/model`），前端不得解析或拼接。
func (h *modelCtr) Catalog(c *gin.Context) {
	resp, err := h.svc.ModelCatalog(c.Request.Context())
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Sync 重新同步模型清单（`POST /models/sync`；`{runtime?}` 空 = 全部已注册 runtime）。
//
// 成功路径只回逐 runtime 的结论——此刻拿不到 `advertised`，**不许假装已生效**。
func (h *modelCtr) Sync(c *gin.Context) {
	var req dtomodel.ModelSyncReq
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
			return
		}
	}
	resp, err := h.svc.SyncModels(c.Request.Context(), req.Runtime)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Create 新建一条模型条目（同 `(provider_id, model_id)` 已存在 → 40901 `model_exists`）。
func (h *modelCtr) Create(c *gin.Context) {
	var req dtomodel.ModelCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.declared.CreateModel(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Update 覆盖一条模型条目（指针更新；不存在 → 40401 `model_not_found`）。
func (h *modelCtr) Update(c *gin.Context) {
	var patch dtomodel.ModelPatchReq
	if err := c.ShouldBindJSON(&patch); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.declared.UpdateModel(c.Request.Context(), c.Param("id"), patch)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Delete 删除一条模型条目（幂等；若它是默认模型则**连带清空**并在响应里说明）。
func (h *modelCtr) Delete(c *gin.Context) {
	resp, err := h.declared.DeleteModel(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
