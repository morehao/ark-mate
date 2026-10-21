package session

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/session/sessionrow"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/platform/object/objid"
	"github.com/morehao/ark-mate/internal/platform/redact"
	"github.com/morehao/golib/glog"
)

// publish 是**事件的唯一出口**，分两段：
//
//  1. ③ 增量合并：文本增量（message/thought）进 chunkCoalescer 攒批，其余类型在此
//     **先 flush 本会话缓冲再发布**——保证「文本先于终态/工具卡」的 seq 顺序不被越过；
//  2. publishRaw：脱敏闸门 + 落 events 表（事务内取全局 seq）+ 镜像进 bus + transcript 旁路。
//
// 缓冲增量的真正发布走同一 publishRaw（由定时窗口/阈值/终端事件触发），故落库与推流路径唯一。
// 合并不改变事件 type 与 payload 形状（仍带 session_id/agent_id/msg_id 与拼接后的 text），
// 前端按 msg_id 追加同一条消息的渲染逻辑无需改动。
func (pl *Plane) Publish(typ, sessionID string, payload any) eventbus.Event {
	if c := pl.coalescer; c != nil {
		if m, ok := payload.(map[string]any); ok && c.Offers(typ) {
			c.Add(typ, sessionID, m)
			// 返回"没有事件"的零值框架 + 位次哨兵 -1：v21 起 0 是该会话第一条事件的**合法位次**，
			// 用它表示"缓冲中、还没取号"会让调用方把"已在缓冲"误读成"已落成第 0 条"。
			return eventbus.Event{Seq: -1}
		}
		c.Flush(sessionID)
	}
	return pl.PublishRaw(typ, sessionID, payload)
}

// flushChunks 落掉全部缓冲增量（停机/关闭 transcript 前调用，避免丢尾部文本）。
func (pl *Plane) FlushChunks() {
	pl.coalescer.FlushAll()
}

// publishRaw 把域事件发布进 eventbus（agg = sessionID；每 agg 版本递增），
// 并旁路追加到该会话的 wire transcript（未启用/非 map 载荷时跳过）。
// sqlite 模式下（st != nil）：事件先经 model.AppendEvent 在事务内取**会话内位次**，
// bus 以 PublishAt 镜像同一号序（跨重启连续）；DB 写失败时退化进程内取号并告警（不阻断会话）。
// 未注入 bus 时静默丢弃（仅出现在纯列表/单测场景）。
func (pl *Plane) PublishRaw(typ, sessionID string, payload any) eventbus.Event {
	// 脱敏闸门（唯一入口）：事件在此之后才落 events 表 / 进 transcript / 推 WS / 物化 runs，
	// 故一处过闸即覆盖全部下游（含历史回放读的是同一份已脱敏载荷）。
	payload = redact.Payload(payload)
	pl.mu.Lock()
	pl.ver[sessionID]++
	v := pl.ver[sessionID]
	pl.mu.Unlock()

	// v17：事件的**身份在这里铸、且只在这里铸**（live 路径），随后同一支 id 交给 store 落库、
	// 交给 bus 进帧、交给 transcript 落盘——三种载体上是同一支值（"身份随数据流动"）。
	// 存量回填是迁移的事（store/migrate_v17.go），不是第二条运行时铸号点。
	evtID := objid.New(objid.Event)

	bus := pl.bus
	// seq 的初值是 -1（"还没有位次"）而不是 0：v21 起 0 是**该会话第一条事件**的合法位次，
	// 用它当"没取到号"的哨兵，会让"落库成功且恰好是第 0 条"这件事看起来像失败（见下方 durable）。
	seq := int64(-1)
	durable := false
	if st := pl.st; st != nil {
		got, err := st.AppendEvent(context.Background(), evtID, sessionID, typ, payload)
		if err != nil {
			glog.Warnw(context.Background(), "store: append event failed（退化为进程内取号）",
				"session", sessionID, "type", typ, "err", err)
		} else {
			seq, durable = got, true
		}
	}
	var evt eventbus.Event
	switch {
	case bus == nil:
		evt = eventbus.Event{ID: evtID, Type: typ, AggregID: sessionID, Version: v, Seq: seq}
	case durable:
		// durable：DB 的会话内位次是权威，bus 只镜像（同一支 seq 进帧）。
		evt = bus.PublishAt(sessionID, seq, evtID, typ, v, payload)
	default:
		// 没落库（或落库失败）：位次由 bus 按会话分配。
		evt = bus.Publish(evtID, typ, sessionID, v, payload)
	}
	if m, ok := payload.(map[string]any); ok {
		pl.publishSideEffects(sessionID, evt.ID, typ, m, evt.Seq)
	}
	// 收口事件 → runs 收口（sqlite 模式；窗口由开轮行的 first_seq 与本条事件的位次界定）。
	// 位次**必须传进去**：它同时是窗口右端（`runs.last_seq`）——从窗口里"最后一个事件"反推
	// 会在这条终态事件恰好没落库（append 失败）时写下一个错位。
	//
	// 判据是 `RunClosingStatus` 而不是 `TerminalRunStatus`：恢复路径的 `run.status(interrupted)`
	// 也是收口事实，两条路必须用同一支判据（见该函数注释）。
	if pl.st != nil {
		if m, ok := payload.(map[string]any); ok && RunClosingStatus(typ, m) != "" {
			pl.MaterializeRun(sessionID, typ, m, evt.Seq)
		}
	}
	return evt
}

