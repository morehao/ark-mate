package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/service/svcsystem"
)

// ——— P4a：导出用例的**事实来源适配器**（`svcsystem.Source` 的实现） ———
//
// 用例层（`internal/service/svcsystem`）拥有参数校验、错误码出口与渲染；"元数据与事件序列
// 从哪来"是这里的事——两模式一致（sqlite = `sessions`/`events` 表，memory = 会话域镜像 +
// 事件总线回放），于是两种模式的产物逐字相同。
//
// 本文件是适配器，不是实现：真实现（渲染/校验）在用例层，这里只做取数。

var _ svcsystem.Source = (*Service)(nil)

// SessionMeta 实现 `svcsystem.Source`（不存在 → ok=false，由用例层记进 `skipped`）。
//
// **不持 `s.mu`**：`Lookup` 自己取会话域的锁并返回副本（P2b 守卫那一族）。
func (s *Service) SessionMeta(ctx context.Context, id string) (svcsystem.SessionMeta, bool) {
	if s.st != nil {
		c, ok, err := s.st.GetSession(ctx, id)
		if err != nil || !ok {
			return svcsystem.SessionMeta{}, false
		}
		return svcsystem.SessionMeta{ID: c.ID, Title: c.Title, RuntimeID: c.RuntimeID, Status: c.Status,
			Created: c.CreatedAt, Updated: c.UpdatedAt}, true
	}
	r, ok := s.sessionPlane.Lookup(id)
	if !ok {
		return svcsystem.SessionMeta{}, false
	}
	return svcsystem.SessionMeta{ID: r.ID, Title: r.Title, RuntimeID: r.RuntimeID, Status: r.Status,
		Created: r.CreatedAt, Updated: r.UpdatedAt}, true
}

// SessionEvents 实现 `svcsystem.Source`：会话事件（升序、已解码；命中上限 → truncated）。
func (s *Service) SessionEvents(ctx context.Context, id string) ([]eventbus.Event, bool) {
	if s.st != nil {
		var out []eventbus.Event
		after := int64(-1)
		for {
			batch, more, err := s.st.EventsAfter(ctx, id, after, 1000)
			if err != nil {
				return out, false
			}
			for _, e := range batch {
				if len(out) >= svcsystem.MaxExportEvents {
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
		if len(out) >= svcsystem.MaxExportEvents {
			return out, true
		}
		out = append(out, e)
	}
	return out, false
}
