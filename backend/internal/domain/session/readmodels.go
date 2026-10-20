package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/glog"
)

// RebuildSessionReadModels 用 `events` 重建某会话的全部读模型（J7：读模型整体可重建）。
//
// 三段：① `runs` 补缺（`run.started` 事件是权威：缺行→补一行）；② 把**日志里已有终态**却还
// 停在 running 的行收口（终态那次落库失败的自愈）；③ `messages` 整会话重铺。
//
// ①② 只认事件、不认内存：这一轮结束没有，看的是它自己的终态事件在不在日志里。因此
// `openRun`/`FinishRun` 这两条 best-effort 路径失败留下的洞都能被下一次重建补上（J7 的字面意思）。
//
// 做法：读该会话全部事件（会话内位次升序，一次读完）与 runs（按窗口起点排）→ 交给
// `ReplaceSessionMessages` **整会话重铺**（一个事务里先删该会话全部 messages 再插入）。
// 与 live 路径共用同一段构建代码（`buildSessionMessageRows`）——"运行时"与"重建"分叉过一次，
// 代价是读模型里有两套规则，而两套规则的分歧只会在"重建之后看起来不一样"时暴露。
//
// 整会话重铺而不是逐轮重铺（E4）：`ordinal` 是"会话内第几条"，只有整会话重排才能保证它稠密；
// 顺带把"没有归属 run 的孤儿行"（旧口径留下的）一起清掉，而不是永远留在库里当幽灵消息。
//
// 幂等：同一份 events 反复跑得到**内容相同**的行集合（`objid.New` 会铸新 id，但读模型的身份不
// 承担外部引用——搜索的锚点是 `seq`，`messages.id` 不出现在任何 wire 契约里）。
func (pl *Plane) RebuildSessionReadModels(ctx context.Context, sessionID string) error {
	if pl.st == nil {
		return nil
	}
	all, err := pl.SessionEvents(ctx, sessionID)
	if err != nil {
		return err
	}
	// ① 补齐缺失的 run 行（开轮落行是 best-effort：不补的话，那一轮会永久从轮次明细里消失——
	// 终态找不到开轮行就跳过——而 events 里它明明还在）。
	if added, err := pl.reconcileRunRows(ctx, sessionID, all); err != nil {
		return fmt.Errorf("read models: reconcile runs: %w", err)
	} else if added {
		glog.Infow(ctx, "read models: 从 run.started 补出缺失的 runs 行", "session", sessionID)
	}
	runs, err := pl.SessionRuns(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("read models: list runs: %w", err)
	}
	// ② 用日志里的终态把还停在 running 的行收口。收口会改 `last_seq`，而窗口夹取要用新值，
	// 所以改完必须重读一次。
	if closed := pl.closeRunsWithTerminal(sessionID, all, runs); closed {
		glog.Infow(ctx, "read models: 用日志里的终态事件收口了停在 running 的行", "session", sessionID)
		if runs, err = pl.SessionRuns(ctx, sessionID); err != nil {
			return fmt.Errorf("read models: list runs: %w", err)
		}
	}
	// ②b 还剩 running 的行、而会话已经不在 in_progress：崩溃残轮没有事件层收口事实的旧库。
	// 「重建之后不该有悬空的 running」是这一段的**唯一**目的（`mate doctor` 的判据就是它）。
	if closed := pl.closeDanglingRunsOfInactiveSession(ctx, sessionID, runs, all); closed {
		glog.Infow(ctx, "read models: 会话不在进行中，收口了残留的 running 行", "session", sessionID)
		if runs, err = pl.SessionRuns(ctx, sessionID); err != nil {
			return fmt.Errorf("read models: list runs: %w", err)
		}
	}
	// ③ 整会话重铺 messages（"按窗口切片 + 序号跨轮连续 + 空正文丢弃"全在
	// `buildSessionMessageRows` 里；进行中的轮窗口也在那里夹）。
	return pl.ReplaceSessionMessages(ctx, sessionID, all, runs)
}

