package session

import (
	"fmt"
	"sync"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/acp"
)

// TestInFlightTracker 语义（§17 E1 显式化簿记，语义同 §1.5 修订）：
// 唯一在途 → SoleOwner 归属；无在途/多会话并发在途 → 空串不猜测；
// Mark/Unmark 幂等；Clear 作废连接登记；nil client 安全。
func TestInFlightTracker(t *testing.T) {
	tr := NewInFlightTracker()
	// nil client 安全。
	tr.Mark(nil, "x")
	if tr.SoleOwner(nil) != "" || tr.Len(nil) != 0 {
		t.Fatal("nil client 应空结果")
	}

	// 造两把"伪连接"：tracker 只按指针身份记账，无需真实 acp 连接。
	var c1, c2 acp.Client
	if s := tr.SoleOwner(&c1); s != "" {
		t.Fatal("无在途应返回空串")
	}

	// 单在途 → 唯一归属。
	tr.Mark(&c1, "conv-a")
	if s := tr.SoleOwner(&c1); s != "conv-a" {
		t.Fatalf("唯一在途应归属 conv-a，got %q", s)
	}
	tr.Mark(&c2, "conv-b") // 另一连接独立
	if s := tr.SoleOwner(&c2); s != "conv-b" {
		t.Fatalf("c2 唯一在途应归属 conv-b，got %q", s)
	}
	if s := tr.SoleOwner(&c1); s != "conv-a" {
		t.Fatal("c1 归属不受 c2 影响")
	}

	// 多会话并发在途（同连接）→ 归属不唯一，返回空串不猜测。
	tr.Mark(&c1, "conv-a2")
	if s := tr.SoleOwner(&c1); s != "" {
		t.Fatalf("并发在途应空串不猜测，got %q", s)
	}
	if n := tr.Len(&c1); n != 2 {
		t.Fatalf("c1 在途数应 2，got %d", n)
	}

	// Unmark 到唯一 → 恢复归属。
	tr.Unmark(&c1, "conv-a2")
	if s := tr.SoleOwner(&c1); s != "conv-a" {
		t.Fatalf("撤销一个后应归属 conv-a，got %q", s)
	}

	// Unmark 幂等 + 清空后键删除。
	tr.Unmark(&c1, "conv-a")
	tr.Unmark(&c1, "conv-a")
	if tr.Len(&c1) != 0 || tr.SoleOwner(&c1) != "" {
		t.Fatal("清空后应无在途")
	}

	// Clear 作废连接登记。
	tr.Mark(&c1, "conv-a")
	tr.Clear(&c1)
	tr.Unmark(&c1, "conv-a") // 对已清键 no-op
	if tr.Len(&c1) != 0 {
		t.Fatal("Clear 后应无在途")
	}
	tr.Clear(nil) // nil 安全
}

// TestInFlightTrackerConcurrent 并发安全：多 goroutine Mark/Unmark/SoleOwner/Len 竞争下
// 计数最终归零且不 panic（race 检测覆盖）。
func TestInFlightTrackerConcurrent(t *testing.T) {
	tr := NewInFlightTracker()
	var c acp.Client
	const (
		workers = 8
		each    = 200
	)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				id := fmt.Sprintf("w%d-%d", w, i)
				tr.Mark(&c, id)
				tr.Len(&c)
				tr.SoleOwner(&c)
				tr.Unmark(&c, id)
			}
		}(w)
	}
	wg.Wait()
	if n := tr.Len(&c); n != 0 {
		t.Fatalf("并发结束后应 0 在途，got %d", n)
	}
}
