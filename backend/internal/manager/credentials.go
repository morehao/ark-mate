package manager

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/mechanism/credential"

	"github.com/morehao/ark-mate/pkg/credentials"
)

// ——— 凭据面（v20：库是权威，`<MATE_HOME>/credentials.json` 是 0600 的可重建镜像）———
//
// 这个文件是"密钥从哪来"在 manager 侧的**唯一**落点，三件事各只有一处实现：
//
//	① **解析**：provider 拨号密钥走 `credentials.Layered` 的四层回落
//	   （继承环境 > 托管 > <cwd>/.env > <home>/.env），顺序实现只有 `pkg/credentials` 那一份；
//	   MCP 的 `store:` 引用没有环境变量锚点，只查"托管"这一层。
//	② **镜像**：库写完（成功）之后双写镜像。**镜像写失败绝不让整笔写失败、也不补偿**——
//	   镜像是库的可重建副本，为它回滚一次已提交的库写入是本末倒置；失败只 WARN + 由
//	   `mate doctor` 的镜像对账段列出来。
//	③ **对账**：装配期把库**并进**镜像（非破坏性：不删镜像里已有的键）。

// credential.ProviderKey 一次 provider 拨号密钥的解析结果。
//
// 它就是 `credentials.Resolution` 再加一件 manager 才知道的事：命中"继承环境"那一层时，
// **是哪个环境变量命中的**（层序里这一层会遮住面板里保存的那条，读面必须说得出来）。

// ——— 以下是 P3d 过渡期的**转发层**（纪律：一行 + TODO，不许夹带校验/日志/错误包装）———

// TODO(P4b): 删转发（调用点直接依赖 definition.Mechanism）
func (s *Service) mirrorValue(ref string) (string, bool) {
	return credential.MirrorValue(s.credentialDoc(), ref)
}

// TODO(P4b): 删转发（调用点直接依赖 definition.Mechanism）
func (s *Service) mirrorSet(ref, value string) error {
	return credential.MirrorSet(s.credentialDoc(), ref, value)
}

// TODO(P4b): 删转发（调用点直接依赖 definition.Mechanism）
func (s *Service) mirrorDelete(ref string) error {
	return credential.MirrorDelete(s.credentialDoc(), ref)
}

// ——— P3d 过渡期转发层（纪律：一行 + TODO）———

// credentialLayered 已登记的四层凭据面（未装配 → nil）。
//
// TODO(P4b): 删转发（调用点直接依赖 credential.Store）
func (s *Service) credentialLayered() *credentials.Layered { return s.creds.Layered() }

// SetCredentials 登记四层凭据面（装配期写一次、之后只读）。
//
// TODO(P4b): 删转发（调用点直接依赖 credential.Store）
func (s *Service) SetCredentials(l *credentials.Layered) { s.creds.SetLayered(l) }

// SetCredentialCwd 登记凭据 cwd（装配期写一次、之后只读；理由见 credential.Store.SetCwd）。
//
// TODO(P4b): 删转发（调用点直接依赖 credential.Store）
func (s *Service) SetCredentialCwd(dir string) { s.creds.SetCwd(dir) }

// TODO(P4b): 删转发（调用点直接依赖 credential.Store）
func (s *Service) credentialCwd() string { return s.creds.Cwd() }

// TODO(P4b): 删转发（调用点直接依赖 credential.Store）
func (s *Service) credentialDoc() *credentials.Doc { return s.creds.Doc() }

// ——— P3d 过渡期转发层（纪律：一行 + TODO）———

// TODO(P4b): 删转发（调用点直接依赖 credential.Store）
func (s *Service) matchEnvKey(keys []string, value string) string {
	return s.creds.MatchEnvKey(keys, value)
}

// TODO(P4b): 删转发（调用点直接依赖 credential.Store）
func (s *Service) SyncCredentialMirror(ctx context.Context, reason string) (int, error) {
	return s.creds.SyncCredentialMirror(ctx, reason)
}

// TODO(P4b): 删转发（调用点直接依赖 credential.Store）
func (s *Service) providerEnvKeyNames(providerID string) []string {
	return s.creds.ProviderEnvKeyNames(providerID)
}

// TODO(P4b): 删转发（调用点直接依赖 credential.Store.ResolveProviderKey）
//
// 这一条比"一行转发"多一行，是**类型擦除的产物、不是第二份逻辑**：`d` 可能为 nil
// （`providers.go` 的 `s.providers[id]` 是 map 查找），原实现就带这个守卫；而机制层
// 已经不认识 `providerDef`，守卫没法跟着搬，只能留在转发层。
func (s *Service) resolveProviderKey(ctx context.Context, d *provider.Def) credential.ProviderKey {
	return s.providerPlane.KeyFor(ctx, d)
}
