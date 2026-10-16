package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/provider"
)

// ——— 供应商域：P3d 过渡期的转发层 ———
//
// 载体/写面/读面（`providers.go` · `providers_write.go` · `providers_carrier.go`，约 1128 行）
// 已落 `domain/provider`（P3a-1b）。域自持一把只保护域内字段的锁（并发审查见计划 §3.1.35：
// 29 处临界区零跨域），并持 `st`/`reg`/`creds` 三个注入件与两个端口（写面回执、默认模型）。
//
// 本文件只留转发。**转发的名字必须与调用点一致**（`app.go` 与几个测试文件都在调它们），
// 所以这里逐个保名。
//
// P4a 正文第四刀已删掉**档案面四条**（`ListProviders`/`CreateProvider`/`UpdateProvider`/
// `DeleteProvider`）：它们的用例出口现在是 `service/svcprovider`（控制器在那里把 `Provider`
// 造出来），`manager` 这几行转发已经没有生产调用点——留着只会让"实现搬到哪一层"重新不可见。
//
// 刻意**不**转发域内只被本包用到的几个私有入口（`providerDirSnapshot` / `providerModelCount`）：
// 没有消费者的一行转发就是死代码。

// TODO(P4b): 删转发（调用点直接依赖 provider.Plane）
func (s *Service) SetProviderDir(dir string) { s.providerPlane.SetProviderDir(dir) }

// TODO(P4b): 删转发（调用点直接依赖 provider.Plane）
func (s *Service) LoadProviderDefinitions(dir string) int {
	return s.providerPlane.LoadProviderDefinitions(dir)
}

// TODO(P4b): 删转发（调用点直接依赖 provider.Plane）
func (s *Service) LoadProviderDefinitionsFromStore(ctx context.Context, dir string) int {
	return s.providerPlane.LoadProviderDefinitionsFromStore(ctx, dir)
}

// TODO(P4b): 删转发（调用点直接依赖 provider.Plane）
func (s *Service) ReloadProviderDefinitionsFromStore(ctx context.Context, dir string) int {
	return s.providerPlane.ReloadProviderDefinitionsFromStore(ctx, dir)
}

// TODO(P4b): 删转发（`connection.go` 的密钥注入直接依赖 provider.Plane）
//
// `providerEnvFor` 是"密钥怎么送到子进程"的唯一入口（P3a-2 的 `runtimeconn` 会直接用它），
// 逐字保行为。
func (s *Service) providerEnvFor(runtimeID, providerID string) []string {
	return s.providerPlane.ProviderEnvFor(runtimeID, providerID)
}

// TODO(P4b): 删转发（`connection.go` 直接依赖 provider.Plane）
func (s *Service) injectedProviderID(ctx context.Context) string {
	return s.providerPlane.InjectedProviderID(ctx)
}

// TODO(P4b): 删转发（`connection.go` / `diagnostics.go` 直接依赖 provider.Plane）
func (s *Service) CredentialProblem(ctx context.Context) error {
	return s.providerPlane.CredentialProblem(ctx)
}

// TODO(P4b): 删转发（`provider_effect.go` 直接依赖 provider.Plane）
func (s *Service) providerModelRefsReadOnly(ctx context.Context, providerID string) []string {
	return s.providerPlane.ProviderModelRefsReadOnly(ctx, providerID)
}

// 编译期兜底：域必须真的提供上面这些方法（改名会让本文件当场编译失败，而不是静默少一条转发）。
var _ = provider.Def{}
