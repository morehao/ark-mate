package command

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestWatchParentExit 父进程 id 变化即回调（桌面壳被强杀时的自保路径）。
//
// 真的杀掉父进程在单测里做不到（测试进程就是父进程），因此注入 getppid 序列：
// 前两次不变、第三次变化 → 必须且只回调一次。
func TestWatchParentExit(t *testing.T) {
	var calls int32
	var ppid int32 = 100
	getppid := func() int { return int(atomic.LoadInt32(&ppid)) }

	done := make(chan struct{})
	go func() {
		watchParentExit(context.Background(), getppid, 5*time.Millisecond, func() {
			atomic.AddInt32(&calls, 1)
			close(done)
		})
	}()

	time.Sleep(20 * time.Millisecond) // 让它在"父进程还在"的状态下转几圈
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("父进程未变时不应回调，实际 %d 次", n)
	}
	atomic.StoreInt32(&ppid, 1) // 模拟被 reparent（父进程已死）

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("父进程变化后 2s 内没有回调")
	}
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("回调应恰好一次，实际 %d 次", n)
	}
}

// TestWatchParentExitStopsOnContextCancel ctx 取消（正常停机）后不再轮询、也不回调。
func TestWatchParentExitStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int32
	var ppid int32 = 100
	go watchParentExit(ctx, func() int { return int(atomic.LoadInt32(&ppid)) }, 5*time.Millisecond, func() {
		atomic.AddInt32(&calls, 1)
	})
	cancel()
	atomic.StoreInt32(&ppid, 1)
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("ctx 取消后不应回调，实际 %d 次", n)
	}
}
