package manager

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/morehao/ark-forge/internal/dto/dtoconversation"
	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/code"
)

// ——— §6 busy input 队列（排队 / 插话） ———
//
// 改造前：会话正忙时 POST /conversations/:id/prompt 直接报 turn_in_progress——用户敲下的字
// 在 UI 上就地丢失（要么等，要么先取消，两条路都不是「我先把这句话交代了」）。现在
// delivery 三值：
//
//	reject（缺省）          忙时报错。历史行为，不产生队列行。
//	after_turn             入队；当前轮结束后自动投递。
//	interrupt_then_prompt  入队 + 取消当前轮 → 随后自动投递（服务端原子化：前端「先 cancel
//	                       再 prompt」两步之间会丢窗口——cancel 落地前旧轮可能已自然结束）。
//
// 事实源：sqlite 模式 = inputs 表（投递状态 + 审计台账：谁在何时排了什么、最终投递/撤下/作废）；
// memory 模式 = 进程内队列（进程结束即失效，与「memory 模式以 transcript 为事实源」的既有取向
// 一致——没有库可写时不留半吊子台账）。
//
// 顺序：严格 FIFO（created_at, id）。interrupt_then_prompt **不插队**——先排的先投，否则连按
// 三次「插话」会以逆序执行。投递前校验 Generation（入队时的会话执行代际）：不等 = 该项依赖的
// 那一轮上下文已被重建（连接断开重连 / 会话重开），该项**作废**而不是投进用户没见过的上下文。
//
// 不自动续投的两种收尾：turn 失败（避免把队列变成「连续失败重放」）与进程主动停机
// （intentional；重启时全库 queued 行统一置作废——等待的那一轮已随进程消失）。

const (
	// DeliveryReject 忙时直接报错（缺省；不排队）。
	DeliveryReject = "reject"
	// DeliveryAfterTurn 排队，等当前轮结束再投递。
	DeliveryAfterTurn = store.InputDeliveryAfterTurn
	// DeliveryInterruptThenPrompt 取消当前轮，随后投递。
	DeliveryInterruptThenPrompt = store.InputDeliveryInterruptThenPrompt
)

// ValidDeliveries 合法投递模式（报错提示与校验共用，顺序即文档顺序）。
var ValidDeliveries = []string{DeliveryReject, DeliveryAfterTurn, DeliveryInterruptThenPrompt}

// 队列项终态（事件收敛用；与 store.InputStatus* 同值）。
const (
	ReasonTurnFinished = "turn_finished"  // 队列项因上一轮结束而投递
	ReasonUserCancel   = "user_cancel"    // 用户显式撤下
	ReasonDeliverFail  = "deliver_failed" // 投递时启动新一轮失败（文本保留在台账行里，不静默丢弃）
	ReasonStale        = "stale"          // 进程重启：等待的那一轮已随进程消失，统一作废
)

// NormalizeDelivery 归一并校验投递模式（空 = reject，即历史行为）。
// 非法值显式报错而不是回落 reject：投递语义是**用户的意图**，猜错等于悄悄丢输入。
func NormalizeDelivery(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", DeliveryReject:
		return DeliveryReject, nil
	case DeliveryAfterTurn:
		return DeliveryAfterTurn, nil
	case DeliveryInterruptThenPrompt:
		return DeliveryInterruptThenPrompt, nil
	default:
		return "", acl.NewError(acl.ClassRequest, "invalid_delivery",
			fmt.Sprintf("delivery 需为 %s 之一，得到 %q", strings.Join(ValidDeliveries, "|"), v), nil)
	}
}

// QueuedInput 队列项（事件载荷与 REST 形状）。
type QueuedInput struct {
	ID             string    `json:"input_id"`
	ConversationID string    `json:"session_id"`
	Text           string    `json:"text"`
	Delivery       string    `json:"delivery"`
	TargetTurnID   string    `json:"target_turn_id,omitempty"`
	Generation     int64     `json:"generation"`
	CreatedAt      time.Time `json:"created_at"`
}

// PromptOutcome 投递结果（Queued=true 表示本次输入已入队，EventSeq = input_queued 的水位）。
type PromptOutcome struct {
	EventSeq int64
	Queued   bool
	InputID  string
}

// ——— Service 侧队列读写（唯一入口；DB / 内存两实现） ———

