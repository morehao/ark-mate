package provider

import (
	"context"

	src "github.com/morehao/ark-mate/internal/core/credential"
)

// Secrets 供应商密钥面（v3.2 刀 8 收尾）。
//
// 四件事：解析一条档案的生效密钥（四层回落 + 库对账）、托管文档是否存在、往托管文档写/删。
// 前三样是**机制**的活（文件格式、层序、原子写），后一样只是"文档在不在"的存在性判断。
//
// 之所以要端口：机制侧的签名里带 `*credentials.Doc`（托管文档本体），领域一旦拿到那个类型，
// `→ infra/credentials` 与 `→ mechanism` 两条边就回来了。文档本体应当只活在适配体内部。
type Secrets interface {
	// Resolve 解析该档案的生效密钥（只读；不缓存——库、托管文档与 .env 都可能被进程外改动）。
	Resolve(ctx context.Context, providerID, ref string) src.ProviderKey
	// HasDoc 托管文档是否已装配（未装配 = memory 模式下没有可写落点）。
	HasDoc() bool
	// MirrorSet 把一条密钥写进托管文档（原子写、0600）。
	MirrorSet(ref, value string) error
	// MirrorDelete 从托管文档删掉一条（不存在不算错）。
	MirrorDelete(ref string) error
}
