// Package svcruntime 定义底座面的服务端口：provider 档案、运行时派生的模型目录与运行时诊断。
//
// **没有 runtime 概览**：底座由构建期固定（backend/pkg/runtime/build.go），用户面不存在
// "列出有哪些底座"的读面（`GET /runtimes` 已随 agent 候选面一并下线）。
package svcruntime

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
)

// RuntimeSvc 底座（provider/模型目录）面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ RuntimeSvc = (*T)(nil) 保证一致性。
type RuntimeSvc interface {
	// ListProviders 列出供应商概要（密钥只写不读回；带引用面、模型条数与问题清单）。
	ListProviders(ctx context.Context) ([]dtoprovider.ProviderResp, error)

	// CreateProvider 新建一条供应商（id 已存在 → `provider_exists`(40901)，绝不覆盖）。
	CreateProvider(ctx context.Context, req dtoprovider.ProviderCreateReq) (*dtoprovider.ProviderResp, error)

	// UpdateProvider 覆盖一条供应商（**只更新**；不存在 → `provider_not_found`(40401)）。
	// name/protocol/base_url/note 指针更新；api_key 非 nil 即覆盖/清除（空串 = 清除 ref + 密文）。
	UpdateProvider(ctx context.Context, id string, patch dtoprovider.ProviderPatchReq) (*dtoprovider.ProviderResp, error)

	// DeleteProvider 删除一条供应商（幂等；被 agent 或模型引用 → `provider_in_use`(40901)）。
	DeleteProvider(ctx context.Context, id string) (*dtoprovider.ProviderDeleteResp, error)

	// ProbeProvider 跑一次连接自检（本地段无条件、上游段仅当 req.Upstream）。
	ProbeProvider(ctx context.Context, id string, req dtoprovider.ProviderProbeReq) (*dtoprovider.ProviderProbeResp, error)

	// RuntimeIdentity 构建期固定的底座身份（`/meta` 的 `runtime` 段：id/name/version）。
	//
	// 它是**这个二进制的属性**（`-X pkg/runtime.BuildRuntime=<id>`，不注入 = dsh），所以没有参数、
	// 也不会失败：id 恒为 `runtime.SelectedRuntime()`，name 取注册表里的展示名（取不到回落 id），
	// version 取不到即空串（核心不探测本机安装）。
	RuntimeIdentity(ctx context.Context) dtoruntime.IdentityResp

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

	// RuntimeDiagnostics 返回 `GET /runtime/diagnostics` 的读面：底座**此刻**的事实
	// （进程状态、握手结论、stderr 尾巴、最后一条错误与补救指引）。
	//
	// v19：它原是 `GET /agents/:id/diagnostics`，agent 概念并入 runtime 后折回这里——
	// 底座只有一个，路径里再带一个 id 只会暗示"可以选"。**只读**：不主动拨号。
	RuntimeDiagnostics(ctx context.Context) (*dtoruntime.DiagnosticsResp, error)
}
