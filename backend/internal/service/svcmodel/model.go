// Package svcmodel 承载**模型清单与目录**面的用例层（ADR-2：从 svcruntime 拆出）。
//
// **两个事实源分得清**：平台的 `models` 表是**声明**（写面在这），运行时派生的目录是**事实**
// （ACP session 投影，只读）。混成一个读数就会让"平台声明了什么"与"底座广告了什么"变成同一句话。
//
// **P4a 正文第五刀（写面）**：声明的**写面**（`CreateModel`/`UpdateModel`/`DeleteModel`）的
// 实现从 `manager/model_list.go` 搬进本包（`declared.go` 的 `Declared`）。读面（`ListModels`、
// `ModelsView`）与运行时目录/同步面（`ModelCatalog`/`SyncModels`）仍在过渡期的 `manager`：
// 那一半的判据要**执行面**（活连接的 session 投影、spawn 采样），它随 `Orchestrator` 在 P5 一起
// dissolve——所以这一刀先收"不碰执行面"的那半边，接口也跟着切成两片。
package svcmodel

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
)

// DeclaredSvc 平台模型清单的**写面**（三条），实现是本包的 `Declared`（`declared.go`）。
//
// 读面（`ListModels`）刻意**不在**这个接口上：它在实现搬过来之前，写面不该被迫连带引用一份
// 还没搬的读数（`svcprovider` 那一刀的同一条口径：接口按**面**切，不按领域切）。
type DeclaredSvc interface {
	// CreateModel 新建一条模型条目（`(provider_id, model_id)` 唯一 → 40901；
	// provider 不存在 → 40401；请求体带 `runtime` → 40001）。
	CreateModel(ctx context.Context, req dtomodel.ModelCreateReq) (*dtomodel.ModelResp, error)
	// UpdateModel 覆盖一条模型条目（指针更新；`is_default` 写的是 **meta 的单一默认**）。
	UpdateModel(ctx context.Context, id string, patch dtomodel.ModelPatchReq) (*dtomodel.ModelResp, error)
	// DeleteModel 删除一条模型条目（幂等；**允许删**，若它是默认则连带清空并在响应里说明）。
	DeleteModel(ctx context.Context, id string) (*dtomodel.ModelDeleteResp, error)
}

// ModelSvc 模型面的**过渡期复合接口**：声明清单的读面 + 运行时目录 + 同步。
//
// 今天由 `*manager.Service` 满足（装配点交出，控制器收下）。它只声明**controller 真正调用**的
// 三个方法——不要为了"看起来完整"把 `ListModels` 再放回来：那个方法没有接口消费方，
// 放进来只会让"这一段还剩什么"变得不可见。
//
// 这一片的归宿是 P4b 第二刀（读面 + 同步面进 `svcmodel`）与 P5（目录面的采样换成执行面的窄端口）。
type ModelSvc interface {
	// ModelsView 返回 `GET /models` 的读面：平台清单 + **聚合**生效结论（effective/effective_at）
	// + 逐 runtime 机器面明细（sync[]）。它是**无副作用**的（读上一次同步的持久结论）。
	ModelsView(ctx context.Context, runtimeID string, refresh bool) (*dtomodel.ModelsResp, error)
	// ModelCatalog 返回**运行时派生**的模型目录（ACP session config options 投影）。
	// source=session|resume|cache|none 标注取样来源。它是**事实**（运行时广告什么）；
	// 平台声明的那份是 `models` 表。
	ModelCatalog(ctx context.Context) (*dtomodel.ModelCatalogResp, error)
	// SyncModels 重新同步（运行时不变量的唯一收敛入口；空 = 全部已注册 runtime）。
	// 成功路径**不回带 advertised**（那要新进程才拿得到），逐 runtime 如实回结论。
	SyncModels(ctx context.Context, runtimeID string) (*dtomodel.ModelSyncResp, error)
}
