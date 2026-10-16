package svcsystem

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/eventbus"
)

// ——— P4a：系统面**消费方**声明的窄端口 ———
//
// 导出要读两样不属于用例层的东西：会话元数据（会话域）与事件序列（持久层 / memory 的事件总线）。
// 用例层不 import `manager`、也不 import `store`/`domain/session` 的平面——它**声明自己要什么**，
// 由装配侧交出实现（ADR-6：消费方声明窄接口）。
//
// 端口刻意只有两个方法：多一个都意味着用例层在替别人做决定。

// SessionMeta 导出所需的会话元数据。
//
// 它**是** `domain/session.Meta`（别名，不是平行类型）：这个值对象的下沉是刻意的——见该类型的
// 文件头，它让"实现包为了写出方法签名而反向 import 用例层"这条依赖变成不必要（R4）。
type SessionMeta = session.Meta

// Source 导出的事实来源。
type Source interface {
	// SessionMeta 取会话元数据（不存在 → ok=false，进 `skipped`）。
	SessionMeta(ctx context.Context, id string) (SessionMeta, bool)
	// SessionEvents 取会话事件（**升序**、已解码）；truncated = 命中上限被截断。
	//
	// `limit` 是**用例层的上限**（`MaxExportEvents`）而不是实现方自己的常量：上限是导出用例的
	// 口径，实现方按它截断即可。写成入参之后，"取数截一次、渲染又截一次、成品里看不出是哪一次"
	// 这条分叉在签名层面就不成立了（实现方再也拿不到第二个数）。
	SessionEvents(ctx context.Context, id string, limit int) (events []eventbus.Event, truncated bool)
}
