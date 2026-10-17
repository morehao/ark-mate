package svcsession

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/code"
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
}

// Queue 排队输入用例。依赖事实只有会话域平面一个。
type Queue struct {
	plane *session.Plane
}

// NewQueue 造排队输入用例（依赖事实由装配点交下来，用例实例在这里造）。
func NewQueue(plane *session.Plane) QueueSvc { return &Queue{plane: plane} }

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
