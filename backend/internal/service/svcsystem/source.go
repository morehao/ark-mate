package svcsystem

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/eventbus"
)

// ——— P4a：系统面**消费方**声明的窄端口 ———
//
// 导出要读两样不属于用例层的东西：会话元数据（会话域）与事件序列（持久层 / memory 的事件总线）。
// 用例层不 import `manager`、也不 import `store`/`domain/session`——它**声明自己要什么**，
// 由装配侧交出实现（ADR-6：消费方声明窄接口）。
//
// 端口刻意只有两个方法：多一个都意味着用例层在替别人做决定。

// SessionMeta 导出所需的会话元数据（**与存储形态无关**：sqlite 走库、memory 走内存镜像，
// 适配器负责抹平；用例层只认这几个字段）。
type SessionMeta struct {
	ID        string
	Title     string
	RuntimeID string
	Status    string
	Created   time.Time
	Updated   time.Time
}

// Source 导出的事实来源。
type Source interface {
	// SessionMeta 取会话元数据（不存在 → ok=false，进 `skipped`）。
	SessionMeta(ctx context.Context, id string) (SessionMeta, bool)
	// SessionEvents 取会话事件（**升序**、已解码）；truncated = 命中上限被截断。
	SessionEvents(ctx context.Context, id string) (events []eventbus.Event, truncated bool)
}
