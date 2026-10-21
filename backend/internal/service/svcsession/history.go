package svcsession

import (
	"context"
	"fmt"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// ——— 会话历史面（P4a 正文第十刀：从 `manager` 上提到用例层）———
//
// 这一面是**真用例**（不是一行转发）：游标语义（after 开区间 / before 开区间 / 两者互斥）、
// 位次哨兵（`-1` = 没有游标，而 `0` 是合法位次——夹到 0 会静默丢掉第一条）、limit 的钳制、
// 两条载体（sqlite 读 `events` 表 / 内存模式回放 eventbus），以及首尾位次与 `has_more` 的装配。
// 域的 `session.Plane` 提供会话存在性与内存事件，库句柄提供 durable 通道。
//
// `HistorySvc` 是按**面**切出来的接口（与 `SessionSvc` 分开）：会话面还有 18 个方法要逐刀搬，
// 而这一面的实现已经完整——控制器按面持有（`ctrsession` 同时持 `SessionSvc` 与 `HistorySvc`）。

// HistorySvc 会话历史读面。
type HistorySvc interface {
	// SessionHistory 见下方实现的注释。
	SessionHistory(ctx context.Context, id string, q dtosession.HistoryQuery) (*dtosession.SessionHistoryResp, error)
}

// EventReader 事件历史的**窄读端口**（v3.1 P4：用例层不 import store）。
//
// `*store.Store` 结构上满足它，装配根直接交下来；memory 模式传 nil（走 eventbus 回放）。
// 只声明这条用例真正用到的方法：多一个方法就等于多一条"这层还能读别的东西"的暗示。
type EventReader interface {
	EventsAfter(ctx context.Context, sessionID string, after, limit int64) ([]model.EventEntity, bool, error)
	EventsBefore(ctx context.Context, sessionID string, before, limit int64) ([]model.EventEntity, bool, error)
}

// History 会话历史用例。依赖事实：事件读端口（sqlite 模式；memory 模式为 nil）、会话域平面、事件总线。
type History struct {
	st    EventReader
	plane *session.Plane
	bus   *eventbus.Bus
}

// NewHistory 造会话历史用例。
func NewHistory(st EventReader, plane *session.Plane, bus *eventbus.Bus) HistorySvc {
	return &History{st: st, plane: plane, bus: bus}
}

var _ HistorySvc = (*History)(nil)

// SessionHistory 返回会话事件历史（seq 升序，after 向后 / before 向前）。
//
// 数据源：sqlite 模式（st != nil）读 events 表——durable、不受 eventbus 内存上限（65536）约束，
// 「加载更早」可一直翻到会话第一条；内存模式回放 eventbus（进程内）。
// 语义：after（开区间，seq > after）、before（开区间，seq < before，返回最近 limit 条并升序排列）
// 互斥（handler 已拦，服务层再兜一次 → 40001）；has_more 沿当前方向；首尾 seq 一并回带。
//
// **游标口径（v21）**：`seq` 是会话内位次、0 是合法值，所以"没有 after"的哨兵是 `-1`（早于该会话
// 一切事件），由 handler 在缺省时填入；`after = 0` 表示"第 0 条之后"（跳过 `session.created`）。
// 空页的 FirstSeq/LastSeq 回 **-1**（此前是 0，而 0 现在是一个真实位次，"空"与"第 0 条"必须可分）。
func (h *History) SessionHistory(ctx context.Context, id string, q dtosession.HistoryQuery) (*dtosession.SessionHistoryResp, error) {
	if !h.plane.HasSession(id) {
		return nil, code.FromError(acp.NewError(acp.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if q.After != nil && q.Before != nil {
		return nil, code.FromError(acp.NewError(acp.ClassRequest, "history_cursor_conflict",
			"after 与 before 互斥（一个向后、一个向前）", nil))
	}
	// 缺席的游标 = -1（v21 哨兵："没有游标"）。两个方向都**不夹到 0**：0 是会话第一条事件的
	// 合法位次，夹没了它，首屏/翻页就会丢掉那一条。
	after, before := int64(-1), int64(-1)
	if q.After != nil {
		if after = *q.After; after < -1 {
			after = -1
		}
	}
	if q.Before != nil {
		if before = *q.Before; before < -1 {
			before = -1
		}
	}
	if q.Limit < 1 {
		q.Limit = 1
	}
	if q.Limit > 1000 {
		q.Limit = 1000
	}

	var (
		events  []eventbus.Event
		hasMore bool
	)
	if h.st != nil {
		var batch []model.EventEntity
		var err error
		if q.Before != nil {
			batch, hasMore, err = h.st.EventsBefore(ctx, id, before, int64(q.Limit))
		} else {
			batch, hasMore, err = h.st.EventsAfter(ctx, id, after, int64(q.Limit))
		}
		if err != nil {
			return nil, code.FromError(acp.NewError(acp.ClassService, "history_read_failed",
				fmt.Sprintf("读取会话历史失败：%v", err), nil))
		}
		events = make([]eventbus.Event, 0, len(batch))
		for _, e := range batch {
			events = append(events, eventbus.Event{
				ID: e.ID, Type: e.Type, Seq: e.Seq, AggregID: e.SessionID, Payload: session.DecodePayload(e.PayloadJSON),
			})
		}
	} else {
		// 内存模式：先取该会话**全部**事件（起点固定 `-1`，与游标无关——游标只决定裁哪一端），
		// 再按方向裁窗口。起点若跟着一个缺省的 after=0 走，会让"翻更早"这条路径每次静默
		// 丢掉第 0 条事件。
		mine := h.bus.EventsAfter(id, -1)
		if q.Before != nil {
			// `before < 0` = 从最新往回（哨兵）；`before >= 0` 是"该位次之前"。
			if before >= 0 {
				kept := mine[:0]
				for _, evt := range mine {
					if evt.Seq < before {
						kept = append(kept, evt)
					}
				}
				mine = kept
			}
			// 取窗口末尾 limit 条（保持升序）；窗口外还有更早 → has_more。
			if len(mine) > q.Limit {
				hasMore = true
				mine = mine[len(mine)-q.Limit:]
			}
		} else {
			if after >= 0 {
				kept := mine[:0]
				for _, evt := range mine {
					if evt.Seq > after {
						kept = append(kept, evt)
					}
				}
				mine = kept
			}
			if len(mine) > q.Limit {
				hasMore = true
				mine = mine[:q.Limit]
			}
		}
		events = mine
	}

	resp := &dtosession.SessionHistoryResp{ID: id, Events: events, HasMore: hasMore, FirstSeq: -1, LastSeq: -1}
	if len(events) > 0 {
		resp.FirstSeq = events[0].Seq
		resp.LastSeq = events[len(events)-1].Seq
	}
	return resp, nil
}
