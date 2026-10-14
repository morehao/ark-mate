package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/object/objusage"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// registerSession 登记一个新会话，返回记录指针。
// EnableStore 启用 SQLite 持久层（P1 live 接线）。启用后：
//   - publish 以 store.AppendEvent 事务内分配的**会话内位次**为权威，bus 用 PublishAt 镜像同一号序；
//   - registerSession/markSession/UpdateSession 同步写库（sessions 为 DB 权威元数据镜像）；
//   - transcript recorder 视为停写（serve 装配时不再 SetTranscriptDir）。
//
// 须在 RestoreFromStore 之前、任何会话动作之前调用。
func (s *Service) EnableStore(st *store.Store) {
	s.st = st
	// T5 的配置指纹随持久层一起装载（它并进代际键，重启后必须仍然生效——
	// 否则重启会把"已物化的清单"当成"没有清单"，下一轮凭空重换代际）。
	s.loadRuntimeConfigDigests(context.Background())
	// 装配期把启动面与当前声明对齐一次（v12：启动参数/内联内容/凭据 env 都由 adapter 经
	// `resource.Delivery` 装卸）。重启后平台必须重新交付——否则上一轮的交付物与新声明可能分叉，
	// 而界面显示的是"已同步"。
	if ids, err := s.runtimeIDsForSync(""); err == nil {
		for _, id := range ids {
			if _, serr := s.SyncRuntime(context.Background(), id); serr != nil {
				glog.Warnw(context.Background(), "runtime sync: 装配期同步失败", "runtime", id, "err", serr)
			}
		}
	}
}

// RestoreFromStore 启动恢复（sqlite 模式）：从 DB 载入会话记录并把全部已落库事件
// 注入 eventbus（seq 原样 PublishAt）——重启后 bus 与 DB 共用**同一套会话内位次**，
// WS/断线续传游标不漂移，旧会话仍可回放。返回恢复的会话数与注入事件数。
//
// 注入顺序（v21）：按 `(session_id, seq)` 复合游标逐页取（`EventsAllBySession`），
// 于是**每个会话各自升序**。v21 之前这里按全局 seq 升序——那个顺序的意义是"保证全局单号序
// 不被 PublishAt 改写"，而 v21 起 `nextSeq` 是每会话一张表，跨会话的相对顺序不再影响任何号；
// 仍要保证的是"组内升序"（复合同序恰好同时给了分组与组内有序）。
func (s *Service) RestoreFromStore(ctx context.Context) (int, int, error) {
	if s.st == nil {
		return 0, 0, nil
	}
	list, err := s.st.ListSessions(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("restore: list sessions: %w", err)
	}
	s.mu.Lock()
	s.restoring = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.restoring = false
		s.mu.Unlock()
	}()

	sessions, injected := 0, 0
	interrupted := 0
	ver := map[string]int64{}
	for _, sess := range list {
		rec := &SessionRec{
			ID: sess.ID, Cwd: sess.Cwd,
			WorkspaceID:        sess.WorkspaceID,
			WorkspaceTemporary: sess.WorkspaceTemporary, ProviderID: sess.ProviderID,
			ModelID: sess.ModelID, RuntimeID: runtime.NormalizeID(sess.RuntimeID),
			RuntimeSessionID: sess.RuntimeSessionID,
			Title:            sess.Title, Pinned: sess.Pinned, Archived: sess.Archived,
			Status: sess.Status, Origin: sess.Origin, CapabilityJSON: sess.CapabilityJSON,
			// **恢复会话钉住的指纹**：漏了这一段，重启后 rec.Fingerprint 就是空的 →
			// 代际键退回"未声明"形态，会话悄悄换到默认能力集（正是 M3 要防的漂移）。
			Fingerprint: sess.CapabilityFingerprint,
			CreatedAt:   sess.CreatedAt, UpdatedAt: sess.UpdatedAt,
		}
		s.registerSession(rec) // restoring=true → 不重复回写 DB
		sessions++
		// interrupted 收敛：残留 in_progress（崩溃/重启中断，无终态）→ 可操作态（列表可停止/重发）。
		// 事件保持原样：续聊时 materializeRun 以「上一 run 之后」窗口重建本轮，不受影响。
		if rec.Status == SessionStatusInProgress {
			s.markSession(rec.ID, SessionStatusInterrupted)
			interrupted++
		}
	}

	// 事件按 `(session_id, seq)` 升序注入（会话内升序是唯一有意义的顺序；见函数注释）。
	if s.bus != nil {
		var (
			afterSession string
			afterSeq     int64 = -1
		)
		for {
			evs, more, err := s.st.EventsAllBySession(ctx, afterSession, afterSeq, 2000)
			if err != nil {
				glog.Warnw(ctx, "restore: read events failed", "err", err)
				break
			}
			for _, ev := range evs {
				ver[ev.SessionID]++
				s.bus.PublishAt(ev.SessionID, ev.Seq, ev.ID, ev.Type, ver[ev.SessionID], decodePayload(ev.PayloadJSON))
				injected++
				afterSession, afterSeq = ev.SessionID, ev.Seq
			}
			if !more {
				break
			}
		}
	}
	// 读模型重建（v21）：迁移把 `meta.read_models_rebuild` 置成刚跑过的版本号，表示"位次口径换了、
	// 旧读模型里的窗口列已经不可信"。此处消费它并清标记——**只有这一条路径**（见 model/enum.go）。
	s.rebuildStaleReadModels(ctx)
	// §6：重启前排队未投递的输入一律作废——它们等的那一轮已随进程消失，自动重放等于替用户
	// 按下发送（且上下文已断）。保留行仅作审计（见 inputqueue.go）。
	s.staleQueuedInputsOnRestore(ctx)
	glog.Infow(ctx, "store restored", "sessions", sessions, "events", injected)
	return sessions, injected, nil
}

