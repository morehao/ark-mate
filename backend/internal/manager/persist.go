package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/morehao/ark-forge/internal/dto/dtoconversation"
	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/ark-forge/pkg/object/objusage"
)

// registerConversation 登记一个新会话，返回记录指针。
// EnableStore 启用 SQLite 持久层（P1 live 接线）。启用后：
//   - publish 以 store.AppendEvent 事务内分配的全局 seq 为权威，bus 用 PublishAt 镜像同一号序；
//   - registerConversation/markConversation/UpdateConversation 同步写库（conversations 为 DB 权威元数据镜像）；
//   - transcript recorder 视为停写（serve 装配时不再 SetTranscriptDir）。
//
// 须在 RestoreFromStore 之前、任何会话动作之前调用。
func (s *Service) EnableStore(st *store.Store) { s.st = st }

// RestoreFromStore 启动恢复（sqlite 模式）：从 DB 载入会话记录并把全部已落库事件
// 注入 eventbus（seq 原样 PublishAt）——重启后 bus 与 DB 共用同一全局号序，
// WS/断线续传游标不漂移，旧会话仍可回放。返回恢复的会话数与注入事件数。
func (s *Service) RestoreFromStore(ctx context.Context) (int, int, error) {
	if s.st == nil {
		return 0, 0, nil
	}
	list, err := s.st.ListConversations(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("restore: list conversations: %w", err)
	}
	s.mu.Lock()
	s.restoring = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.restoring = false
		s.mu.Unlock()
	}()

	conversations, injected := 0, 0
	interrupted := 0
	ver := map[string]int64{}
	for _, sess := range list {
		rec := &ConversationRec{
			ID: sess.ID, AgentID: sess.AgentID, Cwd: sess.Cwd,
			WorkspaceTemporary: sess.WorkspaceTemporary, Provider: sess.Provider,
			Model: sess.Model, RuntimeHint: sess.RuntimeHint,
			ProviderSessionID: sess.ProviderSessionID,
			Title:             sess.Title, Pinned: sess.Pinned, Archived: sess.Archived,
			Status: sess.Status, CreatedAt: sess.CreatedAt, UpdatedAt: sess.UpdatedAt,
		}
		s.registerConversation(rec) // restoring=true → 不重复回写 DB
		conversations++
		// interrupted 收敛：残留 in_progress（崩溃/重启中断，无终态）→ 可操作态（列表可停止/重发）。
		// 事件保持原样：续聊时 materializeRun 以「上一 run 之后」窗口重建本轮，不受影响。
		if rec.Status == ConversationStatusInProgress {
			s.markConversation(rec.ID, ConversationStatusInterrupted)
			interrupted++
		}
	}

	// 事件按全局 seq 升序注入（跨会话也保持升序，保证 PublishAt 不倒退/不回退取号）。
	if s.bus != nil {
		var after int64
		for {
			evs, more, err := s.st.EventsAllAfter(ctx, after, 2000)
			if err != nil {
				slog.Default().Warn("restore: read events failed", "err", err)
				break
			}
			for _, ev := range evs {
				ver[ev.ConversationID]++
				s.bus.PublishAt(ev.Seq, ev.Type, ev.ConversationID, ver[ev.ConversationID], decodePayload(ev.PayloadJSON))
				injected++
				after = ev.Seq
			}
			if !more {
				break
			}
		}
	}
	slog.Default().Info("store restored", "conversations", conversations, "events", injected)
	return conversations, injected, nil
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

// persistConversation 把会话记录同步写库（best-effort：失败告警不阻断会话流程）。
func (s *Service) persistConversation(rec *ConversationRec) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.st.UpsertConversation(ctx, store.Conversation{
		ID:                 rec.ID,
		AgentID:            rec.AgentID,
		Cwd:                rec.Cwd,
		WorkspaceTemporary: rec.WorkspaceTemporary,
		Provider:           rec.Provider,
		Model:              rec.Model,
		RuntimeHint:        rec.RuntimeHint,
		ProviderSessionID:  rec.ProviderSessionID,
		Title:              rec.Title,
		Pinned:             rec.Pinned,
		Archived:           rec.Archived,
		Status:             rec.Status,
		CreatedAt:          rec.CreatedAt,
		UpdatedAt:          rec.UpdatedAt,
	})
	if err != nil {
		slog.Default().Warn("store: upsert conversation failed", "conversation", rec.ID, "err", err)
	}
}

// persistMemoryAdd 写一条权限记忆（permission_memories 表；同键覆盖，since 为授予时刻）。
func (s *Service) persistMemoryAdd(conversationID, title, kind, decision string, since time.Time) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.st.AddMemory(ctx, store.Memory{
		ConversationID: conversationID, Title: title, Kind: kind, Decision: decision, CreatedAt: since,
	}); err != nil {
		slog.Default().Warn("store: add permission memory failed", "conversation", conversationID, "title", title, "err", err)
	}
}

