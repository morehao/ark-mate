package manager

import (
	"context"
	"encoding/json"
	"fmt"
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
	list, err := s.st.ListSessions(ctx, nil)
	if err != nil {
		glog.Warnw(ctx, "read models: 列会话失败（重建跳过）", "err", err)
		return
	}
	failed := 0
	for _, sess := range list {
		if err := s.RebuildSessionReadModels(ctx, sess.ID); err != nil {
			failed++
			glog.Warnw(ctx, "read models: 会话重建失败", "session", sess.ID, "err", err)
		}
	}
	if failed > 0 {
		glog.Warnw(ctx, "read models: 重建未全部成功，标记保留待下次重试", "failed", failed, "total", len(list))
		return
	}
	if err := s.st.DeleteMeta(ctx, model.MetaKeyReadModelsRebuild); err != nil {
		glog.Warnw(ctx, "read models: 清重建标记失败", "err", err)
		return
	}
	glog.Infow(ctx, "read models rebuilt", "migration", ver, "sessions", len(list))
}

// RebuildSessionReadModels 用 `events` 重建某会话的全部读模型（J7：读模型整体可重建）。
//
// M1 阶段只重建 `messages`；`runs` 的重建在 M2 接上（那时 run 变成实体、有自己的起点事件，
// 重建才有权威依据——现在 runs 的 id 还是 `r-<session>-<first_seq>` 这种从窗口推导出来的东西）。
//
// 做法：读该会话全部事件（会话内位次升序，一次读完）→ 按每个 run 的 `[first_seq, last_seq]`
// 窗口切片 → **先清该 run 的旧行再插新行**。清是必须的：位次口径换了之后，某个窗口在新口径下
// 可能一行都不产出，而"空的批次"在插入路径里是 no-op，会把上一代的旧行留在库里。
//
// 幂等：同一份 events 反复跑得到同一组行（`objid.New` 会铸新 id，但 rows 的**内容**等价；
// 读模型的身份不承担外部引用——`messages.id` 不出现在任何 wire 契约里）。
func (s *Service) RebuildSessionReadModels(ctx context.Context, sessionID string) error {
	if s.st == nil {
		return nil
	}
	runs, err := s.st.ListRuns(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("read models: list runs: %w", err)
	}
	all, err := s.sessionEvents(ctx, sessionID)
	if err != nil {
		return err
	}
	// 整会话先清空再按 run 重铺：这样"没有归属 run 的孤儿行"（旧口径留下的）也会一起消失，
	// 而不是永远留在库里当幽灵消息。
	if err := s.st.DeleteMessagesBySession(ctx, sessionID); err != nil {
		return fmt.Errorf("read models: clear messages: %w", err)
	}
	for _, r := range runs {
		window := eventsInWindow(all, r.FirstSeq, r.LastSeq)
		if err := s.rebuildRunMessages(ctx, sessionID, r.ID, window); err != nil {
			return fmt.Errorf("read models: rebuild run %s: %w", r.ID, err)
		}
	}
	return nil
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

// materializeRun 在 sqlite 模式下把「上一 run 之后到当前终态」的事件窗口物化为 runs 行：
// 窗口**从本轮首条用户消息起算**（一轮 = 一次用户输入；窗口起点与本轮输入之间夹着的会话级
// 事件不属本轮，见下方轮界裁剪）；user_text 取窗口首条用户消息；first/last_seq 记录窗口
// （失败轮输入/统计精确回溯）。
// 幂等：窗口无新事件（重复调用/无 run）时直接返回。best-effort：失败仅告警。
func (s *Service) materializeRun(sessionID, typ string, payload map[string]any) {
	if s.st == nil {
		return
	}
	status := terminalRunStatus(typ)
	if status == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	last, err := s.st.RunLastSeq(ctx, sessionID)
	if err != nil {
		glog.Warnw(context.Background(), "store: run last seq failed", "session", sessionID, "err", err)
		return
	}
	evs, more, err := s.st.EventsAfter(ctx, sessionID, last, 5000)
	if err != nil {
		glog.Warnw(context.Background(), "store: run window read failed", "session", sessionID, "err", err)
		return
	}
	if more {
		glog.Warnw(context.Background(), "store: run window 超 5000 条被截断", "session", sessionID)
	}
	if len(evs) == 0 {
		return // 无新事件（重复终态调用等）→ 幂等跳过
	}
	// 轮界裁剪：窗口起点是「上一 run 的 last_seq」，它与本轮首条用户消息之间可能夹着**会话级**
	// 事件（session.created / session.resumed / config_options 等）——它们不属于这一轮。不裁掉
	// 有两个实测后果（2026-10-14 用户报「一问却有三条 messages」）：
	//  ① messages 物化把 session.created 当成一条 assistant 时间线 → 落出一条正文为空的行；
	//  ② runID 取裁剪前的首事件 seq，与随后按「首条用户消息」写下的 first_seq 错位
	//     （id 是 r-…-27 而 first_seq=28），本轮时长/首令牌耗时也把会话前置算进了这一轮。
	// 找不到用户消息（异常路径：没有 prompt 的终态）则保持原窗口——宁可多算，也不丢一轮。
	for i, e := range evs {
		if e.Type == BusUserMessage {
			evs = evs[i:]
			break
		}
	}
	firstSeq, lastSeq := evs[0].Seq, evs[len(evs)-1].Seq
	runID := fmt.Sprintf("r-%s-%d", sessionID, firstSeq)

	userText, stopReason, errCode := "", "", ""
	if payload != nil {
		if t, _ := payload["text"].(string); t != "" {
			userText = t
		}
		if st, _ := payload["stop_reason"].(string); st != "" {
			stopReason = st
		}
		if cd, _ := payload["code"].(string); cd != "" {
			errCode = cd
		}
	}
	// user_text 优先取窗口内首条用户消息（覆盖 payload 里的原始文本）。
	for _, e := range evs {
		if e.Type == BusUserMessage {
			var m map[string]any
			if err := json.Unmarshal([]byte(e.PayloadJSON), &m); err == nil {
				if t, _ := m["text"].(string); t != "" {
					userText = t
					break
				}
			}
		}
	}

	started := evs[0].CreatedAt
	ended := evs[len(evs)-1].CreatedAt
	// 首令牌耗时：首个 assistant 输出（文本/思考/工具）事件相对**本轮起点（首条用户消息）**的
	// 时刻；无输出则为 0（与 dto 里 first_token_ms 的口径一致）。
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
	r := model.RunEntity{
		ID: runID, SessionID: sessionID, UserText: userText, Status: status,
		ErrorCode: errCode, StopReason: stopReason, UsageJSON: usageJSON,
		FirstSeq: firstSeq, LastSeq: lastSeq, FirstTokenMS: firstTokenMS,
		StartedAt: started, EndedAt: ended,
	}
	if err := s.st.CreateRun(ctx, r); err != nil {
		glog.Warnw(context.Background(), "store: create run failed", "session", sessionID, "run", runID, "err", err)
		return
	}
	// 终态补齐（CreateRun 直接带终态写入，FinishRun 幂等再走一遍以统一 ended 口径）。
	_ = s.st.FinishRun(ctx, runID, status, stopReason, errCode, usageJSON, ended)
	// 消息物化：同窗口 events → messages 行（user 原文 + assistant 文本片段/时间线/工具最终态）。
	s.materializeMessages(sessionID, runID, evs)
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