// SessionRuns 读某会话的 runs 并**按窗口起点排序**。
//
// 为什么不用 `ListRuns` 自己的顺序：它按 `started_at` 排（那是给人看的"第几轮"），而窗口夹取
// 只能按位次来——时钟回拨时 `started_at` 的顺序说了不算，`first_seq` 说了算。
func (pl *Plane) SessionRuns(ctx context.Context, sessionID string) ([]model.RunEntity, error) {
	runs, err := pl.st.ListRuns(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].FirstSeq < runs[j].FirstSeq })
	return runs, nil
}

// reconcileRunRows 用 `run.started` 事件补齐**缺失的** run 行（J7：runs 也是投影）。
//
// 只补：已存在的行（含崩溃留下的 running 行）既不删也不改——它的 status 是事实，投影推不出来。
// 行身份**取自事件载荷**（`run_id`），绝不在这里铸新号：读模型不能成为第二个身份铸造点。
func (pl *Plane) reconcileRunRows(ctx context.Context, sessionID string, all []model.EventEntity) (bool, error) {
	rows, err := pl.st.ListRuns(ctx, sessionID)
	if err != nil {
		return false, err
	}
	known := make(map[int64]bool, len(rows))
	for _, r := range rows {
		known[r.FirstSeq] = true
	}
	added := false
	for _, e := range all {
		if e.Type != BusRunStarted || known[e.Seq] {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(e.PayloadJSON), &m); err != nil {
			glog.Warnw(ctx, "read models: run.started 载荷解不开，跳过", "session", sessionID, "seq", e.Seq)
			continue
		}
		runID, _ := m["run_id"].(string)
		if runID == "" {
			glog.Warnw(ctx, "read models: run.started 载荷没有 run_id，跳过", "session", sessionID, "seq", e.Seq)
			continue
		}
		text, _ := m["text"].(string)
		if err := pl.st.CreateRun(ctx, model.RunEntity{
			ID: runID, SessionID: sessionID, UserText: text,
			Status: SessionStatusInProgress, FirstSeq: e.Seq, LastSeq: -1, StartedAt: e.CreatedAt,
		}); err != nil {
			return added, err
		}
		known[e.Seq] = true
		added = true
	}
	return added, nil
}

// closeRunsWithTerminal 把"日志里已经有本轮**收口事实**、行却还停在 running"的 run 收口。
//
// 这是给收口物化**失败**（`FinishRun` 写库失败只 WARN）自愈用的：那一轮的开轮行还在、收口事件
// 也在，缺的只是那次 UPDATE。判据不是"猜这一轮结束了"——而是日志里那条事件的载荷
// `run_id` **就是这一轮的身份**（同一支值，从开轮一路带到这里）。
//
// "收口事实"有两支（见 `RunClosingStatus`）：终态事件，以及恢复路径的 `run.status(interrupted)`。
// 后者是崩溃残轮在**事件层**留下的收口；没有它，重建只能靠"会话不在 in_progress"这条更弱的口径
// （见 `closeDanglingRunsOfInactiveSession`），而那条口径说不出「轮次是重启带走的」。
func (pl *Plane) closeRunsWithTerminal(sessionID string, all []model.EventEntity, runs []model.RunEntity) bool {
	open := make(map[string]bool, len(runs))
	for _, r := range runs {
		if r.LastSeq < 0 {
			open[r.ID] = true
		}
	}
	if len(open) == 0 {
		return false
	}
	closed := false
	for _, e := range all {
		var m map[string]any
		if err := json.Unmarshal([]byte(e.PayloadJSON), &m); err != nil {
			continue
		}
		if RunClosingStatus(e.Type, m) == "" {
			continue
		}
		id, _ := m["run_id"].(string)
		if id == "" || !open[id] {
			continue
		}
		// 复用 live 路径的收口函数（窗口 = `[first_seq, 终态位次]`、user_text 取行里的、首令牌耗时
		// 相对行里的起点）——"重建"与"运行时"必须是同一段代码，否则两条路会慢慢分叉。
		pl.MaterializeRun(sessionID, e.Type, m, e.Seq)
		delete(open, id)
		closed = true
	}
	return closed
}

