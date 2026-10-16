package manager

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/pkg/procreg"
	"github.com/morehao/golib/glog"
)

// registerProc 把刚拉起的子进程交给**执行面**登记（P4a 正文第四十八刀：注册表归执行面）。
//
// 本函数只做"连接对象 → 注册项"的映射与 pid 落位；登记/告警/回收都在 `runtimeconn.Plane` 里
// （那是"哪些子进程是我的"的唯一事实源）。
func (o *Orchestrator) registerProc(rc *runtimeConn) {
	if rc == nil || rc.client == nil {
		return
	}
	// pid 先落到连接上：诊断面（A3 /diagnostics）不依赖进程注册表是否启用。
	info := rc.client.PidInfo()
	if info.PID > 0 {
		rc.pid = info.PID
	}
	if info.PID <= 0 {
		return
	}
	o.svc.runtimePlane.RegisterProc(procreg.Entry{
		PID: info.PID, PGID: info.PGID, Command: info.Command,
		Runtime: rc.spec.ID,
	})
}

// unregisterProc 摘除登记（正常回收路径）。
func (o *Orchestrator) unregisterProc(rc *runtimeConn) {
	if rc == nil {
		return
	}
	o.svc.runtimePlane.UnregisterProc(rc.pid)
}

// reapIdleOnce 空闲回收单轮：扫全部连接，回收「无在途 run、无任何会话引用、且自建立起空闲
// 超过 idle」的暖进程（P2 StartAgent 常驻类）；锁内判定 + dropConn 原子摘除 → 锁外停进程，
// 下次会话/StartAgent 经 acquire 按需拉起（与 P1 断点恢复合流：连接丢失后 prompt 自动重建）。
// 返回回收数量。
func (o *Orchestrator) reapIdleOnce(now time.Time, idle time.Duration) int {
	o.mu.Lock()
	var victims []*runtimeConn
	for _, rc := range o.conns {
		if rc.started.IsZero() || now.Sub(rc.started) < idle {
			continue
		}
		if o.inflight.Len(rc.client) > 0 {
			continue // 有在途 run，不回收
		}
		hasSession := false
		for _, as := range o.sessions {
			if as.client == rc.client {
				hasSession = true
				break
			}
		}
		if hasSession {
			continue // 仍有会话引用（含已完成态），不回收（回收语义只覆盖暖进程）
		}
		victims = append(victims, rc)
	}
	o.mu.Unlock()
	for _, rc := range victims {
		rc.intentional.Store(true) // 主动回收，不是连接故障
		o.dropConn(rc)             // 原子摘除 + 状态回落（ownerBefore 保护新连接 running）
		if rc.client != nil {
			_ = rc.client.Close() // watchConn 收尾（dropConn 幂等）
		}
	}
	return len(victims)
}

// StartIdleReaper 启动空闲回收 goroutine（每 interval 扫一次，idle 为闲置阈值；
// <=0 任一 → 不启动）。Close 时停止。
func (o *Orchestrator) StartIdleReaper(interval, idle time.Duration) {
	if interval <= 0 || idle <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.mu.Lock()
	if o.reapStop != nil {
		o.mu.Unlock()
		cancel()
		return // 已启动（幂等）
	}
	o.reapStop = cancel
	o.mu.Unlock()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if n := o.reapIdleOnce(now, idle); n > 0 {
					glog.Infow(context.Background(), "orchestrator: 空闲回收", "reaped", n, "idle", idle.String())
				}
			}
		}
	}()
}
