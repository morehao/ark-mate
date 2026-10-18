package runtime

import (
	"context"

	"github.com/morehao/ark-mate/internal/identity"
	"github.com/morehao/ark-mate/internal/resource"
)

// IdentityAdapter 第五张可执行端口表：**产品身份的交付实现**（见 registry.go 的 `identities`）。
//
// 为什么身份要单独一张表，而不是复用 `resource.Syncer`：两者输入不同、失败半径不同、消费者也不同。
//
//	Syncer     —— 应用层**资源**（供应商/模型），要读库、要凭据、有"空清单必撤回"的契约；
//	Identity   —— 一个**部署级常量**，不读库、不含密钥、失败只影响"模型怎么自称"。
//
// 把它们塞进同一张表会让 `SyncModels` 多一个与模型无关的职责，也会让"空清单必撤回"那条
// 契约莫名其妙地作用到身份上（身份为空确实要撤回，但它的判据是**身份模式**，不是模型清单长度）。
//
// 实现方（`pkg/runtime/<adapter>`）自己知道写哪个文件、什么格式、怎么校验、怎么撤回；
// 内核只认识 `identity.Spec` 这个值对象，不认识任何底座的键名。
//
// 契约（三条）：
//
//	① **幂等**：同一份 `Spec.Fingerprint()` 重复交付必须收敛（不重写字节、不重装参数）；
//	② **撤回是删 + 摘**：空 `SystemPrompt` 时必须真的把产物拿掉（删文件 + 从启动参数里摘掉），
//	   只"不写"等于让上一份继续生效——而界面上没有任何一行会说这件事；
//	③ **fail-loud 且不半写**：静态校验失败即中止并返回错误，绝不写出可能让底座拒启的字节。
//
// 结论用 `resource.Outcome`（复用状态取值域：applied/unchanged/unsupported/failed），
// 其中 `Credential` 段恒为零值——身份没有凭据面。
type IdentityAdapter interface {
	DeliverIdentity(ctx context.Context, spec identity.Spec) (resource.Outcome, error)
}