// rebuildStaleReadModels 消费 `meta.read_models_rebuild`：有值 = 上一次迁移改动了事件位次口径，
// 全部会话的读模型（M1：messages；M2 起还有 runs）必须按新位次重建，然后清掉标记。
//
// best-effort：任何一步失败只 WARN（标记留着，下次启动重试——比"半个库重建了一半还清了标记"好）。
//
// 它与 `mate doctor --rebuild-read-models` 共用 `rebuildAllReadModels`：区别只在**触发条件**
// （这边看标记、那边是人工显式要求），而"重建什么"必须只有一份实现——两份的话，
// "doctor 重建出来的"与"启动重建出来的"就能不一样，而那是最难查的一类不一致（J7 的可重建性
// 要求"同一份 events 得到同一组读模型"，与谁触发无关）。
func (s *Service) rebuildStaleReadModels(ctx context.Context) {
	if s.st == nil {
		return
	}
	ver, ok, err := s.st.GetMeta(ctx, model.MetaKeyReadModelsRebuild)
	if err != nil {
		glog.Warnw(ctx, "read models: 读重建标记失败", "err", err)
		return
	}
	if !ok || ver == "" {
		return
	}
	total, failed, err := s.rebuildAllReadModels(ctx)
	if err != nil {
		glog.Warnw(ctx, "read models: 列会话失败（重建跳过，标记保留）", "err", err)
		return
	}
	if failed > 0 {
		glog.Warnw(ctx, "read models: 重建未全部成功，标记保留待下次重试", "failed", failed, "total", total)
		return
	}
	if err := s.st.DeleteMeta(ctx, model.MetaKeyReadModelsRebuild); err != nil {
		glog.Warnw(ctx, "read models: 清重建标记失败", "err", err)
		return
	}
	glog.Infow(ctx, "read models rebuilt", "migration", ver, "sessions", total)
}