// publishSideEffects 非 map 载荷不触发的旁路副作用：
// wire transcript 追加 + 会话标题自动推导（首个用户消息；PATCH 可覆盖）。
//
// v17 起 transcript 行同时写 `id` 与 `seq`（身份 + 位次）：两者都是**上游传进来的同一支值**，
// 这里不重铸任何一个；v21 起 `seq` 是**会话内**位次（0 起）。
func (pl *Plane) publishSideEffects(sessionID, eventID, typ string, m map[string]any, seq int64) {
	// seq < 0 = 这条事件既没落库也没进总线（bus 与 store 都没装配的退化态），压根没有位次。
	// 不写进 transcript：装载面把负位次当坏行、整份文件跳过，写进去等于自己造一份读不回来的文件。
	// `pl.tx` 是**具体指针**（P3b-2b 起不再经接口端口）：`(*transcriptRecorder)(nil)` 的接收者
	// 判空在方法体内（`begin`/`appendLine` 都自己判），这一层判空仍然保留——它省掉的是一次
	// 无意义的调用，而两处判空的语义一致（nil = 未启用）。
	if seq >= 0 && pl.tx != nil {
		pl.tx.appendLine(sessionID, eventID, seq, typ, m)
	}
	// 会话标题自动推导：首个用户消息作为标题（截断）。PATCH 可覆盖（见 UpdateSession）。
	//
	// 必须走 `Mutate`（**它自己取域锁**）：`Lookup` 交出来的是副本（改副本等于没改），而在
	// 外面再包一层 `pl.mu` 就是同一 goroutine 二次加锁（自死锁——这一处与 `Restore` 那处
	// 各踩过一次，见计划 §3.1.45/§3.1.46）。域里的规矩：**跨方法的锁只有一种拿法，就是被调
	// 方法自己拿**；调用方不替它拿，也不要"顺手"延长临界区。
	if typ == BusUserMessage {
		var derived string
		pl.Mutate(sessionID, func(r *SessionRec) {
			if strings.TrimSpace(r.Title) != "" {
				return
			}
			text, _ := m["text"].(string)
			if text == "" {
				return
			}
			derived = DeriveTitle(text)
			r.Title = derived
			r.UpdatedAt = time.Now().UTC()
		})
		if derived != "" && pl.st != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := pl.st.UpdateSessionMeta(ctx, sessionID, sessionrow.MetaPatch{Title: &derived}); err != nil {
				glog.Warnw(context.Background(), "store: derive title persist failed", "session", sessionID, "err", err)
			}
		}
	}
}
