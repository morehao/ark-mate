package svcsystem

import (
	"context"

	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/session/event"
	"github.com/morehao/ark-mate/internal/domain/session/sessionrow"
)

// ——— P4a 正文第八刀：事实来源的适配器收进用例层 ———
//
// 直到第七刀为止，`Source` 的实现还留在 `manager`（`export_source.go`：两个方法是 `*Service`
// 的方法）。那是过渡形态：`router.Ports` 因此必须持一个 **svc 类型**（`svcsystem.Source`），
// 装配点为了声明一格字段而 import 了用例包。
//
// 收进来之后这一格换成三份**依赖事实**（`Store` / `SessionPlane` / `Bus`），
// 用例与来源都在 `ctrexport` 的构造函数里造：
//
//	Facts{st, plane, bus}  ──实现──▶  Source  ──▶  Exporter（校验/错误码/渲染）
//	   sqlite：sessions/events 表        （消费方声明）      （用例本体，`export.go`）
//	   memory：会话域镜像 + 总线回放
//
// 两种载体的选择（`st != nil` 走库）刻意留在这一个构造函数里：它是**取数的装配事实**，
// 不是业务判断；分成两个导出构造函数只会让"哪份载体"这件事散到接线点上。

// SourceFactsReader 系统面的**窄读端口**（v3.1 P4：用例层不 import store）。
//
// `*store.Store` 结构上满足它；memory 模式传 nil（只能给进程内事实）。
type SourceFactsReader interface {
	GetSession(ctx context.Context, id string) (sessionrow.Item, bool, error)
	EventsAfter(ctx context.Context, sessionID string, after, limit int64) ([]event.Item, bool, error)
}

// Facts 是 `Source` 的实现：把库或内存镜像读成导出要的形状。
//
// 它**只做取数**：渲染、上限口径、错误码都在 `Exporter`（`export.go`）。两条载体的产物
// 逐字相同，靠的是这里对同一组事实的两种读法，而不是两套渲染。
type Facts struct {
	st    SourceFactsReader
	plane *session.Plane
	bus   *eventbus.Bus
}

// NewSource 造事实来源：`st != nil`（sqlite 模式）走库，否则走会话域镜像 + 事件总线回放。
//
// `plane`/`bus` 在 sqlite 模式下也用得上（会话域镜像与实时事件仍在内存里），所以三份事实
// 一起交下来、由这里决定读哪里——接线点不再重复那个 `st != nil` 判断。
func NewSource(st SourceFactsReader, plane *session.Plane, bus *eventbus.Bus) Source {
	return &Facts{st: st, plane: plane, bus: bus}
}

var _ Source = (*Facts)(nil)

// SessionMeta 见 `Source`（不存在 → ok=false，由用例层记进 `skipped`）。
//
// **不持锁**：`Lookup` 自己取会话域的锁并返回副本（P2b 守卫那一族：持锁调域平面是锁序反转的雏形）。
func (f *Facts) SessionMeta(ctx context.Context, id string) (SessionMeta, bool) {
	if f.st != nil {
		c, ok, err := f.st.GetSession(ctx, id)
		if err != nil || !ok {
			return SessionMeta{}, false
		}
		return SessionMeta{ID: c.ID, Title: c.Title, RuntimeID: c.RuntimeID, Status: c.Status,
			Created: c.CreatedAt, Updated: c.UpdatedAt}, true
	}
	r, ok := f.plane.Lookup(id)
	if !ok {
		return SessionMeta{}, false
	}
	return SessionMeta{ID: r.ID, Title: r.Title, RuntimeID: r.RuntimeID, Status: r.Status,
		Created: r.CreatedAt, Updated: r.UpdatedAt}, true
}

// SessionEvents 见 `Source`：会话事件（升序、已解码；命中 `limit` → truncated）。
//
// `limit` 由用例层给（`MaxExportEvents`）：本函数只按它截断，不再自己持一个上限常量。
func (f *Facts) SessionEvents(ctx context.Context, id string, limit int) ([]eventbus.Event, bool) {
	if f.st != nil {
		var out []eventbus.Event
		after := int64(-1)
		for {
			batch, more, err := f.st.EventsAfter(ctx, id, after, 1000)
			if err != nil {
				return out, false
			}
			for _, e := range batch {
				if len(out) >= limit {
					return out, true
				}
				out = append(out, eventbus.Event{
					ID: e.ID, Type: e.Type, Seq: e.Seq, AggregID: e.SessionID,
					Payload: session.DecodePayload(e.PayloadJSON),
				})
				after = e.Seq
			}
			if !more || len(batch) == 0 {
				break
			}
		}
		return out, false
	}
	all := f.bus.EventsAfter(id, -1)
	out := make([]eventbus.Event, 0, len(all))
	for _, e := range all {
		if len(out) >= limit {
			return out, true
		}
		out = append(out, e)
	}
	return out, false
}
