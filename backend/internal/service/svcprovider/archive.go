package svcprovider

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
)

// ——— P4a 正文第四刀：供应商**档案面**用例层实现 ———
//
// 这一刀之前，档案四动作的实现在 `domain/provider`（P3a-1b 搬进去的 1128 行），而"谁把
// 它们端出去"是 `manager` 的四条转发（`provider_forward.go`）。
// 转发层是**过渡期的形状**：它存在的唯一理由是调用点还认 `*manager.Service`——P4b 已把
// 那四条转发与整个 `manager` 包一起删除，调用点直连域平面。
//
// 现在的形状（口径见 AGENTS.md「用例层（`service/svc<领域>`）的三条硬规矩」）：
//
//	ctrprovider —— 只认本包声明的 `ProviderSvc`（一个 controller 只引用一份 service 接口）
//	     ↓ 构造
//	svcprovider.Provider（本文件）—— 档案用例的**唯一出口**：请求/响应装配、错误码出口、
//	      "这套动作对外长什么样"都在这一层
//	     ↓ 内部直接拿域平面
//	domain/provider.Plane —— 域不变量与状态（"密钥只写不读回"、id 不覆盖、载体、
//	     被模型引用不可删、并发临界区）
//
// **为什么这层是薄的、却不是"只做一行转发的适配器"**：域里那些不变量被**其他四个域**共用
// （连接期的密钥注入、诊断的凭据悬空、模型目录的默认模型、体检的清单事实），所以它们必须留在
// 共享的域包里；而"档案管理"是一条**用例**，它今天已经有一件有判据与文案的活在本包
// （`probe.go`），将来还有一件要落进来：写面的**生效面回执**（`providerEffect`，现在还在
// `manager/provider_effect.go`，因为它要问编排器"哪些会话会顺延"——那是 P5 与 `manager`
// 一起 dissolve 的东西）。把回执接进域平面的是装配点（`SetEffect`），不是域自己。
//
// 于是这一层有明确的**主人与职责边界**：它不是给域改名的壳，而是"供应商管理"这条用例的落点
// ——域不认识 dto 的对外形态，也不该认识"哪条会话会顺延"。
type Provider struct {
	plane *provider.Plane
}

// NewProvider 造供应商档案用例。`plane` 是**依赖事实**（域平面上只有一个实例，见
// `provider.NewPlane` 的注释：两份 = 两个事实源），由装配点交给控制器、控制器在这里构造。
func NewProvider(plane *provider.Plane) ProviderSvc { return &Provider{plane: plane} }

// 断言跟着实现走（`provider.go` 里那句"断言留在装配点"随之删除）。
var _ ProviderSvc = (*Provider)(nil)

// ListProviders 列出供应商概要（密钥只写不读回；带引用面、模型条数与问题清单）。
func (p *Provider) ListProviders(ctx context.Context) ([]dtoprovider.ProviderResp, error) {
	return p.plane.ListProviders(ctx)
}

// CreateProvider 新建一条供应商（id 已存在 → `provider_exists`(40901)，绝不覆盖）。
func (p *Provider) CreateProvider(ctx context.Context, req dtoprovider.ProviderCreateReq) (*dtoprovider.ProviderResp, error) {
	return p.plane.CreateProvider(ctx, req)
}

// UpdateProvider 覆盖一条供应商（**只更新**；不存在 → `provider_not_found`(40401)）。
func (p *Provider) UpdateProvider(ctx context.Context, id string, patch dtoprovider.ProviderPatchReq) (*dtoprovider.ProviderResp, error) {
	return p.plane.UpdateProvider(ctx, id, patch)
}

// DeleteProvider 删除一条供应商（幂等；被 agent 或模型引用 → `provider_in_use`(40901)）。
func (p *Provider) DeleteProvider(ctx context.Context, id string) (*dtoprovider.ProviderDeleteResp, error) {
	return p.plane.DeleteProvider(ctx, id)
}
