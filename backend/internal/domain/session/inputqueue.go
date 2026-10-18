package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/code"
	"github.com/morehao/ark-mate/internal/object/objid"
	"github.com/morehao/golib/glog"
)

const (
	// DeliveryReject 忙时直接报错（缺省；不排队）。
	DeliveryReject = "reject"
	// DeliveryAfterRun 排队，等当前轮结束再投递。
	DeliveryAfterRun = model.InputDeliveryAfterRun
	// DeliveryInterruptThenPrompt 取消当前轮，随后投递。
	DeliveryInterruptThenPrompt = model.InputDeliveryInterruptThenPrompt
)

// ValidDeliveries 合法投递模式（报错提示与校验共用，顺序即文档顺序）。
var ValidDeliveries = []string{DeliveryReject, DeliveryAfterRun, DeliveryInterruptThenPrompt}

// 队列项终态（事件收敛用；与 model.InputStatus* 同值）。
const (
	ReasonRunFinished = "run_finished"   // 队列项因上一轮结束而投递
	ReasonUserCancel  = "user_cancel"    // 用户显式撤下
	ReasonDeliverFail = "deliver_failed" // 投递时启动新一轮失败（文本保留在台账行里，不静默丢弃）
	ReasonStale       = "stale"          // 进程重启：等待的那一轮已随进程消失，统一作废
	// ReasonUserEdit 用户把队项**取回编辑**（ADR-10 的第三个动作）：它没被投递，也不算"撤下"——
	// 用户是拿去改内容，改完会作为**新一轮**再发一次。与 user_cancel 分开是刻意的：
	// 两者的界面含义不同（"我不要了" vs "我要改一下"），合成一个值就只能靠猜。
	ReasonUserEdit = "user_edit"
)

// QueuedInputReasons 队列项离队原因的**取值集**（唯一事实源）。
//
// 三处必须逐条相等：这里、前端 `QueuedInputReason`（`types/src/session.ts`）、以及 archtest 的
// `TestQueuedInputReasonVocabularyIsClosed`。理由是前端**只认事件**、不做本地推断——后端多发一个
// 前端不认的值，界面就只能把它渲染成"未知原因"或者干脆不收敛。
var QueuedInputReasons = []string{ReasonRunFinished, ReasonUserCancel, ReasonDeliverFail, ReasonStale, ReasonUserEdit}

// NormalizeDelivery 归一并校验投递模式（空 = reject，即历史行为）。
// 非法值显式报错而不是回落 reject：投递语义是**用户的意图**，猜错等于悄悄丢输入。
func NormalizeDelivery(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", DeliveryReject:
		return DeliveryReject, nil
	case DeliveryAfterRun:
		return DeliveryAfterRun, nil
	case DeliveryInterruptThenPrompt:
		return DeliveryInterruptThenPrompt, nil
	default:
		return "", acl.NewError(acl.ClassRequest, "invalid_delivery",
			fmt.Sprintf("delivery 需为 %s 之一，得到 %q", strings.Join(ValidDeliveries, "|"), v), nil)
	}
}

// QueuedInput 队列项（事件载荷与 REST 形状）。
type QueuedInput struct {
	ID          string    `json:"input_id"`
	SessionID   string    `json:"session_id"`
	Text        string    `json:"text"`
	Delivery    string    `json:"delivery"`
	TargetRunID string    `json:"target_run_id,omitempty"`
	Generation  int64     `json:"generation"`
	CreatedAt   time.Time `json:"created_at"`
}

// PromptOutcome 投递结果（Queued=true 表示本次输入已入队，EventSeq = input_queued 的水位）。
type PromptOutcome struct {
	EventSeq int64
	Queued   bool
	InputID  string
}