// rebuildAllReadModels 重建**全部会话**的读模型：返回会话总数与失败数。
//
// 单个会话失败**不中断**（一个会话的坏数据不该让其余会话失去自愈机会），失败只计 WARN；
// 调用方按 `failed > 0` 决定"重建标记/清理"的去留。
func (s *Service) rebuildAllReadModels(ctx context.Context) (total, failed int, err error) {
	if s.st == nil {
		return 0, 0, nil
	}
	list, err := s.st.ListSessions(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	for _, sess := range list {
		if rerr := s.RebuildSessionReadModels(ctx, sess.ID); rerr != nil {
			failed++
			glog.Warnw(ctx, "read models: 会话重建失败", "session", sess.ID, "err", rerr)
		}
	}
	return len(list), failed, nil
}

// RebuildSessionReadModels 用 `events` 重建某会话的全部读模型（J7：读模型整体可重建）。
//
// 三段：① `runs` 补缺（`run.started` 事件是权威：缺行→补一行）；② 把**日志里已有终态**却还
// 停在 running 的行收口（终态那次落库失败的自愈）；③ `messages` 整会话重铺。
//
// ①② 只认事件、不认内存：这一轮结束没有，看的是它自己的终态事件在不在日志里。因此
// `openRun`/`FinishRun` 这两条 best-effort 路径失败留下的洞都能被下一次重建补上（J7 的字面意思）。
//
// 做法：读该会话全部事件（会话内位次升序，一次读完）与 runs（按窗口起点排）→ 交给
// `replaceSessionMessages` **整会话重铺**（一个事务里先删该会话全部 messages 再插入）。
// 与 live 路径共用同一段构建代码（`buildSessionMessageRows`）——"运行时"与"重建"分叉过一次，
// 代价是读模型里有两套规则，而两套规则的分歧只会在"重建之后看起来不一样"时暴露。
//
// 整会话重铺而不是逐轮重铺（E4）：`ordinal` 是"会话内第几条"，只有整会话重排才能保证它稠密；
// 顺带把"没有归属 run 的孤儿行"（旧口径留下的）一起清掉，而不是永远留在库里当幽灵消息。
//
// 幂等：同一份 events 反复跑得到**内容相同**的行集合（`objid.New` 会铸新 id，但读模型的身份不
// 承担外部引用——搜索的锚点是 `seq`，`messages.id` 不出现在任何 wire 契约里）。
func (s *Service) RebuildSessionReadModels(ctx context.Context, sessionID string) error {
	if s.st == nil {
		return nil
	}
	all, err := s.sessionEvents(ctx, sessionID)
	if err != nil {
		return err
	}
	// ① 补齐缺失的 run 行（开轮落行是 best-effort：不补的话，那一轮会永久从轮次明细里消失——
	// 终态找不到开轮行就跳过——而 events 里它明明还在）。
	if added, err := s.reconcileRunRows(ctx, sessionID, all); err != nil {
		return fmt.Errorf("read models: reconcile runs: %w", err)
	} else if added {
		glog.Infow(ctx, "read models: 从 run.started 补出缺失的 runs 行", "session", sessionID)
	}
	runs, err := s.sessionRuns(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("read models: list runs: %w", err)
	}
	// ② 用日志里的终态把还停在 running 的行收口。收口会改 `last_seq`，而窗口夹取要用新值，
	// 所以改完必须重读一次。
	if closed := s.closeRunsWithTerminal(sessionID, all, runs); closed {
		glog.Infow(ctx, "read models: 用日志里的终态事件收口了停在 running 的行", "session", sessionID)
		if runs, err = s.sessionRuns(ctx, sessionID); err != nil {
			return fmt.Errorf("read models: list runs: %w", err)
		}
	}
	// ③ 整会话重铺 messages（"按窗口切片 + 序号跨轮连续 + 空正文丢弃"全在
	// `buildSessionMessageRows` 里；进行中的轮窗口也在那里夹）。
	return s.replaceSessionMessages(ctx, sessionID, all, runs)
}

// sessionRuns 读某会话的 runs 并**按窗口起点排序**。
//
// 为什么不用 `ListRuns` 自己的顺序：它按 `started_at` 排（那是给人看的"第几轮"），而窗口夹取
// 只能按位次来——时钟回拨时 `started_at` 的顺序说了不算，`first_seq` 说了算。
func (s *Service) sessionRuns(ctx context.Context, sessionID string) ([]model.RunEntity, error) {
	runs, err := s.st.ListRuns(ctx, sessionID)
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
func (s *Service) reconcileRunRows(ctx context.Context, sessionID string, all []model.EventEntity) (bool, error) {
	rows, err := s.st.ListRuns(ctx, sessionID)
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
		if err := s.st.CreateRun(ctx, model.RunEntity{
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

// closeRunsWithTerminal 把"日志里已经有本轮终态事件、行却还停在 running"的 run 收口。
//
// 这是给终态物化**失败**（`FinishRun` 写库失败只 WARN）自愈用的：那一轮的开轮行还在、终态事件
// 也在，缺的只是那次 UPDATE。判据不是"猜这一轮结束了"——而是日志里那条终态事件的载荷
// `run_id` **就是这一轮的身份**（同一支值，从开轮一路带到这里）。
//
// 崩溃中断的轮次刻意不受影响：它们没有终态事件，因此仍然留在 running（见 RunEntity 注释）。
func (s *Service) closeRunsWithTerminal(sessionID string, all []model.EventEntity, runs []model.RunEntity) bool {
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
		if terminalRunStatus(e.Type) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(e.PayloadJSON), &m); err != nil {
			continue
		}
		id, _ := m["run_id"].(string)
		if id == "" || !open[id] {
			continue
		}
		// 复用 live 路径的收口函数（窗口 = `[first_seq, 终态位次]`、user_text 取行里的、首令牌耗时
		// 相对行里的起点）——"重建"与"运行时"必须是同一段代码，否则两条路会慢慢分叉。
		s.materializeRun(sessionID, e.Type, m, e.Seq)
		delete(open, id)
		closed = true
	}
	return closed
}

// sessionEvents 读某会话的全部事件（位次升序；跨页循环，页大小与恢复注入一致）。
func (s *Service) sessionEvents(ctx context.Context, sessionID string) ([]model.EventEntity, error) {
	var all []model.EventEntity
	after := int64(-1)
	for {
		batch, more, err := s.st.EventsAfter(ctx, sessionID, after, 2000)
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
func decodePayload(raw string) any {
	if raw == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		return v
	}
	return raw
}

// persistSession 把会话记录同步写库（best-effort：失败告警不阻断会话流程）。
func (s *Service) persistSession(rec *SessionRec) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.st.UpsertSession(ctx, model.SessionEntity{
		ID:                 rec.ID,
		Cwd:                rec.Cwd,
		WorkspaceID:        rec.WorkspaceID,
		WorkspaceTemporary: rec.WorkspaceTemporary,
		ProviderID:         rec.ProviderID,
		ModelID:            rec.ModelID,
		RuntimeID:          rec.RuntimeID,
		RuntimeSessionID:   rec.RuntimeSessionID,
		Title:              rec.Title,
		Pinned:             rec.Pinned,
		Archived:           rec.Archived,
		Status:             rec.Status,
		Origin:             rec.Origin,
		CapabilityJSON:     rec.CapabilityJSON,
		// **钉住的指纹**（代际）：普通持久化写的就是记录里的那个值（会话一旦建立就不再重新解析），
		// 所以这里不会"每次落库都把指纹刷成当前值"；只有 M3-6 的轮次边界迁移会显式改写它。
		CapabilityFingerprint: rec.Fingerprint,
		CreatedAt:             rec.CreatedAt,
		UpdatedAt:             rec.UpdatedAt,
	})
	if err != nil {
		glog.Warnw(context.Background(), "store: upsert session failed", "session", rec.ID, "err", err)
	}
}

// persistMemoryAdd 写一条权限记忆（permission_memories 表；同键覆盖，since 为授予时刻）。
func (s *Service) persistMemoryAdd(sessionID, title, kind, decision string, since time.Time) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.st.AddMemory(ctx, model.PermissionMemoryEntity{
		ID:        objid.New(objid.PermissionMemory),
		SessionID: sessionID, Title: title, Kind: kind, Decision: decision, CreatedAt: since,
	}); err != nil {
		glog.Warnw(context.Background(), "store: add permission memory failed", "session", sessionID, "title", title, "err", err)
	}
}

// persistMemoryDel 撤销一条权限记忆。
func (s *Service) persistMemoryDel(sessionID, title, kind string) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.st.DeleteMemory(ctx, sessionID, title, kind); err != nil {
		glog.Warnw(context.Background(), "store: delete permission memory failed", "session", sessionID, "title", title, "err", err)
	}
}

// persistMemoryDelAll 一键撤销整会话权限记忆。
func (s *Service) persistMemoryDelAll(sessionID string) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.st.DeleteSessionMemories(ctx, sessionID); err != nil {
		glog.Warnw(context.Background(), "store: delete session memories failed", "session", sessionID, "err", err)
	}
}

