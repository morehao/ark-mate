package runtimeconn

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/adapters/outbound/procreg"
	"github.com/morehao/golib/glog"
)

// ——— P4a 正文第五十四刀：子进程登记与空闲回收（随连接池搬入） ———
//
// 这两件事此前是 `manager` 的 `orphan_reap.go`（145 行）与 `connection.go` 里的
// `reclaimConns`。它们只吃「连接对象 + 进程注册表 + 一个'还有人用吗'的判据」，而这个判据
// 现在经 `DropSink.InUseOn` 由编排侧回答——于是整个文件可以待在执行面，不认识会话表。

// registerProc 把刚拉起的子进程交给执行面登记。
//
// 本函数只做"连接对象 → 注册项"的映射与 pid 落位；登记/告警/回收都在 `Plane` 里
// （那是"哪些子进程是我的"的唯一事实源）。
func (pl *Pool) registerProc(c *Conn) {
	if c == nil || c.client == nil || pl.bind == nil {
		return
	}
	// pid 先落到连接上：诊断面（A3 /diagnostics）不依赖进程注册表是否启用。
	info := c.client.PidInfo()
	if info.PID > 0 {
		c.PID = info.PID
	}
	if info.PID <= 0 {
		return
	}
	pl.bind.RegisterProc(procreg.Entry{
		PID: info.PID, PGID: info.PGID, Command: info.Command,
		Runtime: c.spec.ID,
	})
}

// unregisterProc 摘除登记（正常回收路径）。
func (pl *Pool) unregisterProc(c *Conn) {
	if c == nil || pl.bind == nil {
		return
	}
	pl.bind.UnregisterProc(c.PID)
}

// ReapIdle 空闲回收单轮：扫全部连接，回收「无在途 run、无任何会话引用、且自建立起空闲
// 超过 idle」的暖进程（P2 StartAgent 常驻类）；锁内判定 + Drop 原子摘除 → 锁外停进程，
// 下次会话/StartAgent 经 acquire 按需拉起（与 P1 断点恢复合流：连接丢失后 prompt 自动重建）。
// 返回回收数量。
func (pl *Pool) ReapIdle(now time.Time, idle time.Duration) int {
	pl.mu.Lock()
	candidates := make([]*Conn, 0, len(pl.conns))
	for _, c := range pl.conns {
		if c.Started.IsZero() || now.Sub(c.Started) < idle {
			continue
		}
		candidates = append(candidates, c)
	}
	pl.mu.Unlock()
	n := 0
	for _, c := range candidates {
		if pl.inUse(c) {
			continue // 有在途 run 或仍有会话引用：不回收（回收语义只覆盖暖进程）
		}
		c.Intentional.Store(true) // 主动回收，不是连接故障
		pl.Drop(c)                // 原子摘除 + 状态回落（wasCurrent 保护新连接 running）
		if c.client != nil {
			_ = c.client.Close() // watch 收尾（Drop 幂等）
		}
		n++
	}
	return n
}

// StartIdleReaper 启动空闲回收 goroutine（每 interval 扫一次，idle 为闲置阈值；
// <=0 任一 → 不启动）。CloseAll 时停止。
func (pl *Pool) StartIdleReaper(interval, idle time.Duration) {
	if interval <= 0 || idle <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	pl.mu.Lock()
	if pl.reapStop != nil {
		pl.mu.Unlock()
		cancel()
		return // 已启动（幂等）
	}
	pl.reapStop = cancel
	pl.mu.Unlock()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if n := pl.ReapIdle(now, idle); n > 0 {
					glog.Infow(context.Background(), "orchestrator: 空闲回收", "reaped", n, "idle", idle.String())
				}
			}
		}
	}()
}
