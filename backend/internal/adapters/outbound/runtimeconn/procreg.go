package runtimeconn

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/adapters/outbound/procreg"
	"github.com/morehao/golib/glog"
)

// ——— 执行面的**子进程注册表**（P4a 正文第四十八刀）———
//
// 这份状态原先挂在 `manager.Orchestrator.procReg` 上（`orphan_reap.go` 145 行里的一半）。
// 它属执行面：登记的是**本进程拉起的底座子进程**（pid / pgid / command / runtime），孤儿回收
// 也是执行面的活——daemon 被 SIGKILL 时子进程因独立进程组而存活，只有下一次启动的那一份
// 注册表能认出它们。
//
// **为什么值得单独一刀**：注册表是「哪些子进程是我的」的**唯一事实源**。两份（一份在
// `Orchestrator`、一份在执行面）的后果不是报错，而是静默——登记写进一份、回收读另一份，
// 表现为「孤儿回收报告 `reaped=0` 而进程确实还活着」，而所有既有测试照旧绿。
//
// 本文件只装**纯注册表动作**（登记 / 摘除 / 回收 / 读取）：它们只吃 `procreg.Entry` 与 pid，
// **不认识连接对象与会话表**。空闲回收那一半（`reapIdleOnce` / `StartIdleReaper`）要用连接池与
// 会话表判定"这条暖进程还有没有人引用"，仍留在编排侧——它经这里登记/摘除，且**不自己碰注册表**。

// SetProcRegistry 注入进程注册表（装配期；nil = 不登记、不回收）。
func (pl *Plane) SetProcRegistry(r *procreg.Registry) {
	pl.mu.Lock()
	pl.procReg = r
	pl.mu.Unlock()
}

// ProcRegistry 当前注册表（诊断/测试用；nil = 未启用）。
func (pl *Plane) ProcRegistry() *procreg.Registry {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.procReg
}

// ReapOrphans 回收上一 daemon 实例遗留的孤儿 runtime 进程（启动时调用一次）。
//
// **锁内只取指针、锁外才回收**：回收要真的杀进程（慢），持平面锁做它会让同一时刻的连接建立/状态
// 读面一起等。
func (pl *Plane) ReapOrphans(grace time.Duration) (procreg.Report, error) {
	pl.mu.Lock()
	reg := pl.procReg
	pl.mu.Unlock()
	if reg == nil {
		return procreg.Report{Disabled: true}, nil
	}
	return reg.Reap(grace)
}

// RegisterProc 登记刚拉起的子进程（失败只告警，不影响会话可用性）。
func (pl *Plane) RegisterProc(e procreg.Entry) {
	pl.mu.Lock()
	reg := pl.procReg
	pl.mu.Unlock()
	if reg == nil {
		return
	}
	if err := reg.Add(e); err != nil {
		glog.Warnw(context.Background(), "procreg: 登记子进程失败", "pid", e.PID, "err", err)
		return
	}
	glog.Infow(context.Background(), "procreg: 登记 runtime 子进程",
		"runtime", e.Runtime, "pid", e.PID, "pgid", e.PGID)
}

// UnregisterProc 摘除登记（正常回收路径；pid <= 0 或未启用时是 no-op）。
func (pl *Plane) UnregisterProc(pid int) {
	if pid <= 0 {
		return
	}
	pl.mu.Lock()
	reg := pl.procReg
	pl.mu.Unlock()
	if reg == nil {
		return
	}
	if err := reg.Remove(pid); err != nil {
		glog.Warnw(context.Background(), "procreg: 摘除子进程登记失败", "pid", pid, "err", err)
	}
}
