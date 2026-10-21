package session

import (
	"context"
	"encoding/json"
	"time"

	"github.com/morehao/ark-mate/internal/platform/object/objid"
	"github.com/morehao/ark-mate/internal/domain/session/event"
	"github.com/morehao/ark-mate/internal/domain/session/message"
	"github.com/morehao/ark-mate/internal/domain/session/run"
	"github.com/morehao/golib/glog"
)

// ——— messages 物化（**白名单投影**，整会话重建） ———
//
// 语义（读模型，从 events 可完整重建；设计 §D3/J5/J7）：
//
//   - **白名单**：只有两类事件产出消息行——`session.user_message`（一行 user = 输入原文）与
//     `session.message`（若干行 assistant = 一段连续 prose，按 `msg_id` 分段）。其余一律
//     **不产出行**（DSH 的 `default → null`）：thought / tool_* / usage / permission_* /
//     config_options / run.* / input_* / session.created 都不成行——它们仍在 `events` 里，
//     时间线由事件读面渲染。"投影"只回答"可检索/可寻址的对话正文有哪几段"；多存一份时间线
//     等于给同一件事开第二个真相源，而它只会在"投影 vs 事件"不一致时变成噪声。
//   - **空正文不产行**：拼接结果 `text == ""` 的片段直接丢弃（纯 thought 的一步、失败轮的空
//     回复）。没有正文的行在检索里永远不命中，在界面上是一个空气泡——它是投影的产物，不是事实。
//   - **整会话重建**（E4）：一个事务里删掉该会话全部行、按位次重新插入。两个理由：① `ordinal`
//     是"会话内第几条"，只有整会话重排才能保证 1..N 稠密且唯一；② 幂等不再依赖"同一 run 算出
//     同一组行"——重放同一份 events 得到内容相同的行（`id` 会重铸，见下）。
//   - `ordinal` 跨 run 连续（`baseOrdinal` + 行内递增）；`kind` 目前**恒为空串**（扩展位：
//     "只有将来真要多一种可搜事实时才取值"，J5 一列一义）。
//
// 幂等：同一份 events 反复重建得到内容相同的行集合；`messages.id` 每次重铸（它是投影的内部
// 身份，不出现在任何 wire 契约里——搜索的锚点是 `seq`）。

// messageRowSourceTypes 是 messages 白名单投影的**来源类型集合**（唯一声明处）。
//
// 它与 `buildMessageRows` 里那两个 `case` 是同一件事的两种写法：`case` 是执行面（哪些事件成行），
// 这份列表是**声明面**——`mate doctor` 的 `read-models` 体检拿它判"库里有没有别的事件类型
// 混进了 messages"（在投影里加一个 `case` 很容易，而它会静默改变读模型的含义：检索会突然
// 命中思考文本）。两处必须同时改，守卫 G5 钉住这一点。
var messageRowSourceTypes = []string{BusUserMessage, BusMessage}

// isMessageRowSourceType 判断某个事件类型是否在投影白名单里。
func isMessageRowSourceType(t string) bool {
	for _, s := range messageRowSourceTypes {
		if s == t {
			return true
		}
	}
	return false
}

type msgRowBuilder struct {
	role      string
	msgID     string
	text      string
	seqLo     int64
	seqHi     int64
	events    []map[string]any
	createdAt time.Time
}

// MaterializeMessages 是 live 路径（run 收口）的 best-effort 包装：失败只 WARN，events 仍是
// 唯一事实源（下一次重建会把整会话重铺回来）。整会话重铺比"只重铺一轮"重，所以超时给到 5s。
func (pl *Plane) MaterializeMessages(sessionID string) {
	if pl.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pl.RebuildSessionMessages(ctx, sessionID); err != nil {
		glog.Warnw(context.Background(), "store: rebuild messages failed", "session", sessionID, "err", err)
	}
}

// RebuildSessionMessages 读该会话的事件与 runs，整会话重铺 messages（**能报错**的那一半：
// 重建通道要把失败报上去；live 路径由 `MaterializeMessages` 降级成 WARN）。
func (pl *Plane) RebuildSessionMessages(ctx context.Context, sessionID string) error {
	if pl.st == nil {
		return nil
	}
	all, err := pl.SessionEvents(ctx, sessionID)
	if err != nil {
		return err
	}
	runs, err := pl.SessionRuns(ctx, sessionID)
	if err != nil {
		return err
	}
	return pl.ReplaceSessionMessages(ctx, sessionID, all, runs)
}

// ReplaceSessionMessages 用**已经读好的** events/runs 重铺（重建通道手上有这两份数据，不必再读
// 一遍；两条路共用同一段构建代码——"运行时"与"重建"分叉过一次，代价是读模型有两套规则）。
func (pl *Plane) ReplaceSessionMessages(ctx context.Context, sessionID string, all []event.Item, runs []run.Item) error {
	rows := buildSessionMessageRows(sessionID, all, runs)
	return pl.st.ReplaceSessionMessages(ctx, sessionID, rows)
}

