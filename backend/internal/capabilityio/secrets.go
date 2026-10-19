package capabilityio

import (
	"context"

	src "github.com/morehao/ark-mate/internal/credential"
	"github.com/morehao/ark-mate/internal/domain/provider"
	mechcred "github.com/morehao/ark-mate/internal/mechanism/credential"
)

// Secrets 把凭据机制适配成供应商域声明的窄端口。
//
// `*credentials.Doc`（托管文档本体）只在这里出现：领域侧只看到"解析/有没有/写/删"四个动作，
// 拿不到文档本身——那正是 `→ infra/credentials` 与 `→ mechanism` 两条边的来源。
type Secrets struct{ st *mechcred.Store }

var _ provider.Secrets = Secrets{}

// NewSecrets 用凭据机制造一个密钥面适配体（装配期与测试共用）。
func NewSecrets(st *mechcred.Store) Secrets { return Secrets{st: st} }

// Resolve 解析生效密钥（四层回落 + 库对账）。
func (s Secrets) Resolve(ctx context.Context, providerID, ref string) src.ProviderKey {
	return s.st.ResolveProviderKey(ctx, providerID, ref)
}

// HasDoc 托管文档是否已装配。
func (s Secrets) HasDoc() bool { return s.st.Doc() != nil }

// MirrorSet 写入一条密钥。
func (s Secrets) MirrorSet(ref, value string) error {
	return mechcred.MirrorSet(s.st.Doc(), ref, value)
}

// MirrorDelete 删除一条密钥。
func (s Secrets) MirrorDelete(ref string) error {
	return mechcred.MirrorDelete(s.st.Doc(), ref)
}