// closeDanglingRunsOfInactiveSession 把「会话已经不在 in_progress、run 行却仍停在 running」的行收口。
//
// 这是**只剩这一条路**时的兜底：崩溃残轮在旧库（`run.status(interrupted)` 还不存在时写下的）里
// 没有事件层的收口事实，而「会话不在 in_progress」本身已经是一个足够强的判断——一条会话不可能
// 在非 in_progress 状态下还有一轮在跑。取值直接用会话的终态归类（`interrupted` / `completed` /
// `failed` / `cancelled` 与 run 的取值同词），窗口右端取该会话的最后一个位次（没有终态事件，
// 窗口就是"从起点到日志末尾"）。
//
// 与 `closeRunsWithTerminal` 的分工：那一支优先（它读得出"重启带走的"），本函数只处理它漏下的。
func (pl *Plane) closeDanglingRunsOfInactiveSession(ctx context.Context, sessionID string, runs []model.RunEntity, all []model.EventEntity) bool {
	if pl.st == nil || len(runs) == 0 || len(all) == 0 {
		return false
	}
	rec, ok := pl.Lookup(sessionID)
	if !ok || rec.Status == SessionStatusInProgress || rec.Status == "" {
		return false
	}
	lastSeq := all[len(all)-1].Seq
	closed := false
	for _, r := range runs {
		if r.LastSeq >= 0 || lastSeq < r.FirstSeq {
			continue
		}
		if err := pl.st.FinishRun(ctx, r.ID, model.RunFinishPatch{
			LastSeq: lastSeq, Status: rec.Status, EndedAt: all[len(all)-1].CreatedAt,
		}); err != nil {
			glog.Warnw(ctx, "read models: 收口残留 running 行失败", "session", sessionID, "run", r.ID, "err", err)
			continue
		}
		closed = true
	}
	return closed
}

// SessionEvents 读某会话的全部事件（位次升序；跨页循环，页大小与恢复注入一致）。
func (pl *Plane) SessionEvents(ctx context.Context, sessionID string) ([]model.EventEntity, error) {
	var all []model.EventEntity
	after := int64(-1)
	for {
		batch, more, err := pl.st.EventsAfter(ctx, sessionID, after, 2000)
		if err != nil {
			return nil, fmt.Errorf("read models: read events: %w", err)
		}
		all = append(all, batch...)
		if !more || len(batch) == 0 {
			return all, nil
		}
		after = batch[len(batch)-1].Seq
	}
}

// runRowFor 取本轮的开轮行：先按终态载荷里的 `run_id`；取不到再退到"该会话**唯一**的 running 行"。
//
// 兜底路径存在的原因：终态载荷的 `run_id` 是 M2 才加上的，而**开轮行也在 M2 才落**——升级前后
// 交错的那一轮会只满足一半（有行没 id / 有 id 没行）。三条都落空时返回 false，调用方 WARN 跳过：
// 没有起点就没有窗口，硬造一个会把别的轮次的事件算进这一轮（比不物化更糟）。
//
// **不唯一就不猜**：同一会话里两条 running 行意味着崩溃残轮，那时把终态记在"最近一条"上会
// 静默地把一轮的窗口/状态写到另一轮身上（读出来毫无异常，只是数字是别的轮的）。
func (pl *Plane) runRowFor(ctx context.Context, sessionID, runID string) (model.RunEntity, bool) {
	if runID != "" {
		if r, ok, err := pl.st.RunByID(ctx, runID); err != nil {
			glog.Warnw(context.Background(), "store: 读 run 行失败", "session", sessionID, "run", runID, "err", err)
		} else if ok {
			return r, true
		}
	}
	rows, err := pl.st.RunningRuns(ctx, sessionID)
	if err != nil {
		glog.Warnw(context.Background(), "store: 读 running run 失败", "session", sessionID, "err", err)
		return model.RunEntity{}, false
	}
	if len(rows) != 1 {
		if len(rows) > 1 {
			glog.Warnw(context.Background(), "store: 该会话有多条 running 行，不猜归属",
				"session", sessionID, "n", len(rows))
		}
		return model.RunEntity{}, false
	}
	return rows[0], true
}

