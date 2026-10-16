package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/store"
	goruntime "github.com/morehao/ark-mate/pkg/runtime"
)

// 本文件是 `mechanism/doctor` 的**读模型重建端口**在会话侧的接线面（P1a-1b）。
//
// 为什么要它：读模型的**单会话重建**（`Service.RebuildSessionReadModels`）要 events 读面与
// runs/messages 写面，还要在"用日志里的终态收口停在 running 的轮"时复用 live 路径的物化
// （`materializeRun`，它要 orchestrator 的用量快照）——那些都是会话侧的东西，而体检在机制层，
// 机制层不得反向 import 会话侧。于是**循环留在机制层、实现留在会话侧**，中间由这个端口接起来。
//
// 逃生门（临时造一个 `Service`）**留在这里而不是 doctor 里**：它是"实现侧的装配细节"，
// 应该由拥有实现的那一层负责——doctor 只认端口。等 `persist.go` 随会话域落进
// `domain/session`（P3b），这段接线会跟着搬过去。

// readModelSessionRebuilder 只提升"重建一个会话"这一个方法到接线面上。
//
// 刻意用**内嵌窄接口**而不是把 `*Service` 直接暴露出去：命令层需要的是"能重建读模型"，
// 不该顺手拿到整个会话面（那是另一件事，也更容易被误用）。内嵌（而不是写一个同名转发方法）
// 还有一条硬理由：守卫 G7 用 AST 数"`RebuildSessionReadModels` 的调用点恰好一个"来判
// "重建只有一份实现"——加一个同名转发会让那个计数变成 2，而守卫**分不清**转发与第二份实现，
// 于是只能被迫放宽（判据失去意义）。内嵌提升不产生调用点。
type readModelSessionRebuilder interface {
	RebuildSessionReadModels(ctx context.Context, sessionID string) error
}

// ReadModelRebuilder 读模型重建器的接线面（满足 `doctor.ReadModelRebuilder`）。
type ReadModelRebuilder struct {
	readModelSessionRebuilder
	svc *Service
}

// NewReadModelRebuilder 造一个只装库句柄的重建器（`mate doctor --rebuild-read-models` 用）。
//
// 调用方必须 `defer rb.Close()`：内部的 `Service` 带着流式合并器的 goroutine。
//
// 刻意**不调** `EnableStore`：它会顺带做装配期同步（`SyncRuntime`，写底座配置落点），而重建读
// 模型是纯读模型的写动作。副作用面越小，"doctor 说重建好了"越等于"读模型真被重铺了"。
func NewReadModelRebuilder(st *store.Store) *ReadModelRebuilder {
	svc := NewService(goruntime.AllBuiltinRegistry(), nil)
	svc.attachStore(st)
	return &ReadModelRebuilder{readModelSessionRebuilder: svc, svc: svc}
}

// Close 释放重建器（关掉内部合并器）。
func (r *ReadModelRebuilder) Close() {
	if r == nil || r.svc == nil || r.svc.coalescer == nil {
		return
	}
	r.svc.coalescer.Close()
}

// MessageRowSourceTypes 消息白名单投影的**来源类型集合**（声明面的只读出口，调用方拿到的是副本）。
//
// 为什么需要这个出口：`mate doctor` 的 read-models 体检要拿它判"库里有没有别的事件类型混进
// messages"（在投影里加一个 `case` 很容易，而它会静默改变读模型的含义：检索会突然命中思考
// 文本）。那份集合的声明面必须与投影的 `switch` 同源（守卫 G5 钉住），所以它属于会话侧；
// 体检判据在机制层，机制层不 import 会话侧——于是由组合根把这份事实**交下去**。
func MessageRowSourceTypes() []string {
	return append([]string(nil), messageRowSourceTypes...)
}
