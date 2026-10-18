package svcsession

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/code"
)

// QueueSvc 排队输入的**撤下**（§6）。
//
// 为什么它算真用例而不是"一行转发"：它自己定三条语义——请求形状校验（缺参 → `input_required`）、
// 幂等口径（不存在/已投递/已作废一律 `false` 而不是报错）、以及**真撤下时必须发一条
// `session.input_cancelled`**（离队是可回放的事实）。这三条在别处没有第二份。
//
// 这一面**不需要新端口**：入队的载体（队列状态）与事件的出口都在**会话域平面**里
// （`Plane.DeleteQueuedInput` / `Plane.Publish`），用例层直接调它——与只读那几面同一手法：
// 事实在域里、判据在用例层，中间不再垫一层转发。
type QueueSvc interface {
	// DeleteQueuedInput 撤下一条尚未投递的排队输入（§6）；返回是否存在待投递项被撤下
	// （已投递/已作废是台账行，不可撤；不存在的 id 幂等返回 false）。
	DeleteQueuedInput(ctx context.Context, id, inputID string) (bool, error)

	// MoveQueuedInput 重排（DOM insertBefore 语义，ADR-10 动作①）：`beforeID` 空 = 追加到末尾。
	MoveQueuedInput(ctx context.Context, id, inputID, beforeID string) error

	// SendNow 让这一条**成为下一轮**（动作②）：排到队首；会话空闲时立刻投递。
	SendNow(ctx context.Context, id, inputID string) error

	// PopForEdit 取回草稿并作废（动作③）：返回正文，并发一条
	// `input_cancelled{reason: user_edit}`（离队是可回放的事实）。
	PopForEdit(ctx context.Context, id, inputID string) (string, error)
}

// QueuedDelivery 「让队首立刻成为一轮」的窄端口（会话轮次投递是那一面的事，本用不拥有它）。
//
// 只用于 `SendNow` 的**空闲**分支：忙时什么都不用做——当前轮结束时收尾路径会自然投递队首，
// 而此刻排队项已经被排到了第一位。这一层刻意不自己去启动一轮：那样"谁来投递"就有两份实现。
type QueuedDelivery interface {
	DeliverQueuedNow(ctx context.Context, sessionID string) error
}

// Queue 排队输入用例。依赖事实：会话域平面（队列状态与事件出口）+ 投递窄端口。
type Queue struct {
	plane *session.Plane
	del   QueuedDelivery
}

// NewQueue 造排队输入用例（依赖事实由装配点交下来，用例实例在这里造）。
func NewQueue(plane *session.Plane, del QueuedDelivery) QueueSvc {
	return &Queue{plane: plane, del: del}
}

var _ QueueSvc = (*Queue)(nil)

// DeleteQueuedInput 撤下一条尚未投递的排队输入。
//
// 漏发那条事件的后果是**静默**的：REST 回 `{"deleted":true}`、库里那一行也真的没了，
// 而前端的队列只认事件、不做本地推断——于是界面里那一行永远挂着，直到刷新页面。
func (q *Queue) DeleteQueuedInput(ctx context.Context, id, inputID string) (bool, error) {
	if id == "" || inputID == "" {
		return false, code.FromError(acl.NewError(acl.ClassRequest, "input_required", "session_id 与 input_id 必填", nil))
	}
	ok, err := q.plane.DeleteQueuedInput(ctx, id, inputID)
	if err != nil {
		return false, code.FromError(err)
	}
	if !ok {
		return false, nil
	}
	// `runtime` 只是给前端显示用的归属事实：会话记录可能刚好在这一刻被回收，取不到就留空
	// （**不阻断**这条事件——"谁被撤了"比"属于哪个底座"重要）。
	runtimeID := ""
	if rec := q.plane.Rec(id); rec != nil {
		runtimeID = rec.RuntimeID
	}
	q.plane.Publish(session.BusInputCancelled, id, map[string]any{
		"session_id": id, "runtime": runtimeID, "input_id": inputID, "reason": session.ReasonUserCancel,
	})
	return true, nil
}

// requireParams 三个动作共用的参数校验（缺参 → `input_required`，与撤下那一条同码）。
func requireParams(id, inputID string) error {
	if id == "" || inputID == "" {
		return code.FromError(acl.NewError(acl.ClassRequest, "input_required", "session_id 与 input_id 必填", nil))
	}
	return nil
}

// MoveQueuedInput 重排（ADR-10 动作①）。**不发事件**：顺序不是可回放的"事实"，而是队列的当前态
// ——冷挂载走 `GET /sessions/:id/inputs` 拿到的就是重排后的顺序。多发一条"顺序变了"的事件，
// 前端还得靠它去改本地顺序，而那等于把同一份状态维护两遍。
func (q *Queue) MoveQueuedInput(ctx context.Context, id, inputID, beforeID string) error {
	if err := requireParams(id, inputID); err != nil {
		return err
	}
	return code.FromError(q.plane.MoveQueuedInput(ctx, id, inputID, beforeID))
}

// SendNow 让这一条成为下一轮（ADR-10 动作②）。
//
// 两步：排到队首（域内），然后**让投递路径看见它**——会话空闲时 nobody 会"等当前轮结束"，
// 所以必须立刻投一次；忙时端口那一侧是 no-op（收尾路径自然会投队首）。
func (q *Queue) SendNow(ctx context.Context, id, inputID string) error {
	if err := requireParams(id, inputID); err != nil {
		return err
	}
	if err := q.plane.SendNowQueuedInput(ctx, id, inputID); err != nil {
		return code.FromError(err)
	}
	if q.del == nil {
		return nil
	}
	if err := q.del.DeliverQueuedNow(ctx, id); err != nil {
		return code.FromError(err)
	}
	return nil
}

// PopForEdit 取回草稿并作废（ADR-10 动作③）。事件由域里发（离队事实的单一铸造点）。
func (q *Queue) PopForEdit(ctx context.Context, id, inputID string) (string, error) {
	if err := requireParams(id, inputID); err != nil {
		return "", err
	}
	text, err := q.plane.PopQueuedInputForEdit(ctx, id, inputID)
	if err != nil {
		return "", code.FromError(err)
	}
	return text, nil
}