// MaterializeRun 在 sqlite 模式下把**本轮窗口**收口：窗口 = `[run.first_seq, terminalSeq]`，
// 起点由开轮行给出（v22 起不再由"上一 run 的 last_seq + 轮界裁剪规则"推导——那套推导曾参与
// 身份构造，规则一变历史轮次的 id 就跟着变）。
//
// 三件写在一次调用里：① UPDATE 行的终态与窗口（含 last_seq）；② user_text 从行读（开轮已写）；
// ③ 用窗口事件重建 messages。幂等：行已终态时重复调用只会重算同一窗口。
// best-effort：失败仅告警（events 仍是唯一事实源）。
func (pl *Plane) MaterializeRun(sessionID, typ string, payload map[string]any, terminalSeq int64) {
	if pl.st == nil {
		return
	}
	status := RunClosingStatus(typ, payload)
	if status == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runID := ""
	if payload != nil {
		runID, _ = payload["run_id"].(string)
	}
	row, ok := pl.runRowFor(ctx, sessionID, runID)
	if !ok {
		// M2 之前开出的轮（或直发终态事件的调用方）没有开轮行：没有起点就没有窗口。
		glog.Warnw(context.Background(), "store: 找不到本轮的开轮行，跳过 runs 物化",
			"session", sessionID, "run", runID, "type", typ)
		return
	}
	runID = row.ID

	evs, more, err := pl.st.EventsAfter(ctx, sessionID, row.FirstSeq-1, 5000)
	if err != nil {
		glog.Warnw(context.Background(), "store: run window read failed", "session", sessionID, "err", err)
		return
	}
	if more {
		glog.Warnw(context.Background(), "store: run window 超 5000 条被截断", "session", sessionID)
	}
	// 窗口右端 = 终态位次（开区间读回来的可能包含更晚的事件：同一会话串行执行，正常不会有，
	// 但重建/重放路径下"多读"必须是显式裁掉，而不是靠"恰好没有"）。
	evs = eventsInWindow(evs, row.FirstSeq, terminalSeq)
	if len(evs) == 0 {
		glog.Warnw(context.Background(), "store: run window 为空，跳过 runs 物化",
			"session", sessionID, "run", runID, "first_seq", row.FirstSeq, "terminal_seq", terminalSeq)
		return
	}

	stopReason, errCode, payloadText := "", "", ""
	if payload != nil {
		if t, _ := payload["text"].(string); t != "" {
			payloadText = t
		}
		if st, _ := payload["stop_reason"].(string); st != "" {
			stopReason = st
		}
		if cd, _ := payload["code"].(string); cd != "" {
			errCode = cd
		}
	}
	// user_text：开轮行是权威（`run.started` 载荷里的原文）；行里空（老数据）才回落到窗口内
	// 首条用户消息，最后才是终态载荷的 text。
	userText := row.UserText
	if userText == "" {
		userText = payloadText
	}
	if userText == "" {
		for _, e := range evs {
			if e.Type != BusUserMessage {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(e.PayloadJSON), &m); err == nil {
				if t, _ := m["text"].(string); t != "" {
					userText = t
					break
				}
			}
		}
	}

	// 起止时刻：起点取**行里的**（开轮那一刻），终点取终态事件时刻。
	started := runStartTime(row.StartedAt, evs[0].CreatedAt)
	ended := evs[len(evs)-1].CreatedAt
	// 首令牌耗时：首个 assistant 输出（文本/思考/工具）事件相对**本轮起点**的毫秒差；
	// 无输出则为 0（与 dto 里 first_token_ms 的口径一致）。
	var firstTokenMS int64
	for _, e := range evs {
		if e.Type == BusMessage || e.Type == BusThought || e.Type == BusToolCall {
			firstTokenMS = e.CreatedAt.Sub(started).Milliseconds()
			break
		}
	}
	// M3 用量统计：随 run 落库该会话最近一次 ACP 用量快照（无上报 → 空串，不臆造数值）。
	usageJSON := ""
	if pl.usageOf != nil {
		if u, ok := pl.usageOf(sessionID); ok {
			if b, err := json.Marshal(u); err == nil {
				usageJSON = string(b)
			}
		}
	}
	if err := pl.st.FinishRun(ctx, runID, model.RunFinishPatch{
		LastSeq: terminalSeq, Status: status, StopReason: stopReason, ErrorCode: errCode,
		UsageJSON: usageJSON, FirstTokenMS: firstTokenMS, EndedAt: ended,
	}); err != nil {
		glog.Warnw(context.Background(), "store: finish run failed", "session", sessionID, "run", runID, "err", err)
		return
	}
	// 消息物化：**整会话**重铺（E4）。不在这里传本轮窗口——`ordinal` 是"会话内第几条"，
	// 只重铺本轮等于让序号在每轮里从 1 重新数；而且白名单投影只认 `user_message`/`message`
	// 两类事件，"只重铺那一轮"省下的代价远小于它引入的第二套规则。
	pl.MaterializeMessages(sessionID)
}

