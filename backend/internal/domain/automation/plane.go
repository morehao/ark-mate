// Package automation 是**自动化**这个域：定时/手动触发的「按排期建会话」。
//
// 它**负责**：自动化定义的装载与读写（两种载体：DB 行 / 定义文件）、排期计算与到期判定、
// 触发一次的认领与写回、运行史的记录与实时事件、以及调度循环本身的启停。
//
// 它**不负责**：会话的创建与取消（那是 `domain/session`——本域经 `Host` 端口请求）、
// 事件的身份与落库（本域只发一条**只走实时通道**的 `automation.run`，身份与落库都属会话域）、
// 以及对外 DTO 与错误码出口（用例层 `app/*` 与 `contract/dto*`）。
//
// **它自持一把锁**：`recs`（内存注册表）、`dir`、`autoVer`、调度 goroutine 的句柄此前挂在
// `manager.Service` 上、与 `sessions` 共用 `Service.mu`；而"自动化注册表"与"会话表"本来没有
// 必须原子这一说（域内没有一处临界区同时读两者——P2b 前置已把"持 manager 锁调会话域"清零）。
package automation

import (
	"context"
	"sync"
	"time"
)

// Plane 自动化域的**有状态主体**（自持一把只保护域内字段的锁）。
type Plane struct {
	mu sync.Mutex
	// dir 定义目录（'' = 不落盘，仅内存）；见 LoadAutomationDefinitions。
	dir string
	// recs 内存注册表（id → 记录）。坏定义**留在里面**带 `LoadErr`——用户写了文件却在面板上
	// 看不见、还不知道为什么，是最难排查的一类投诉。
	recs map[string]*Rec
	// autoVer 自动化事件的版本计数（`automation:<id>` 聚合）。
	// 会话侧那张版本表在 `domain/session`——两个名字避免下一个人以为它们共享一张表。
	autoVer map[string]int64
	// stop 调度 goroutine 的停止函数（非 nil = 已启动；StartAutomationScheduler 幂等判据）。
	stop context.CancelFunc
	// done 调度 goroutine 的退出信号（Stop 等它关闭再返回：停机后不该再有新触发起步）。
	done chan struct{}
	// outboxOnce 投递 worker 的幂等启动闸（StartAutomationOutboxWorker 可能被调用多次）。
	outboxOnce sync.Once
	// runMu 把「认领判定 → 建会话 → 写回游标」串成临界区。
	//
	// 认领判定（`automation.Decide`）是纯函数，纯函数**不提供互斥**：tick 与
	// `POST /automations/:id/run` 若同时看到"这个 occurrence 还没认领过"，两边都会建会话——
	// 而 automation 的 prompt 常常有副作用（改代码、发通知），重复跑一次是不可接受的。
	// 临界区里唯一的慢动作是建会话（子进程握手），而 automation 触发本身稀疏，串行化代价可忽略。
	runMu sync.Mutex

	// st 库句柄（DB 载体；nil = memory 模式，走文件载体或纯内存）。装配期一次性注入。
	st    AutomationStore
	files DefinitionFiles
	// host 宿主端口（建会话 / 取消 / 事件出口）。装配期一次性注入。
	host Host
}

// NewPlane 造一个自动化域（构造期不启调度；`StartAutomationScheduler` 才启）。
func NewPlane() *Plane {
	return &Plane{recs: map[string]*Rec{}, autoVer: map[string]int64{}}
}

// SetStore 装配期登记库句柄（DB 载体用）。
//
// **必须在装配期调用**：构造时按值传一个当时的 `st` 会永远停在 nil，而表现是"该走库的走了
// 内存"——静默（与 `session.Plane.SetStore` 同一条教训）。
func (pl *Plane) SetStore(st AutomationStore) { pl.st = st }

// SetHost 装配期登记宿主端口（建会话/取消/事件出口；nil = 不能触发）。
func (pl *Plane) SetHost(h Host) { pl.host = h }

// hostOf 取宿主端口（未登记 → nil；调用方自己判，不在这里兜一个空实现：
// 空实现会把"忘了接线"变成"静默不触发"）。
func (pl *Plane) hostOf() Host { return pl.host }

// nowFn 取当前时间（测试可替换；生产就是 time.Now）。
var nowFn = time.Now

// Mutate 在**本域的锁**下就地改一条记录（返回 false = 没有这条）。
//
// 与 `session.Plane.Mutate` 同一个形状，存在的理由也一样：读面（`RecByID`）给的是**副本**，
// 而"拨一下内存调度态"（重算 nextDue、改启动动作）这类动作要就地改。回调里只许动字段、
// **不许做 I/O**（持锁读库会与本域"先写内存、再落库"的顺序互相卡住）。
func (pl *Plane) Mutate(id string, fn func(r *Rec)) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	rec, ok := pl.recs[id]
	if !ok {
		return false
	}
	fn(rec)
	return true
}

// SchedulerStopped 调度 goroutine 是否**未启动**（`start == nil` 的判据）。
//
// 存在的理由：启停的判据（"interval<=0 不该起 goroutine"）此前靠读 `Service.automationStop`
// 这个字段来断言；字段进域之后，那个断言需要一个属于域的读面——而不是让调用方去够内部指针。
func (pl *Plane) SchedulerStopped() bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.stop == nil
}