// enqueueInput 入队一条待投递输入。
func (s *Service) enqueueInput(ctx context.Context, convID, text, delivery, targetTurn string, generation int64) (QueuedInput, error) {
	in := QueuedInput{
		ID: uuid.NewString(), ConversationID: convID, Text: text, Delivery: delivery,
		TargetTurnID: targetTurn, Generation: generation, CreatedAt: time.Now().UTC(),
	}
	if s.st != nil {
		row := store.Input{
			ID: in.ID, ConversationID: convID, Text: text, Delivery: delivery,
			TargetTurnID: targetTurn, Generation: generation,
			Status: store.InputStatusQueued, CreatedAt: in.CreatedAt,
		}
		if err := s.st.CreateInput(ctx, row); err != nil {
			return QueuedInput{}, err
		}
		return in, nil
	}
	s.mu.Lock()
	s.inputs[convID] = append(s.inputs[convID], in)
	s.mu.Unlock()
	return in, nil
}

// listQueuedInputs 列出某会话待投递输入（FIFO；含只读用途）。
func (s *Service) listQueuedInputs(ctx context.Context, convID string) []QueuedInput {
	if s.st != nil {
		rows, err := s.st.ListQueuedInputs(ctx, convID)
		if err != nil {
			slog.Default().Warn("读取待投递输入失败", "conversation", convID, "err", err)
			return nil
		}
		out := make([]QueuedInput, 0, len(rows))
		for _, r := range rows {
			out = append(out, queuedFromStore(r))
		}
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]QueuedInput(nil), s.inputs[convID]...)
}

