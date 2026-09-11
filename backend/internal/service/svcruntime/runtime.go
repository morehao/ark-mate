// Package svcruntime 定义底座面的服务端口：runtime 概要、provider 档案
// 与运行时派生的模型目录。
package svcruntime

import (
	"context"

	"github.com/morehao/ark-forge/internal/dto/dtomodel"
	"github.com/morehao/ark-forge/internal/dto/dtoprovider"
	"github.com/morehao/ark-forge/internal/dto/dtoruntime"
)

// RuntimeSvc 底座（runtime/provider/模型目录）面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ RuntimeSvc = (*T)(nil) 保证一致性。
type RuntimeSvc interface {
	// ListRuntimes 返回可用 Runtime 概要。
	ListRuntimes() ([]dtoruntime.RuntimeResp, error)

	// ListProviders 列出 provider 档案概要（密钥只写不读回）。
	ListProviders() ([]dtoprovider.ProviderResp, error)

	// UpdateProvider upsert provider 档案（type/model/key 子集；key 空串 = 清除）。
	UpdateProvider(ctx context.Context, id string, patch dtoprovider.ProviderPatchReq) (*dtoprovider.ProviderResp, error)

	// ModelCatalog 返回某 agent 的**运行时派生**模型目录（ACP session config options 投影）。
	// 平台不内置任何模型清单：source=session|resume|cache|none 标注取样来源。
	ModelCatalog(ctx context.Context, agentID string) (*dtomodel.ModelCatalogResp, error)
}
