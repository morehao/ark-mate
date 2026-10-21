// Package svcmodel 承载**供应商域**的用例层：供应商档案、连接自检、平台模型清单写面，
// 以及模型读清单 / 运行时目录 / 同步面的过渡端口。
//
// 2026-10-20：原 `svcprovider` 并入本包——provider 与 model 同属 `domain/provider` 一个域
// （`models.provider_id` 是外键式的挂靠），域包早已同时拥有两者；合并的是目录，不是接口。
// 包名沿用 `svcmodel`（用户面主名词是「设置 · 模型」），域仍是 provider，二者不矛盾。
// 包内仍按**面**切：`ProviderSvc` / `Prober` / `DeclaredSvc` / `ModelSvc` / `Host` /
// `ModelStore`，一个控制器只认自己那片，不合并成大接口。原来拆两个包的理由（"共用一个大接口
// 会让两个控制器各自只用到一半"）已由这些窄接口解决，不再需要目录边界。
//
// **P4a 正文第三/四刀**：自检（`ProbeProvider`）与档案四动作的**实现**都已落本包
// （`probe.go` 的 `Probe` / `archive.go` 的 `Provider`），断言因此也跟着进本包：
// 一个接口只有一处定义、一份实现，装配点不再持有任何"谁的实现"的知识。
package model

import (
	"context"

	"github.com/morehao/ark-mate/internal/contract/dtomodel"
)

// ProviderSvc 供应商**档案面**的服务接口（增删改查四条）。
//
// 实现方是本包的 `Provider`（`archive.go`，断言 `var _ ProviderSvc = (*Provider)(nil)` 就在
// 那里）——接口、实现、构造三样同一个包，消费方（`ctrmodel`）只认这个接口。
type ProviderSvc interface {
	// ListProviders 列出供应商概要（密钥只写不读回；带引用面、模型条数与问题清单）。
	ListProviders(ctx context.Context) ([]dtomodel.ProviderResp, error)
	// CreateProvider 新建一条供应商（id 已存在 → `provider_exists`(40901)，绝不覆盖）。
	CreateProvider(ctx context.Context, req dtomodel.ProviderCreateReq) (*dtomodel.ProviderResp, error)
	// UpdateProvider 覆盖一条供应商（**只更新**；不存在 → `provider_not_found`(40401)）。
	// name/protocol/base_url/note 指针更新；api_key 非 nil 即覆盖/清除（空串 = 清除 ref + 密文）。
	UpdateProvider(ctx context.Context, id string, patch dtomodel.ProviderPatchReq) (*dtomodel.ProviderResp, error)
	// DeleteProvider 删除一条供应商（幂等；被 agent 或模型引用 → `provider_in_use`(40901)）。
	DeleteProvider(ctx context.Context, id string) (*dtomodel.ProviderDeleteResp, error)
}

// Prober 供应商**自检面**的服务端口（实现就在本包：`probe.go` 的 `Probe`）。
//
// 它单独成一个接口而不是并回 `ProviderSvc`：并回去就等于又把"档案"与"自检"绑在一份实现上，
// 而这一刀的全部理由正是它们各有主人。断言跟着实现走（下面那一行）。
type Prober interface {
	// ProbeProvider 跑一次连接自检（本地段无条件、上游段仅当 req.Upstream）。
	ProbeProvider(ctx context.Context, id string, req dtomodel.ProviderProbeReq) (*dtomodel.ProviderProbeResp, error)
}

// Prober 的**实现方**在今天只有一个（`Probe`）；这一行让实现搬家时端口与实现
// 同包编译失败，而不是等装配点报 "missing method"。
var _ Prober = (*Probe)(nil)