// eventsInWindow 取 `[lo, hi]` 闭区间内的事件（入参已按 seq 升序）。
// `hi < 0` = 开区间（M2 起进行中的轮次 last_seq 未知）——此时只按 lo 切。
func eventsInWindow(evs []model.EventEntity, lo, hi int64) []model.EventEntity {
	out := make([]model.EventEntity, 0, len(evs))
	for _, e := range evs {
		if e.Seq < lo {
			continue
		}
		if hi >= 0 && e.Seq > hi {
			continue
		}
		out = append(out, e)
	}
	return out
}

// decodePayload 把 events.payload_json 解码回 map（历史/WS 载荷形态），坏 JSON 回落原文串。
func DecodePayload(raw string) any {
	if raw == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		return v
	}
	return raw
}

// terminalRunStatus 终态事件类型 → run 终态。
func TerminalRunStatus(typ string) string {
	switch typ {
	case BusRunEnded:
		return SessionStatusCompleted
	case BusRunError:
		return SessionStatusFailed
	case BusRunCancelled:
		return SessionStatusCancelled
	}
	return ""
}

// RunClosingStatus 判断一条事件是否**收口**当前轮，返回该轮的终态取值（空串 = 不收口）。
//
// 两支：
//   - 终态事件（`TerminalRunStatus`）——实时路径的常规收口；
//   - `run.status(interrupted)`——**恢复路径**的收口（ADR-8 的后半）。它不在 10 个活体状态里，
//     所以必须按**载荷**判，不能只看 type。
//
// 为什么两支要落在同一个函数里：`publish.go` 的「是否物化 runs」与 `closeRunsWithTerminal` 的
// 「日志里有没有收口事实」必须用**同一支判据**。各写一遍的话，恢复收口会只在 live 路径生效
// （重建出来的读模型里那一轮仍是 `running`），而两条路各自都"看起来在工作"。
func RunClosingStatus(typ string, payload map[string]any) string {
	if s := TerminalRunStatus(typ); s != "" {
		return s
	}
	if typ != BusRunStatus || payload == nil {
		return ""
	}
	if v, _ := payload["status"].(string); v == RunStatusInterrupted {
		return SessionStatusInterrupted
	}
	return ""
}

// runStartTime 取行里的起点时刻；零值（老数据没写 / 行是手工种的）才回落到窗口首事件时刻。
func runStartTime(row time.Time, fallback time.Time) time.Time {
	if !row.IsZero() {
		return row
	}
	return fallback
}