// memoryRows 读全库权限记忆（供 sqlite 启动恢复装载；无 store 时返回空）。
func (s *Service) memoryRows(ctx context.Context) ([]model.PermissionMemoryEntity, error) {
	if s.st == nil {
		return nil, nil
	}
	return s.st.ListAllMemories(ctx)
}

// terminalRunStatus 终态事件类型 → run 终态。
func terminalRunStatus(typ string) string {
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

// openRun 开一轮：把 run 行以 **running** 写进库（v22 起一轮在**开始**时就有行）。
//
// 与 `materializeRun` 的分工：本函数写"这一轮开始了"（first_seq = `run.started` 的位次、
// last_seq = -1），终态那次只 UPDATE。best-effort：失败仅告警——读模型缺一行不该阻断会话，
// 而 events 里那条 `run.started` 还在（`reconcileRunRows` 会把它补成行，J7）。
//
// `started_at` 取 **`run.started` 事件的落库时刻**（回读那一行），不是 `time.Now()`：本轮起点
// 的唯一事实源是那条事件，各写各的会让 `runs.started_at` 与事件时间分叉，而 `first_token_ms`
// 正是相对它算的。
//
// **回读到那条事件才开轮**（位次与 type 都对得上）：开轮行是"查询 `run.started` 得到的物化行"，
// 事件不在（append 失败退化、或位次来自总线而非库）时插一行等于给这一轮编一个不存在的起点——
// 终态收口会把**别的事件**当成它的窗口。宁可没有行（那条 WARN 就是信号）。
func (s *Service) openRun(sessionID, runID, text string, firstSeq int64) {
	if s.st == nil || runID == "" || firstSeq < 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	evs, _, err := s.st.EventsAfter(ctx, sessionID, firstSeq-1, 1)
	if err != nil || len(evs) != 1 || evs[0].Seq != firstSeq || evs[0].Type != BusRunStarted {
		glog.Warnw(context.Background(), "store: 开轮前回读 run.started 失败，跳过开轮行",
			"session", sessionID, "run", runID, "first_seq", firstSeq, "err", err)
		return
	}
	started := evs[0].CreatedAt
	r := model.RunEntity{
		ID: runID, SessionID: sessionID, UserText: text,
		Status: SessionStatusInProgress,
		// first_seq 是本轮 `run.started` 的位次（窗口起点从此由行给出，不再由"上一轮的末尾"推导）。
		FirstSeq: firstSeq,
		// last_seq = -1 = 还没收口：**0 是合法位次**（v21），拿它当"未知"会让进行中的轮看起来
		// 已经覆盖了会话第 0 条事件。
		LastSeq:   -1,
		StartedAt: started,
	}
	if err := s.st.CreateRun(ctx, r); err != nil {
		glog.Warnw(context.Background(), "store: open run failed", "session", sessionID, "run", runID, "err", err)
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
func (s *Service) runRowFor(ctx context.Context, sessionID, runID string) (model.RunEntity, bool) {
	if runID != "" {
		if r, ok, err := s.st.RunByID(ctx, runID); err != nil {
			glog.Warnw(context.Background(), "store: 读 run 行失败", "session", sessionID, "run", runID, "err", err)
		} else if ok {
			return r, true
		}
	}
	rows, err := s.st.RunningRuns(ctx, sessionID)
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

// materializeRun 在 sqlite 模式下把**本轮窗口**收口：窗口 = `[run.first_seq, terminalSeq]`，
// 起点由开轮行给出（v22 起不再由"上一 run 的 last_seq + 轮界裁剪规则"推导——那套推导曾参与
// 身份构造，规则一变历史轮次的 id 就跟着变）。
//
// 三件写在一次调用里：① UPDATE 行的终态与窗口（含 last_seq）；② user_text 从行读（开轮已写）；
// ③ 用窗口事件重建 messages。幂等：行已终态时重复调用只会重算同一窗口。
// best-effort：失败仅告警（events 仍是唯一事实源）。
func (s *Service) materializeRun(sessionID, typ string, payload map[string]any, terminalSeq int64) {
	if s.st == nil {
		return
	}
	status := terminalRunStatus(typ)
	if status == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runID := ""
	if payload != nil {
		runID, _ = payload["run_id"].(string)
	}
	row, ok := s.runRowFor(ctx, sessionID, runID)
	if !ok {
		// M2 之前开出的轮（或直发终态事件的调用方）没有开轮行：没有起点就没有窗口。
		glog.Warnw(context.Background(), "store: 找不到本轮的开轮行，跳过 runs 物化",
			"session", sessionID, "run", runID, "type", typ)
		return
	}
	runID = row.ID

	evs, more, err := s.st.EventsAfter(ctx, sessionID, row.FirstSeq-1, 5000)
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
	if s.orch != nil {
		if u, ok := s.orch.LatestUsage(sessionID); ok {
			if b, err := json.Marshal(u); err == nil {
				usageJSON = string(b)
			}
		}
	}
	if err := s.st.FinishRun(ctx, runID, model.RunFinishPatch{
		LastSeq: terminalSeq, Status: status, StopReason: stopReason, ErrorCode: errCode,
		UsageJSON: usageJSON, FirstTokenMS: firstTokenMS, EndedAt: ended,
	}); err != nil {
		glog.Warnw(context.Background(), "store: finish run failed", "session", sessionID, "run", runID, "err", err)
		return
	}
	// 消息物化：**整会话**重铺（E4）。不在这里传本轮窗口——`ordinal` 是"会话内第几条"，
	// 只重铺本轮等于让序号在每轮里从 1 重新数；而且白名单投影只认 `user_message`/`message`
	// 两类事件，"只重铺那一轮"省下的代价远小于它引入的第二套规则。
	s.materializeMessages(sessionID)
}

// runStartTime 取行里的起点时刻；零值（老数据没写 / 行是手工种的）才回落到窗口首事件时刻。
func runStartTime(row time.Time, fallback time.Time) time.Time {
	if !row.IsZero() {
		return row
	}
	return fallback
}

// ListRuns 列出某会话的轮次明细（runs 表，started_at 升序；失败轮重试/统计用）。
// 内存（无 store）模式下返回空列表（runs 是 sqlite 读模型）。
func (s *Service) ListRuns(ctx context.Context, id string) ([]dtosession.RunResp, error) {
	if s.st == nil {
		return []dtosession.RunResp{}, nil
	}
	rows, err := s.st.ListRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]dtosession.RunResp, 0, len(rows))
	for _, r := range rows {
		resp := dtosession.RunResp{
			ID: r.ID, UserText: r.UserText, Status: r.Status,
			ErrorCode: r.ErrorCode, StopReason: r.StopReason,
			FirstSeq: r.FirstSeq, LastSeq: r.LastSeq, FirstTokenMS: r.FirstTokenMS,
			StartedAt: r.StartedAt.UTC().Format(time.RFC3339Nano),
		}
		if !r.EndedAt.IsZero() {
			resp.EndedAt = r.EndedAt.UTC().Format(time.RFC3339Nano)
		}
		if r.UsageJSON != "" {
			var u objusage.UsageInfo
			if err := json.Unmarshal([]byte(r.UsageJSON), &u); err == nil && u.Reported() {
				resp.Usage = &dtosession.RunUsageResp{
					Used: u.Used, Size: u.Size, CostAmount: u.CostAmount, CostCurrency: u.CostCurrency,
				}
			}
		}
		out = append(out, resp)
	}
	return out, nil
}