// EnqueueInput 入队一条待投递输入。
func (pl *Plane) EnqueueInput(ctx context.Context, convID, text, delivery, targetRun string, generation int64) (QueuedInput, error) {
	in := QueuedInput{
		ID: objid.New(objid.Input), SessionID: convID, Text: text, Delivery: delivery,
		TargetRunID: targetRun, Generation: generation, CreatedAt: time.Now().UTC(),
	}
	if pl.st != nil {
		row := model.InputEntity{
			ID: in.ID, SessionID: convID, Text: text, Delivery: delivery,
			TargetRunID: targetRun, Generation: generation,
			Status: model.InputStatusQueued, CreatedAt: in.CreatedAt,
		}
		if err := pl.st.CreateInput(ctx, row); err != nil {
			return QueuedInput{}, err
		}
		return in, nil
	}
	pl.mu.Lock()
	pl.inputs[convID] = append(pl.inputs[convID], in)
	pl.mu.Unlock()
	return in, nil
}

// listQueuedInputs 列出某会话待投递输入（FIFO；含只读用途）。
func (pl *Plane) ListQueuedInputsOf(ctx context.Context, convID string) []QueuedInput {
	if pl.st != nil {
		rows, err := pl.st.ListQueuedInputs(ctx, convID)
		if err != nil {
			glog.Warnw(ctx, "读取待投递输入失败", "session", convID, "err", err)
			return nil
		}
		out := make([]QueuedInput, 0, len(rows))
		for _, r := range rows {
			out = append(out, queuedFromStore(r))
		}
		return out
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return append([]QueuedInput(nil), pl.inputs[convID]...)
}

// ListQueuedInputs 列出某会话待投递的排队输入（§6 读模型，创建时间升序）。
//
// 与事件的分工：事件负责「过程」（何时入队/投递/撤下），本读模型负责「当前态」——
// 前端冷挂载时 WS 只给 snapshot 不回放历史，没有这个入口队列就会在刷新后凭空消失。
func (pl *Plane) ListQueuedInputs(ctx context.Context, id string) ([]dtosession.QueuedInputResp, error) {
	if !pl.HasSession(id) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	queue := pl.ListQueuedInputsOf(ctx, id)
	out := make([]dtosession.QueuedInputResp, 0, len(queue))
	for _, q := range queue {
		out = append(out, dtosession.QueuedInputResp{
			ID: q.ID, Text: q.Text, Delivery: q.Delivery,
			TargetRunID: q.TargetRunID, Generation: q.Generation,
			CreatedAt: q.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out, nil
}

// ClaimNextInput 取队首**并原子标记为已投递**；false = 队列空（或全被认领）。
// generation 不符的项就地作废并继续看下一条——作废是留痕，不是把队首卡死。
func (pl *Plane) ClaimNextInput(ctx context.Context, convID string, generation int64) (*QueuedInput, bool) {
	if pl.st != nil {
		for {
			head, err := pl.st.HeadQueuedInput(ctx, convID)
			if err != nil {
				glog.Warnw(ctx, "取队首输入失败", "session", convID, "err", err)
				return nil, false
			}
			if head == nil {
				return nil, false
			}
			if head.Generation != generation {
				glog.Warnw(ctx, "排队输入作废：会话执行代际已变（等待的那一轮上下文已重建，请重发）",
					"session", convID, "input_id", head.ID, "queued_gen", head.Generation, "now_gen", generation)
				if _, err := pl.st.StaleQueuedInput(ctx, head.ID); err != nil {
					glog.Warnw(ctx, "标记排队输入作废失败", "input_id", head.ID, "err", err)
					return nil, false
				}
				continue
			}
			ok, err := pl.st.ClaimQueuedInput(ctx, head.ID, time.Now().UTC())
			if err != nil {
				glog.Warnw(ctx, "认领排队输入失败", "input_id", head.ID, "err", err)
				return nil, false
			}
			if !ok {
				continue // 竞态：别人先认领了，看下一条
			}
			q := queuedFromStore(*head)
			return &q, true
		}
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	list := pl.inputs[convID]
	for len(list) > 0 {
		in := list[0]
		list = list[1:]
		if in.Generation != generation {
			glog.Warnw(ctx, "排队输入作废：会话执行代际已变（等待的那一轮上下文已重建，请重发）",
				"session", convID, "input_id", in.ID, "queued_gen", in.Generation, "now_gen", generation)
			continue
		}
		pl.inputs[convID] = list
		return &in, true
	}
	delete(pl.inputs, convID)
	return nil, false
}

// DeleteQueuedInput 撤下一条待投递输入（仅 queued 可撤；已投递/已作废是台账行，不删）。
// 归属由 session_id + input_id 双条件钉住：跨会话删别人的队列项在 SQL 层就不可能。
func (pl *Plane) DeleteQueuedInput(ctx context.Context, convID, inputID string) (bool, error) {
	if pl.st != nil {
		return pl.st.DeleteQueuedInput(ctx, convID, inputID)
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	list := pl.inputs[convID]
	for i, in := range list {
		if in.ID == inputID {
			pl.inputs[convID] = append(list[:i:i], list[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

// MoveQueuedInput 重排一条待投递输入（DOM insertBefore 语义）：把 `inputID` 插到 `beforeID` 之前，
// `beforeID` 为空 = 追加到末尾（ADR-10 的第一个动作）。
//
// 「我看到的顺序」就是投递顺序——这是它唯一的承诺。实现复用 v22 的 `inputs.seq`
// （会话内位次 + `UNIQUE(session_id, seq)`），整段重编号在同一事务里做（见 dao 的两阶段）。
//
// 不存在 / 已投递 / 已作废 → `queued_input_not_found`(40401)：队列项不是"资源集合里的元素"
// 而是"一句话的投递状态"，改一个不存在的项没有幂等含义（那会让界面以为"我移动成功了"）。
func (pl *Plane) MoveQueuedInput(ctx context.Context, convID, inputID, beforeID string) error {
	list := pl.ListQueuedInputsOf(ctx, convID)
	order := make([]string, 0, len(list))
	found := false
	for _, q := range list {
		if q.ID == inputID {
			found = true
			continue
		}
		order = append(order, q.ID)
	}
	if !found {
		return code.FromError(queuedInputNotFoundErr(inputID))
	}
	if beforeID == "" {
		order = append(order, inputID)
	} else {
		at := -1
		for i, id := range order {
			if id == beforeID {
				at = i
				break
			}
		}
		if at < 0 {
			return code.FromError(acl.NewError(acl.ClassRequest, "queued_input_anchor_not_found",
				fmt.Sprintf("锚点队项 %q 不在该会话的待投递列表里", beforeID), nil))
		}
		order = append(order[:at:at], append([]string{inputID}, order[at:]...)...)
	}
	return pl.reorderQueued(ctx, convID, order)
}

// SendNowQueuedInput 让一条待投递输入**成为下一轮**（ADR-10 的第二个动作）：把它排到队首。
//
// 为什么"排到队首"就是"立即发送"：本仓一轮一次、队列在**当前轮结束时**投递队首。所以"下一条
// 投递的是它"这件事的全部含义就是它排在第一位——多写一条"现在就发"的路径只会让同一件事有
// 两种实现（而那两种在并发下必然分叉）。会话空闲时投递由调用方立刻触发（见 `QueueSvc.SendNow`）。
func (pl *Plane) SendNowQueuedInput(ctx context.Context, convID, inputID string) error {
	list := pl.ListQueuedInputsOf(ctx, convID)
	if len(list) == 0 {
		return code.FromError(queuedInputNotFoundErr(inputID))
	}
	order := make([]string, 0, len(list))
	found := false
	for _, q := range list {
		if q.ID == inputID {
			found = true
			continue
		}
		order = append(order, q.ID)
	}
	if !found {
		return code.FromError(queuedInputNotFoundErr(inputID))
	}
	order = append([]string{inputID}, order...)
	return pl.reorderQueued(ctx, convID, order)
}

// PopQueuedInputForEdit 把一条待投递输入**取回草稿并作废**（ADR-10 的第三个动作），返回正文。
//
// 它同时是"离队事实"的铸造点：发一条 `input_cancelled{reason: user_edit}`。不发那一行的后果与
// 撤下时漏发一模一样——REST 说成功了、库里那行也没了，而前端的队列只认事件，那一行会一直挂着。
func (pl *Plane) PopQueuedInputForEdit(ctx context.Context, convID, inputID string) (string, error) {
	text := ""
	for _, q := range pl.ListQueuedInputsOf(ctx, convID) {
		if q.ID == inputID {
			text = q.Text
			break
		}
	}
	ok, err := pl.DeleteQueuedInput(ctx, convID, inputID)
	if err != nil {
		return "", code.FromError(err)
	}
	if !ok {
		return "", code.FromError(queuedInputNotFoundErr(inputID))
	}
	pl.Publish(BusInputCancelled, convID, map[string]any{
		"session_id": convID, "input_id": inputID, "reason": ReasonUserEdit,
	})
	return text, nil
}

// reorderQueued 落盘重排（store / 内存两实现共用一处入口）。
func (pl *Plane) reorderQueued(ctx context.Context, convID string, order []string) error {
	if pl.st != nil {
		if err := pl.st.ReorderQueuedInputs(ctx, convID, order); err != nil {
			return err
		}
		return nil
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	byID := make(map[string]QueuedInput, len(pl.inputs[convID]))
	for _, q := range pl.inputs[convID] {
		byID[q.ID] = q
	}
	out := make([]QueuedInput, 0, len(order))
	for _, id := range order {
		if q, ok := byID[id]; ok {
			out = append(out, q)
		}
	}
	pl.inputs[convID] = out
	return nil
}

// queuedInputNotFoundErr 队项不在该会话的待投递集里（唯一出口：40401）。
func queuedInputNotFoundErr(inputID string) error {
	return acl.NewError(acl.ClassRequest, "queued_input_not_found",
		fmt.Sprintf("待投递输入 %q 不存在或已不在队列里", inputID), nil)
}

// StaleQueuedInputsOnRestore 启动恢复：把全库待投递输入置作废——等待的那一轮已随进程消失，
// 自动重放等于替用户按下发送（且上下文已断）。保留行仅作审计。
//
// **作废也必须说话**：逐条发 `input_cancelled(reason=stale)`。此前只改台账行不发事件，于是事件流里
// 那个队列项永远没有收敛事实——而前端队列只认事件（刷新/重连会重放历史），重启后刷新页面就能看到
// 一条永远投不出去、也撤不掉的幻影队列项。「撤下与发出是两条不同的事实」这条纪律的另一半是：
// 一条队项的结局**总得**是投递或撤下之一，没有第三种沉默。
//
// 先取快照再批量置作废：作废后行仍在（只是 status 变了），但那时已分不清哪些是本轮刚作废的。
func (pl *Plane) StaleQueuedInputsOnRestore(ctx context.Context) {
	if pl.st == nil {
		return
	}
	pending, err := pl.st.ListAllQueuedInputs(ctx)
	if err != nil {
		glog.Warnw(ctx, "读取重启前的排队输入失败", "err", err)
		return
	}
	n, err := pl.st.StaleAllQueuedInputs(ctx)
	if err != nil {
		glog.Warnw(ctx, "作废重启前的排队输入失败", "err", err)
		return
	}
	if n == 0 {
		return
	}
	glog.Warnw(ctx, "重启前有排队未投递的输入，已全部置作废（未被投递；请在会话里重发）", "count", n)
	for _, r := range pending {
		// 会话身份此刻未必可知（会话尚未重建）；载荷以 input_id 为准，前端按 id 收敛。
		pl.Publish(BusInputCancelled, r.SessionID, map[string]any{
			"session_id": r.SessionID, "input_id": r.ID,
			"delivery": r.Delivery, "reason": ReasonStale,
		})
	}
}

// queuedFromStore 持久行 → 对外形状。
func queuedFromStore(r model.InputEntity) QueuedInput {
	return QueuedInput{
		ID: r.ID, SessionID: r.SessionID, Text: r.Text, Delivery: r.Delivery,
		TargetRunID: r.TargetRunID, Generation: r.Generation, CreatedAt: r.CreatedAt,
	}
}
