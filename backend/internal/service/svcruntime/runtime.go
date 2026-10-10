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

	// ModelCatalog 返回某 agent 的**运行时派生**模型目录（ACP session config options 投影）。
	// 平台不内置任何模型清单：source=session|resume|cache|none 标注取样来源。
	ModelCatalog(ctx context.Context, agentID string) (*dtomodel.ModelCatalogResp, error)
}
