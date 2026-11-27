package session

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestMemorySessionWriterSingleWinner 是 M2 出口闸「同会话并发正确（零双写）」的进程内证明：
// N 个 goroutine 同时 TryAcquire 同一会话，**恰好一个**得 true——即任何时刻同一会话只有一个写者，
// 双写=0。这是单写者端口最核心的判据。
func TestMemorySessionWriterSingleWinner(t *testing.T) {
	w := NewMemorySessionWriter()
	const n = 256
	var wins int64
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer done.Done()
			start.Wait() // 尽量让所有 goroutine 同时起跑，最大化竞争
			if w.TryAcquire("ses_race") {
				atomic.AddInt64(&wins, 1)
			}
		}()
	}
	start.Done()
	done.Wait()
	if wins != 1 {
		t.Fatalf("同一会话并发 TryAcquire 应恰好 1 个赢家（零双写），实际 %d 个——出现双写！", wins)
	}
}

// TestMemorySessionWriterReleaseAllowsReacquire：释放后可再次取得；释放幂等（多释放不 panic、不影响别人）。
func TestMemorySessionWriterReleaseAllowsReacquire(t *testing.T) {
	w := NewMemorySessionWriter()
	if !w.TryAcquire("s1") {
		t.Fatal("首次取得应成功")
	}
	if w.TryAcquire("s1") {
		t.Fatal("持有时再取应失败（单写者）")
	}
	w.Release("s1")
	w.Release("s1") // 幂等：再次释放是 no-op
	if !w.TryAcquire("s1") {
		t.Fatal("释放后应可再次取得")
	}
}

// TestMemorySessionWriterSessionsIndependent：不同会话互不阻塞（单写者是**会话级**，不是全局）。
func TestMemorySessionWriterSessionsIndependent(t *testing.T) {
	w := NewMemorySessionWriter()
	if !w.TryAcquire("a") || !w.TryAcquire("b") {
		t.Fatal("不同会话应能各自取得写权（会话级互斥，不串行化整个系统）")
	}
	if w.TryAcquire("a") {
		t.Fatal("a 已被持有，再取应失败")
	}
	w.Release("a")
	if !w.TryAcquire("a") {
		t.Fatal("a 释放后应可再取，且不影响 b 的持有")
	}
	// b 仍在持有（上面没释放 b）——不应因 a 的变动而受影响。
	if w.TryAcquire("b") {
		t.Fatal("b 应仍在持有中")
	}
}

// TestMemorySessionWriterEmptyIDNeverOwned：空 id 不作为合法会话，恒 false（防误把空串当会话锁住）。
func TestMemorySessionWriterEmptyIDNeverOwned(t *testing.T) {
	w := NewMemorySessionWriter()
	if w.TryAcquire("") {
		t.Fatal("空 sessionID 不该被当作可持有的会话")
	}
}
