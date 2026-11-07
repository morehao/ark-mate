package runtimeconn

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/runtime"
)

// TestKeyLockSerializesSameKey 同键拨号锁必须**串行**：并发的同键临界区不能重叠。
//
// 这是 Dial「查 → 拨 → 登记」singleflight 的机制判据。修复前 Dial 无键级串行化，
// 两个并发请求都 miss Get、各起一条连接，后者覆盖前者（孤儿进程）；这里的锁保证了
// 第二个进入临界区时第一个已登记，从而命中 Get 复用。
func TestKeyLockSerializesSameKey(t *testing.T) {
	pl := NewPool(nil)
	const goroutines = 16

	var inCritical atomic.Int32
	var maxConcurrent atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := pl.keyLock("same-key")
			defer unlock()
			n := inCritical.Add(1)
			// 记录进入临界区的最大并发数：同键锁下应恒为 1。
			for {
				cur := maxConcurrent.Load()
				if n <= cur || maxConcurrent.CompareAndSwap(cur, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond) // 拉长临界区，放大竞争窗口
			inCritical.Add(-1)
		}()
	}
	wg.Wait()

	if got := maxConcurrent.Load(); got != 1 {
		t.Fatalf("同键临界区最大并发应为 1（串行），got %d", got)
	}
}

// TestKeyLockAllowsDifferentKeys 不同键互不阻塞（键级锁，不是全局锁）。
func TestKeyLockAllowsDifferentKeys(t *testing.T) {
	pl := NewPool(nil)
	// 先占住 key A，再在 key B 上取锁：若 B 能立刻拿到，说明锁是键级而非全局。
	unlockA := pl.keyLock("A")

	got := make(chan struct{})
	go func() {
		unlockB := pl.keyLock("B")
		unlockB()
		close(got)
	}()

	select {
	case <-got:
		// 好：不同键不互相阻塞。
	case <-time.After(2 * time.Second):
		t.Fatal("不同键被互相阻塞了：锁退化成了全局锁")
	}
	unlockA()
}

// TestAddDisplacesSameKeyWithoutLeak 同键二次 Add 必须拆除被替换者，不得静默覆盖成孤儿。
//
// 正常情况下 Dial 的键级锁保证同键只 Add 一次；这里直接调 Add 模拟「绕过锁的另一条登记路径」，
// 断言：表里换成新连接、被替换者被 Intentional 标记（走主动拆除），而不是被无声丢弃。
func TestAddDisplacesSameKeyWithoutLeak(t *testing.T) {
	pl := NewPool(nil)
	old := &Conn{Key: "k1", Spec: runtime.Spec{ID: "rt"}}
	pl.Add(old)
	if pl.Get("k1") != old {
		t.Fatal("首次 Add 后应能取到 old")
	}

	nu := &Conn{Key: "k1", Spec: runtime.Spec{ID: "rt"}}
	pl.Add(nu)

	if got := pl.Get("k1"); got != nu {
		t.Fatalf("同键二次 Add 后表里应是新连接，got %p want %p", got, nu)
	}
	if pl.Len() != 1 {
		t.Fatalf("同键二次 Add 后连接数应为 1（不产生第二条），got %d", pl.Len())
	}
	if !old.Intentional.Load() {
		t.Fatal("被替换的旧连接应被标记 Intentional（主动拆除），否则会泄漏成孤儿")
	}
}