// ListQueuedInputs 列出某会话待投递的排队输入（§6 读模型，创建时间升序）。
//
// 与事件的分工：事件负责「过程」（何时入队/投递/撤下），本读模型负责「当前态」——
// 前端冷挂载时 WS 只给 snapshot 不回放历史，没有这个入口队列就会在刷新后凭空消失。
func (s *Service) ListQueuedInputs(ctx context.Context, id string) ([]dtoconversation.QueuedInputResp, error) {
	if !s.hasConversation(id) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	queue := s.listQueuedInputs(ctx, id)
	out := make([]dtoconversation.QueuedInputResp, 0, len(queue))
	for _, q := range queue {
		out = append(out, dtoconversation.QueuedInputResp{
			ID: q.ID, Text: q.Text, Delivery: q.Delivery,
			TargetTurnID: q.TargetTurnID, Generation: q.Generation,
			CreatedAt: q.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out, nil
}

// claimNextInput 取队首**并原子标记为已投递**；false = 队列空（或全被认领）。
// generation 不符的项就地作废并继续看下一条——作废是留痕，不是把队首卡死。
func (s *Service) claimNextInput(ctx context.Context, convID string, generation int64) (*QueuedInput, bool) {
	if s.st != nil {
		for {
			head, err := s.st.HeadQueuedInput(ctx, convID)
			if err != nil {
				slog.Default().Warn("取队首输入失败", "conversation", convID, "err", err)
				return nil, false
			}
			if head == nil {
				return nil, false
			}
			if head.Generation != generation {
				slog.Default().Warn("排队输入作废：会话执行代际已变（等待的那一轮上下文已重建，请重发）",
					"conversation", convID, "input_id", head.ID, "queued_gen", head.Generation, "now_gen", generation)
				if _, err := s.st.StaleQueuedInput(ctx, head.ID); err != nil {
					slog.Default().Warn("标记排队输入作废失败", "input_id", head.ID, "err", err)
					return nil, false
				}
				continue
			}
			ok, err := s.st.ClaimQueuedInput(ctx, head.ID, time.Now().UTC())
			if err != nil {
				slog.Default().Warn("认领排队输入失败", "input_id", head.ID, "err", err)
				return nil, false
			}
			if !ok {
				continue // 竞态：别人先认领了，看下一条
			}
			q := queuedFromStore(*head)
			return &q, true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.inputs[convID]
	for len(list) > 0 {
		in := list[0]
		list = list[1:]
		if in.Generation != generation {
			slog.Default().Warn("排队输入作废：会话执行代际已变（等待的那一轮上下文已重建，请重发）",
				"conversation", convID, "input_id", in.ID, "queued_gen", in.Generation, "now_gen", generation)
			continue
		}
		s.inputs[convID] = list
		return &in, true
	}
	delete(s.inputs, convID)
	return nil, false
}

// deleteQueuedInput 撤下一条待投递输入（仅 queued 可撤；已投递/已作废是台账行，不删）。
// 归属由 conversation_id + input_id 双条件钉住：跨会话删别人的队列项在 SQL 层就不可能。
func (s *Service) deleteQueuedInput(ctx context.Context, convID, inputID string) (bool, error) {
	if s.st != nil {
		return s.st.DeleteQueuedInput(ctx, convID, inputID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.inputs[convID]
	for i, in := range list {
		if in.ID == inputID {
			s.inputs[convID] = append(list[:i:i], list[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

// staleQueuedInputsOnRestore 启动恢复：把全库待投递输入置作废——等待的那一轮已随进程消失，
// 自动重放等于替用户按下发送（且上下文已断）。保留行仅作审计。
//
// **作废也必须说话**：逐条发 `input_cancelled(reason=stale)`。此前只改台账行不发事件，于是事件流里
// 那个队列项永远没有收敛事实——而前端队列只认事件（刷新/重连会重放历史），重启后刷新页面就能看到
// 一条永远投不出去、也撤不掉的幻影队列项。「撤下与发出是两条不同的事实」这条纪律的另一半是：
// 一条队项的结局**总得**是投递或撤下之一，没有第三种沉默。
//
// 先取快照再批量置作废：作废后行仍在（只是 status 变了），但那时已分不清哪些是本轮刚作废的。
func (s *Service) staleQueuedInputsOnRestore(ctx context.Context) {
	if s.st == nil {
		return
	}
	pending, err := s.st.ListAllQueuedInputs(ctx)
	if err != nil {
		slog.Default().Warn("读取重启前的排队输入失败", "err", err)
		return
	}
	n, err := s.st.StaleAllQueuedInputs(ctx)
	if err != nil {
		slog.Default().Warn("作废重启前的排队输入失败", "err", err)
		return
	}
	if n == 0 {
		return
	}
	slog.Default().Warn("重启前有排队未投递的输入，已全部置作废（未被投递；请在会话里重发）", "count", n)
	for _, r := range pending {
		// agent_id 此刻未必可知（会话尚未重建）；载荷以 input_id 为准，前端按 id 收敛。
		s.publish(BusInputCancelled, r.ConversationID, map[string]any{
			"session_id": r.ConversationID, "input_id": r.ID,
			"delivery": r.Delivery, "reason": ReasonStale,
		})
	}
}

// queuedFromStore 持久行 → 对外形状。
func queuedFromStore(r store.Input) QueuedInput {
	return QueuedInput{
		ID: r.ID, ConversationID: r.ConversationID, Text: r.Text, Delivery: r.Delivery,
		TargetTurnID: r.TargetTurnID, Generation: r.Generation, CreatedAt: r.CreatedAt,
	}
}

// ——— Orchestrator 侧：投递 ———

// deliverQueued 在会话空闲时投递队首排队输入（runTurn 收尾 / 入队后复查调用）。
// 不投递：会话仍忙（下一轮自然会再触发）、句柄已死（重建后代际已变，队列项会作废）、队列空。
func (o *Orchestrator) deliverQueued(as *ActiveConversation) {
	as.mu.Lock()
	dead, busy := as.dead, as.busy
	as.mu.Unlock()
	if dead || busy {
		return
	}
	gen := o.svc.conversationGeneration(as.ID)
	q, ok := o.svc.claimNextInput(context.Background(), as.ID, gen)
	if !ok {
		return
	}
	// 先启动新一轮、再发布 delivered：反过来（先发 delivered 再 Prompt）一旦启动失败，
	// 前端会看到队列项凭空消失而其文本从未作为 user_message 出现过。
	if _, err := o.Prompt(context.Background(), as.ID, q.Text); err != nil {
		o.logger.Warn("排队输入投递失败", "conversation", as.ID, "input_id", q.ID, "err", err)
		o.svc.publish(BusInputCancelled, as.ID, map[string]any{
			"session_id": as.ID, "agent_id": as.AgentID, "input_id": q.ID,
			"delivery": q.Delivery, "reason": ReasonDeliverFail,
		})
		return
	}
	o.svc.publish(BusInputDelivered, as.ID, map[string]any{
		"session_id": as.ID, "agent_id": as.AgentID, "input_id": q.ID,
		"delivery": q.Delivery, "reason": ReasonTurnFinished,
	})
}