// persistMemoryDel 撤销一条权限记忆。
func (s *Service) persistMemoryDel(conversationID, title, kind string) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.st.DeleteMemory(ctx, conversationID, title, kind); err != nil {
		slog.Default().Warn("store: delete permission memory failed", "conversation", conversationID, "title", title, "err", err)
	}
}

// persistMemoryDelAll 一键撤销整会话权限记忆。
func (s *Service) persistMemoryDelAll(conversationID string) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.st.DeleteConversationMemories(ctx, conversationID); err != nil {
		slog.Default().Warn("store: delete conversation memories failed", "conversation", conversationID, "err", err)
	}
}

// memoryRows 读全库权限记忆（供 sqlite 启动恢复装载；无 store 时返回空）。
func (s *Service) memoryRows(ctx context.Context) ([]store.Memory, error) {
	if s.st == nil {
		return nil, nil
	}
	return s.st.ListAllMemories(ctx)
}

// terminalRunStatus 终态事件类型 → run 终态。
func terminalRunStatus(typ string) string {
	switch typ {
	case BusTurnEnded:
		return ConversationStatusCompleted
	case BusTurnError:
		return ConversationStatusFailed
	case BusRunCancelled:
		return ConversationStatusCancelled
	}
	return ""
}

// materializeRun 在 sqlite 模式下把「上一 run 之后到当前终态」的事件窗口物化为 runs 行：
// user_text 取窗口首条用户消息；first/last_seq 记录窗口（失败轮输入/统计精确回溯）。
// 幂等：窗口无新事件（重复调用/无 run）时直接返回。best-effort：失败仅告警。
func (s *Service) materializeRun(conversationID, typ string, payload map[string]any) {
	if s.st == nil {
		return
	}
	status := terminalRunStatus(typ)
	if status == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	last, err := s.st.RunLastSeq(ctx, conversationID)
	if err != nil {
		slog.Default().Warn("store: run last seq failed", "conversation", conversationID, "err", err)
		return
	}
	evs, more, err := s.st.EventsAfter(ctx, conversationID, last, 5000)
	if err != nil {
		slog.Default().Warn("store: run window read failed", "conversation", conversationID, "err", err)
		return
	}
	if more {
		slog.Default().Warn("store: run window 超 5000 条被截断", "conversation", conversationID)
	}
	if len(evs) == 0 {
		return // 无新事件（重复终态调用等）→ 幂等跳过
	}
	firstSeq, lastSeq := evs[0].Seq, evs[len(evs)-1].Seq
	runID := fmt.Sprintf("r-%s-%d", conversationID, firstSeq)

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
		if u, ok := s.orch.LatestUsage(conversationID); ok {
			if b, err := json.Marshal(u); err == nil {
				usageJSON = string(b)
			}
		}
	}
	r := store.Run{
		ID: runID, ConversationID: conversationID, UserText: userText, Status: status,
		ErrorCode: errCode, StopReason: stopReason, UsageJSON: usageJSON,
		FirstSeq: firstSeq, LastSeq: lastSeq, FirstTokenMS: firstTokenMS,
		StartedAt: started, EndedAt: ended,
	}
	if err := s.st.CreateRun(ctx, r); err != nil {
		slog.Default().Warn("store: create run failed", "conversation", conversationID, "run", runID, "err", err)
		return
	}
	// 终态补齐（CreateRun 直接带终态写入，FinishRun 幂等再走一遍以统一 ended 口径）。
	_ = s.st.FinishRun(ctx, runID, status, stopReason, errCode, usageJSON, ended)
	// 消息物化：同窗口 events → messages 行（user 原文 + assistant 文本片段/时间线/工具最终态）。
	s.materializeMessages(conversationID, runID, evs)
}

// ListRuns 列出某会话的轮次明细（runs 表，started_at 升序；失败轮重试/统计用）。
// 内存（无 store）模式下返回空列表（runs 是 sqlite 读模型）。
func (s *Service) ListRuns(ctx context.Context, id string) ([]dtoconversation.RunResp, error) {
	if s.st == nil {
		return []dtoconversation.RunResp{}, nil
	}
	rows, err := s.st.ListRuns(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]dtoconversation.RunResp, 0, len(rows))
	for _, r := range rows {
		resp := dtoconversation.RunResp{
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
				resp.Usage = &dtoconversation.RunUsageResp{
					Used: u.Used, Size: u.Size, CostAmount: u.CostAmount, CostCurrency: u.CostCurrency,
				}
			}
		}
		out = append(out, resp)
	}
	return out, nil
}
