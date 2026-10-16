// Package svcprovider 定义**供应商档案**面的服务端口（ADR-2：从 svcruntime 拆出）。
//
// 拆的依据：provider 的读写/自检与"运行时派生的模型目录"是两件事——前者的不变量是
// "密钥只写不读回、id 不覆盖"，后者的不变量是"运行时广告什么 vs 平台声明什么"。
// 共用一个大接口会让两个控制器各自只用到一半（ctrprovider 5 个方法、ctrmodel 6 个）。
package svcprovider

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
)

// ProviderSvc 供应商档案面的服务端口。
type ProviderSvc interface {
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
}
