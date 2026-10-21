package session

import (
	"context"
	"fmt"

	"github.com/morehao/golib/glog"
)

// RebuildAllReadModels 用 events 重建**全部会话**的读模型（J7 的字面操作）。
//
// 消费面**只有两处**：启动钩子 `rebuildStaleReadModels`（标记驱动）与
// `mate doctor --rebuild-read-models`（人工显式要求）。两者共用这一份循环——
// 两份实现今天看着一样，明天改一处就分叉，而分叉的后果是"启动重建出来的"与
// "doctor 重建出来的"读模型不同（J7 要求重建结果与触发者无关）。
//
// 循环归这里（而不是机制层）：它是"遍历全部会话"这件事，会话列表本来就只有本域知道怎么列。
// v3.2 刀 9 从 `mechanism/doctor` 搬下来——那里的 `SessionLister` 只是为了拿到同一份列表，
// 而领域自己就握着它。
func (pl *Plane) RebuildAllReadModels(ctx context.Context) (total, failed int, err error) {
	if pl.st == nil {
		return 0, 0, fmt.Errorf("没有可读写的库句柄")
	}
	list, err := pl.st.ListSessions(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	for _, sess := range list {
		if rerr := pl.RebuildSessionReadModels(ctx, sess.ID); rerr != nil {
			failed++
			glog.Warnw(ctx, "read models: 会话重建失败", "session", sess.ID, "err", rerr)
		}
	}
	return len(list), failed, nil
}
