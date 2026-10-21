package session

import (
	"context"
	"sync"

	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/core/object/objusage"
)

// Plane 会话域的**有状态主体**（P3b-1a：先只持库句柄与一个用量端口；会话记录、输入队列
// 等状态随 P3b-1b/P3b-2 搬进来）。
//
// 并发审查（§3.1.19 的先例，结论见计划 §3.1.40）：会话核心组 13 处 `s.mu` 临界区里
// **0 处跨域**——会话的锁只保护会话自己的字段。所以这一域将自持一把锁；本批搬进来的函数
// 都不取锁（读模型是纯 `st` 操作），所以 `mu` 暂时只占位（下一批的会话表要用）。
type Plane struct {
	mu sync.Mutex
	st Store
	// bus 事件总线（恢复时按 DB 位次把已落库事件注回去；与库共用同一套会话内位次）。
	bus *eventbus.Bus

	// sessions 进程内会话记录（v14 起 sqlite 模式只是**镜像**：库是元数据权威，内存是执行上下文）。
	// 它由**本域自己的锁**保护（并发审查结论：会话的临界区 0 处跨域，见计划 §3.1.40/§3.1.43）。
	sessions map[string]*SessionRec
	// restoring 正从库恢复（恢复期不逐条回写：刚读出来的东西立刻写回去）。
	restoring bool
	// ver 每个会话的事件版本号（进 bus 帧的 version；与库里的位次是两件事）。
	ver map[string]int64
	// runStatusKey 每个会话**上一次发出的** `run.status` 去重键（`run_id|status|phase|…`，
	// 见 `PublishRunStatus`）。它只在进程内、只用于「同状态连发只留第一帧」——丢了不丢事实
	// （事件已在库里），所以不进库、不需要持久化。
	runStatusKey map[string]string
	// runStatusLatest 每个会话**最近一次** `run.status` 的投影（`GET /sessions/:id` 的 `run` 段
	// 与一切过程读面都读它）。写点只有 `recordRunStatus` 一处：实时发布、恢复回放、决策恢复。
	runStatusLatest map[string]RunStatusSnapshot
	// runStatusHold 每个会话**进入等待前**那一次状态（权限 / 提问是插曲，决策后要回到那里）。
	// 由 `recordRunStatus` 在等待/非等待的转换上维护，`ResumeRunStatus` 消费。
	runStatusHold map[string]RunStatusSnapshot
	// inputs 进程内排队输入（memory 模式的事实源；sqlite 模式是 inputs 表的镜像/缓存）。
	inputs map[string][]QueuedInput
	// coalescer 文本增量合并器（nil = 不合并；停机时 FlushAll）。
	coalescer *Coalescer

	// capFacts 能力事实提供者（P3c：能力指纹的装配点在**本域**，但事实要读 runtime 注册表 /
	// skill 投影面 / MCP 注入计划——那三样只有装配侧够得着）。nil = 未登记（只按 runtime id 算）。
	capFacts CapabilityFactsProvider
	// capLookup 能力定义查表（P3c：选定校验要按 id 取定义 + 判可达性事实）。nil = 未登记。
	capLookup CapabilityLookup
	// tx wire transcript 旁路（nil = 未启用）。P3b-2b 起是**域内类型**——临时端口
	// `TranscriptSink` 随装载/检索面一起搬进来后删掉（它此前存在的唯一理由是那半还在 manager）。
	tx *transcriptRecorder

	// workspaceDisplayOf 归属展示字段的解析（端口：工作空间登记仍在 manager）。
	workspaceDisplayOf func(workspaceID string) (string, string)
	// modelValueFor 平台模型行 ↔ 底座运行时值的换算（端口：逐 runtime 形态不同，属 adapter 面）。
	modelValueFor func(runtimeID, providerID, model string) (string, error)
	// staleInputsOnRestore 恢复时作废"重启前排队未投递"的输入（**临时端口**：输入队列 P3b-2 才搬进
	// 本域，届时这条线变成域内调用）。
	staleInputsOnRestore func(ctx context.Context)

	// usageOf 取某会话最近一次上报的用量快照（**端口**：它读的是轮次状态机
	// `Runner.LatestUsage` 的内存表，随 P4a 第五十五刀同族搬入本域；nil = 没有状态机，
	// 同拆分前 "`s.orch == nil` 时 usage_json 留空"的语义）。
	usageOf func(sessionID string) (objusage.UsageInfo, bool)
}

// NewPlane 造一个会话域。库句柄与用量端口都由装配期注入（端口可后置——离线命令没有编排器）。
func NewPlane() *Plane {
	return &Plane{sessions: make(map[string]*SessionRec), ver: make(map[string]int64), inputs: make(map[string][]QueuedInput)}
}

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

// SetStore 装配期登记库句柄。**必须在装配期调用**：构造时按值传一个当时的 `st` 会永远停在
// nil，而表现是"该走库的走了内存"——静默（见 `Service.attachStore` 的注释）。
func (pl *Plane) SetStore(st Store) { pl.st = st }

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

// SetCoalescer 装配期登记文本增量合并器。
func (pl *Plane) SetCoalescer(c *Coalescer) { pl.coalescer = c }

// SetCapabilityFacts 装配期登记能力事实提供者（P3c：指纹装配点已在本域）。
//
// 必须是显式 setter 而不是构造期传参：装配顺序里 `caps`/`reg`/网关开关都要先就位，早一行登记
// 拿到的是"还没装载完的注册表"——表现是**指纹少算一段**，只在"代际该不该切"这种慢变量上现形。
func (pl *Plane) SetCapabilityFacts(p CapabilityFactsProvider) { pl.capFacts = p }

// SetCapabilityLookup 装配期登记能力定义查表（P3c：选定校验用）。
func (pl *Plane) SetCapabilityLookup(l CapabilityLookup) { pl.capLookup = l }

// Version 某会话当前的事件版本号（进 bus 帧的 `version`）。
func (pl *Plane) Version(sessionID string) int64 {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.ver[sessionID]
}

// ResetVersion 把某会话的事件版本号清零（测试夹具用：模拟"重启后版本从 1 开始"）。
func (pl *Plane) ResetVersion(sessionID string) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.ver[sessionID] = 0
}
