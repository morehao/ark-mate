package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/eventbus"
)

// ——— P4a：导出用例的**事实来源适配器**（`svcsystem.Source` 的实现） ———
//
// 用例层（`internal/service/svcsystem`）拥有参数校验、错误码出口与渲染；"元数据与事件序列
// 从哪来"是这里的事——两模式一致（sqlite = `sessions`/`events` 表，memory = 会话域镜像 +
// 事件总线回放），于是两种模式的产物逐字相同。
//
// **本文件不 import `service/svc*`**，而它仍然满足 `svcsystem.Source`——靠的是**结构满足**：
// 两个方法的签名逐字对得上，取值类型（`session.Meta`）与上限（`limit` 入参）都在下层。
// "实现包拿端口类型当签名"这条反向依赖（R4）因此断掉，代价是编译期断言从本文件移到装配点
// （`app.go` 的 `System: a.svc`）——**那条断言其实更硬**：装配点漏接一个端口是当场编译失败，
// 而不是等某条用例跑到一半。
//
// 本文件是适配器，不是实现：真实现（渲染/校验）在用例层，这里只做取数。

// SessionMeta 实现 `svcsystem.Source`（不存在 → ok=false，由用例层记进 `skipped`）。
//
// **不持 `s.mu`**：`Lookup` 自己取会话域的锁并返回副本（P2b 守卫那一族）。
func (s *Service) SessionMeta(ctx context.Context, id string) (session.Meta, bool) {
	if s.st != nil {
		c, ok, err := s.st.GetSession(ctx, id)
		if err != nil || !ok {
			return session.Meta{}, false
		}
		return session.Meta{ID: c.ID, Title: c.Title, RuntimeID: c.RuntimeID, Status: c.Status,
			Created: c.CreatedAt, Updated: c.UpdatedAt}, true
	}
	r, ok := s.sessionPlane.Lookup(id)
	if !ok {
		return session.Meta{}, false
	}
	return session.Meta{ID: r.ID, Title: r.Title, RuntimeID: r.RuntimeID, Status: r.Status,
		Created: r.CreatedAt, Updated: r.UpdatedAt}, true
}

// SessionEvents 实现 `svcsystem.Source`：会话事件（升序、已解码；命中 `limit` → truncated）。
//
// `limit` 由用例层给（`MaxExportEvents`）：本函数只按它截断，不再自己持一个上限常量。
func (s *Service) SessionEvents(ctx context.Context, id string, limit int) ([]eventbus.Event, bool) {
	if s.st != nil {
		var out []eventbus.Event
		after := int64(-1)
		for {
			batch, more, err := s.st.EventsAfter(ctx, id, after, 1000)
			if err != nil {
				return out, false
			}
			for _, e := range batch {
				if len(out) >= limit {
					return out, true
				}
				out = append(out, eventbus.Event{
					ID: e.ID, Type: e.Type, Seq: e.Seq, AggregID: e.SessionID,
					Payload: decodePayload(e.PayloadJSON),
				})
				after = e.Seq
			}
			if !more || len(batch) == 0 {
				break
			}
		}
		return out, false
	}
	all := s.bus.EventsAfter(id, -1)
	out := make([]eventbus.Event, 0, len(all))
	for _, e := range all {
		if len(out) >= limit {
			return out, true
		}
		out = append(out, e)
	}
	return out, false
}
