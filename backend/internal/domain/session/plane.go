package session

import (
	"sync"

	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/object/objusage"
)

// Plane 会话域的**有状态主体**（P3b-1a：先只持库句柄与一个用量端口；会话记录、输入队列
// 等状态随 P3b-1b/P3b-2 搬进来）。
//
// 并发审查（§3.1.19 的先例，结论见计划 §3.1.40）：会话核心组 13 处 `s.mu` 临界区里
// **0 处跨域**——会话的锁只保护会话自己的字段。所以这一域将自持一把锁；本批搬进来的函数
// 都不取锁（读模型是纯 `st` 操作），所以 `mu` 暂时只占位（下一批的会话表要用）。
type Plane struct {
	mu sync.Mutex
	st *store.Store

	// usageOf 取某会话最近一次上报的用量快照（**端口**：它读的是执行面
	// `Orchestrator.LatestUsage` 的内存表，属 runtimeconn；nil = 没有编排器，同拆分前
	// "`s.orch == nil` 时 usage_json 留空"的语义）。
	usageOf func(sessionID string) (objusage.UsageInfo, bool)
}

// NewPlane 造一个会话域。库句柄与用量端口都由装配期注入（端口可后置——离线命令没有编排器）。
func NewPlane() *Plane { return &Plane{} }

// SetStore 装配期登记库句柄。**必须在装配期调用**：构造时按值传一个当时的 `st` 会永远停在
// nil，而表现是"该走库的走了内存"——静默（见 `Service.attachStore` 的注释）。
func (pl *Plane) SetStore(st *store.Store) { pl.st = st }

// SetUsageSource 装配期登记用量快照来源（执行面）。
func (pl *Plane) SetUsageSource(fn func(sessionID string) (objusage.UsageInfo, bool)) {
	pl.usageOf = fn
}
