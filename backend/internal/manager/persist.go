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
//   - publish 以 store.AppendEvent 事务内分配的全局 seq 为权威，bus 用 PublishAt 镜像同一号序；
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
// 注入 eventbus（seq 原样 PublishAt）——重启后 bus 与 DB 共用同一全局号序，
// WS/断线续传游标不漂移，旧会话仍可回放。返回恢复的会话数与注入事件数。
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
			ID: sess.ID, AgentID: sess.AgentID, Cwd: sess.Cwd,
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

	// 事件按全局 seq 升序注入（跨会话也保持升序，保证 PublishAt 不倒退/不回退取号）。
	if s.bus != nil {
		var after int64
		for {
			evs, more, err := s.st.EventsAllAfter(ctx, after, 2000)
			if err != nil {
				glog.Warnw(ctx, "restore: read events failed", "err", err)
				break
			}
			for _, ev := range evs {
				ver[ev.SessionID]++
				s.bus.PublishAt(ev.Seq, ev.ID, ev.Type, ev.SessionID, ver[ev.SessionID], decodePayload(ev.PayloadJSON))
				injected++
				after = ev.Seq
			}
			if !more {
				break
			}
		}
	}
	// §6：重启前排队未投递的输入一律作废——它们等的那一轮已随进程消失，自动重放等于替用户
	// 按下发送（且上下文已断）。保留行仅作审计（见 inputqueue.go）。
	s.staleQueuedInputsOnRestore(ctx)
	glog.Infow(ctx, "store restored", "sessions", sessions, "events", injected)
	return sessions, injected, nil
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
		AgentID:            rec.AgentID,
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
// user_text 取窗口首条用户消息；first/last_seq 记录窗口（失败轮输入/统计精确回溯）。
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
	// user_text 优先取窗口内首条用户消息。
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
	// 首条用户消息 seq 作为 run 起点的 first_seq（更贴近「轮」语义：一轮 = 一次用户输入）。
	for _, e := range evs {
		if e.Type == BusUserMessage {
			firstSeq = e.Seq
			break
		}
	}

	started := evs[0].CreatedAt
	ended := evs[len(evs)-1].CreatedAt
	// 首令牌耗时：首个 assistant 输出（文本/思考/工具）事件相对窗口起点的时刻；无输出则为 0。
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
