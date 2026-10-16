package session

import (
	"context"
	"sync"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/eventbus"
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
	// bus 事件总线（恢复时按 DB 位次把已落库事件注回去；与库共用同一套会话内位次）。
	bus *eventbus.Bus

	// sessions 进程内会话记录（v14 起 sqlite 模式只是**镜像**：库是元数据权威，内存是执行上下文）。
	// 它由**本域自己的锁**保护（并发审查结论：会话的临界区 0 处跨域，见计划 §3.1.40/§3.1.43）。
	sessions map[string]*SessionRec
	// restoring 正从库恢复（恢复期不逐条回写：刚读出来的东西立刻写回去）。
	restoring bool

	// workspaceDisplayOf 归属展示字段的解析（端口：工作空间登记仍在 manager）。
	workspaceDisplayOf func(workspaceID string) (string, string)
	// capabilityViewOf 会话记录 → 能力选定回显（端口：`CapabilitySelection` 是能力代际面，
	// P3c 才搬进本域；会话读面不该为了一个回显字段把整个代际面拖进来）。
	capabilityViewOf func(rec *SessionRec) *dtosession.SessionCapabilitiesResp

	// modelValueFor 平台模型行 ↔ 底座运行时值的换算（端口：逐 runtime 形态不同，属 adapter 面）。
	modelValueFor func(runtimeID, providerID, model string) (string, error)
	// staleInputsOnRestore 恢复时作废"重启前排队未投递"的输入（**临时端口**：输入队列 P3b-2 才搬进
	// 本域，届时这条线变成域内调用）。
	staleInputsOnRestore func(ctx context.Context)

	// usageOf 取某会话最近一次上报的用量快照（**端口**：它读的是执行面
	// `Orchestrator.LatestUsage` 的内存表，属 runtimeconn；nil = 没有编排器，同拆分前
	// "`s.orch == nil` 时 usage_json 留空"的语义）。
	usageOf func(sessionID string) (objusage.UsageInfo, bool)
}

// NewPlane 造一个会话域。库句柄与用量端口都由装配期注入（端口可后置——离线命令没有编排器）。
func NewPlane() *Plane { return &Plane{sessions: make(map[string]*SessionRec)} }

// Snapshot 会话记录的**副本**切片（读面用；调用方拿不到内部指针，改不动本域状态）。
func (pl *Plane) Snapshot() []SessionRec {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	out := make([]SessionRec, 0, len(pl.sessions))
	for _, r := range pl.sessions {
		out = append(out, *r)
	}
	return out
}

// SetWorkspaceDisplay 装配期登记归属展示解析（工作空间登记面）。
func (pl *Plane) SetWorkspaceDisplay(fn func(workspaceID string) (string, string)) {
	pl.workspaceDisplayOf = fn
}

// SetCapabilityView 装配期登记能力选定回显投影（能力代际面）。
func (pl *Plane) SetCapabilityView(fn func(rec *SessionRec) *dtosession.SessionCapabilitiesResp) {
	pl.capabilityViewOf = fn
}

// SetStore 装配期登记库句柄。**必须在装配期调用**：构造时按值传一个当时的 `st` 会永远停在
// nil，而表现是"该走库的走了内存"——静默（见 `Service.attachStore` 的注释）。
func (pl *Plane) SetStore(st *store.Store) { pl.st = st }

// SetUsageSource 装配期登记用量快照来源（执行面）。
func (pl *Plane) SetUsageSource(fn func(sessionID string) (objusage.UsageInfo, bool)) {
	pl.usageOf = fn
}

// SetRestoring 标记"正在从库恢复"（恢复期逐条落库没有意义：刚读出来的东西立刻写回去）。
//
// 必须是显式 setter：这个标志以前是 `Service` 的字段、由 `RestoreFromStore` 直接赋值；搬进本域
// 之后若忘了这条线，**表现是静默的**——恢复期间多发一轮写库，读模型与最终状态都对，只是慢。
func (pl *Plane) SetRestoring(v bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.restoring = v
}

// SetBus 装配期登记事件总线。
func (pl *Plane) SetBus(bus *eventbus.Bus) { pl.bus = bus }

// SetModelValueSource 装配期登记"平台模型行 ↔ 底座运行时值"的换算。
func (pl *Plane) SetModelValueSource(fn func(runtimeID, providerID, model string) (string, error)) {
	pl.modelValueFor = fn
}

// SetStaleInputsOnRestore 装配期登记恢复期的排队输入作废钩子（见字段注释：P3b-2 后并入本域）。
func (pl *Plane) SetStaleInputsOnRestore(fn func(ctx context.Context)) {
	pl.staleInputsOnRestore = fn
}
