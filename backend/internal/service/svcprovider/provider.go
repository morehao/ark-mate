// Package svcprovider 定义**供应商档案**面的服务端口（ADR-2：从 svcruntime 拆出）。
//
// 拆的依据：provider 的读写/自检与"运行时派生的模型目录"是两件事——前者的不变量是
// "密钥只写不读回、id 不覆盖"，后者的不变量是"运行时广告什么 vs 平台声明什么"。
// 共用一个大接口会让两个控制器各自只用到一半（ctrprovider 5 个方法、ctrmodel 6 个）。
//
// **P4a 正文第三刀（供应商自检）**：`ProbeProvider` 从 `ProviderSvc` 上摘掉——
// 它的**实现**已经搬进本包（`probe.go` 的 `Probe`，实现下一条 `Prober`），
// 而档案四条动作的实现今天仍在 `manager`。用例层"收一条、少一条"的过程在这个包上一眼可见：
// 端口上剩几个方法，就说明还有几件事没搬完。
package svcprovider

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
)

// ProviderSvc 供应商**档案面**的服务端口（增删改查四条）。
//
// 实现方以编译期断言 var _ ProviderSvc = (*T)(nil) 保证一致性。今天这四条仍由
// `*manager.Service` 实现——**断言因此留在装配点**（`app.go` 把 `a.svc` 交给
// `router.Ports.Provider` 那一刻），而不是留在 `manager`：除控制器层之外的层不许引用
// `service/svc*`（`internal/archtest/svc_layer_consumer_test.go` 钉着这条）。
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
}

// Prober 供应商**自检面**的服务端口（实现就在本包：`probe.go` 的 `Probe`）。
//
// 它单独成一个接口而不是并回 `ProviderSvc`：并回去就等于又把"档案"与"自检"绑在一份实现上，
// 而这一刀的全部理由正是它们各有主人。断言跟着实现走（下面那一行）。
type Prober interface {
	// ProbeProvider 跑一次连接自检（本地段无条件、上游段仅当 req.Upstream）。
	ProbeProvider(ctx context.Context, id string, req dtoprovider.ProviderProbeReq) (*dtoprovider.ProviderProbeResp, error)
}

// Prober 的**实现方**在今天只有一个（`Probe`）；这一行让实现搬家时端口与实现
// 同包编译失败，而不是等装配点报 "missing method"。
var _ Prober = (*Probe)(nil)