// buildSessionMessageRows 按每个 run 的窗口切片、逐窗构建消息行；`ordinal` 跨窗连续。
//
// 进行中的轮（`last_seq = -1`）窗口右端夹到下一轮起点之前（没有下一轮 = 会话末尾）：不夹的话，
// 一次重启后的重建会把"中断那一轮"之后的事件都算进它。
func buildSessionMessageRows(sessionID string, all []event.Item, runs []run.Item) []message.Item {
	var out []message.Item
	for i, r := range runs {
		hi := r.LastSeq
		if hi < 0 && i+1 < len(runs) {
			hi = runs[i+1].FirstSeq - 1
		}
		win := eventsInWindow(all, r.FirstSeq, hi)
		if len(win) == 0 {
			continue
		}
		out = append(out, buildMessageRows(sessionID, r.ID, win, len(out))...)
	}
	return out
}

// buildMessageRows 把**一个窗口**的事件投影成消息行（白名单 + 空正文丢弃 + 序号接续）。
// `baseOrdinal` = 该窗口之前已经产出的行数（整会话重建时跨轮连续，单窗口重建时给 0）。
func buildMessageRows(sessionID, runID string, evs []event.Item, baseOrdinal int) []message.Item {
	var out []message.Item
	ordinal := baseOrdinal
	var cur *msgRowBuilder

	flush := func() {
		if cur == nil {
			return
		}
		b := cur
		cur = nil
		if b.text == "" {
			// 白名单投影的另一半：**空正文不产行**。纯 thought/工具的一步、失败轮的空回复都落到
			// 这里——它们没有可检索的正文，落成行只会在界面上留一个空气泡、在"空正文"体检里报一条
			// 假阳性。事件仍在库里，时间线不缺东西。
			return
		}
		at := b.createdAt
		if at.IsZero() {
			at = time.Now().UTC()
		}
		ordinal++
		out = append(out, message.Item{
			ID:        objid.New(objid.Message),
			SessionID: sessionID, RunID: runID, Role: b.role, Text: b.text,
			EventsJSON: mustJSON(b.events), ToolDetailsJSON: "[]",
			Ordinal: int64(ordinal), Kind: "",
			SeqLo: b.seqLo, SeqHi: b.seqHi, CreatedAt: at,
		})
	}

	for _, e := range evs {
		var m map[string]any
		if err := json.Unmarshal([]byte(e.PayloadJSON), &m); err != nil {
			m = map[string]any{}
		}
		switch e.Type {
		case BusUserMessage:
			flush()
			text, _ := m["text"].(string)
			if text == "" {
				continue
			}
			ordinal++
			out = append(out, message.Item{
				ID:        objid.New(objid.Message),
				SessionID: sessionID, RunID: runID, Role: message.RoleUser, Text: text,
				EventsJSON:      mustJSON([]map[string]any{{"seq": e.Seq, "type": e.Type, "payload": m}}),
				ToolDetailsJSON: "[]",
				Ordinal:         int64(ordinal), Kind: "",
				SeqLo: e.Seq, SeqHi: e.Seq, CreatedAt: e.CreatedAt,
			})
		case BusMessage:
			text, _ := m["text"].(string)
			msgID, _ := m["msg_id"].(string)
			if cur == nil || (msgID != "" && cur.msgID != "" && msgID != cur.msgID) {
				flush()
				cur = &msgRowBuilder{role: message.RoleAssistant, msgID: msgID, seqLo: -1, seqHi: -1, createdAt: e.CreatedAt}
			} else if cur.msgID == "" && msgID != "" && len(cur.events) > 0 {
				// 无 msg_id 的首段之后带出 msg_id → 归属同一 assistant 消息（底座补 id 的场景）。
				cur.msgID = msgID
			}
			// 初值 -1 而不是 0：v21 起会话第 0 条事件的位次就是 0，用 0 当"还没设过"会让
			// "本轮首事件恰好在位次 0"被下一次迭代静默改写（seq_lo 指向第二条）。
			if cur.seqLo < 0 {
				cur.seqLo = e.Seq
			}
			cur.seqHi = e.Seq
			cur.text += text
			cur.events = append(cur.events, map[string]any{"seq": e.Seq, "type": e.Type, "payload": m})
		default:
			// 白名单之外的一切（thought/tool_*/usage/permission_*/config_options/run.*/input_*/
			// session.created…）**不产生消息行**，也不进任何行的正文——时间线由 events 读面渲染。
			continue
		}
	}
	flush()
	return out
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// MessageRowSourceTypes 白名单投影的来源类型集合（**只读副本**）。
//
// 调用方是组合根：`mate doctor` 的 read-models 体检要拿它判"库里有没有别的事件类型混进
// messages"，而体检判据在机制层、不许 import 会话侧——由组合根把这份事实交下去。
func MessageRowSourceTypes() []string {
	return append([]string(nil), messageRowSourceTypes...)
}
