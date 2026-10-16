// Package svcmodel 定义**模型清单与目录**面的服务端口（ADR-2：从 svcruntime 拆出）。
//
// **两个事实源分得清**：`ListModels` 是**平台声明**的那份（`models` 表，写面在这），
// `ModelCatalog` 是**运行时广告**的那份（ACP session 投影，只读）。
package svcmodel

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
)

// ModelSvc 模型清单与目录面的服务端口。
type ModelSvc interface {
	// ModelsView 返回 `GET /models` 的读面：平台清单 + **聚合**生效结论（effective/effective_at）
	// + 逐 runtime 机器面明细（sync[]）。它是**无副作用**的（读上一次同步的持久结论）。
	ModelsView(ctx context.Context, runtimeID string, refresh bool) (*dtomodel.ModelsResp, error)
	// ModelCatalog 返回**运行时派生**的模型目录（ACP session config options 投影）。
	// source=session|resume|cache|none 标注取样来源。它是**事实**（运行时广告什么）；
	// 平台声明的那份是 `models` 表，读面见 ListModels。
	ModelCatalog(ctx context.Context) (*dtomodel.ModelCatalogResp, error)
	// ListModels 列出**平台声明的模型清单**（v12 起不带 runtime 维度，全量返回）。
	ListModels(ctx context.Context) ([]dtomodel.ModelResp, error)
	// CreateModel 新建一条模型条目（`(provider_id, model_id)` 唯一 → 40901；
	// provider 不存在 → 40401；请求体带 `runtime` → 40001）。
	CreateModel(ctx context.Context, req dtomodel.ModelCreateReq) (*dtomodel.ModelResp, error)
	// UpdateModel 覆盖一条模型条目（指针更新；`is_default` 写的是 **meta 的单一默认**）。
	UpdateModel(ctx context.Context, id string, patch dtomodel.ModelPatchReq) (*dtomodel.ModelResp, error)
	// DeleteModel 删除一条模型条目（幂等；**允许删**，若它是默认则连带清空并在响应里说明）。
	DeleteModel(ctx context.Context, id string) (*dtomodel.ModelDeleteResp, error)
	// SyncModels 重新同步（运行时不变量的唯一收敛入口；空 = 全部已注册 runtime）。
	// 成功路径**不回带 advertised**（那要新进程才拿得到），逐 runtime 如实回结论。
	SyncModels(ctx context.Context, runtimeID string) (*dtomodel.ModelSyncResp, error)
}
