// Package svcruntime 定义底座面的服务端口：runtime 概要、provider 档案
// 与运行时派生的模型目录。
package svcruntime

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
)

// RuntimeSvc 底座（runtime/provider/模型目录）面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ RuntimeSvc = (*T)(nil) 保证一致性。
type RuntimeSvc interface {
	// ListRuntimes 返回可用 Runtime 概要。
	ListRuntimes() ([]dtoruntime.RuntimeResp, error)

	// ListProviders 列出 provider 档案概要（密钥只写不读回；带引用面与问题清单）。
	ListProviders() ([]dtoprovider.ProviderResp, error)

	// CreateProvider 新建 provider 档案（id 已存在 → `provider_exists`(40901)，绝不覆盖）。
	CreateProvider(ctx context.Context, req dtoprovider.ProviderCreateReq) (*dtoprovider.ProviderResp, error)

	// UpdateProvider 覆盖 provider 档案（**只更新**；不存在 → `provider_not_found`(40401)）。
	// type/model/note 指针更新；key 非 nil 即覆盖/清除（空串 = 清除 ref + 密文）。
	UpdateProvider(ctx context.Context, id string, patch dtoprovider.ProviderPatchReq) (*dtoprovider.ProviderResp, error)

	// DeleteProvider 删除 provider 档案（幂等；被 agent 或模型条目引用 → `provider_in_use`(40901)）。
	DeleteProvider(ctx context.Context, id string) (*dtoprovider.ProviderDeleteResp, error)

	// ProbeProvider 跑一次连接自检（本地段无条件、上游段仅当 req.Upstream）。
	ProbeProvider(ctx context.Context, id string, req dtoprovider.ProviderProbeReq) (*dtoprovider.ProviderProbeResp, error)

	// ModelsView 返回 `GET /models` 的完整读面：平台清单（declared）+ 运行时事实（advertised）
	// + 两个差集（diff）+ 物化状态（config）；未声明落点的 runtime 回落到纯旧形态（options）。
	ModelsView(ctx context.Context, runtimeID, agentID string, refresh bool) (*dtomodel.ModelsResp, error)

	// ModelCatalog 返回某 agent 的**运行时派生**模型目录（ACP session config options 投影）。
	// source=session|resume|cache|none 标注取样来源。它是**事实**（运行时广告什么）；
	// 平台声明的那份是 `models` 表，读面见 ListModels。
	ModelCatalog(ctx context.Context, agentID string) (*dtomodel.ModelCatalogResp, error)

	// ListModels 列出**平台声明的模型清单**（runtimeID 为空 = 全部）。它是**意图**，
	// 与 ModelCatalog 的「事实」在对账里相遇（declared vs advertised）。
	ListModels(ctx context.Context, runtimeID string) ([]dtomodel.ModelResp, error)

	// CreateModel 新建一条模型条目（runtime/provider_id/model_id 三元组唯一；
	// runtime 未声明 ConfigProjection → 40501，provider 不存在 → 40401）。
	CreateModel(ctx context.Context, req dtomodel.ModelCreateReq) (*dtomodel.ModelResp, error)

	// UpdateModel 覆盖一条模型条目（指针更新；`is_default:true` 同时清掉同 runtime 的旧默认）。
	UpdateModel(ctx context.Context, id string, patch dtomodel.ModelPatchReq) (*dtomodel.ModelResp, error)

	// DeleteModel 删除一条模型条目（幂等；**是该 runtime 的默认模型 → 40501** `model_is_default`）。
	DeleteModel(ctx context.Context, id string) (*dtomodel.ModelDeleteResp, error)

	// ApplyModels 强制重新物化某 runtime（空 = 全部已声明落点的 runtime）的模型配置文件（T5）。
	// 成功路径**不回带 advertised**（那要新进程才拿得到），只如实回 `pending:true`。
	ApplyModels(ctx context.Context, runtimeID string) (*dtomodel.ModelApplyResp, error)
}
