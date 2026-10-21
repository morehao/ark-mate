package assembly

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSequenceStartStopSymmetric 钉住两条最容易静默漂移的形状：
//
//	① 启动按声明顺序、关闭按**逆序**（关闭顺序与启动不对称的表现是"退出时偶发丢数据"，
//	   而任何单测都不会因此变红）；
//	② 只关闭**真的启动过**的步骤（启动失败回滚之后，未启动的 Stop 不得被调用——否则
//	   你会去关一个还没造出来的东西，第二个错误把第一个错误的现场埋掉）。
func TestSequenceStartStopSymmetric(t *testing.T) {
	var order []string
	step := func(name string) Step {
		return Step{
			Name: name,
			Start: func(context.Context) error {
				order = append(order, "start:"+name)
				return nil
			},
			Stop: func(context.Context) error {
				order = append(order, "stop:"+name)
				return nil
			},
		}
	}
	seq := NewSequence(step("a"), step("b"), step("c"))
	if err := seq.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := join(order); got != "start:a,start:b,start:c" {
		t.Fatalf("启动顺序不是声明顺序: %s", got)
	}
	order = nil
	if errs := seq.Stop(context.Background()); len(errs) != 0 {
		t.Fatalf("Stop 报错: %v", errs)
	}
	if got := join(order); got != "stop:c,stop:b,stop:a" {
		t.Fatalf("关闭顺序不是启动的逆序: %s", got)
	}
	// 幂等：第二次 Stop 不得重复关任何东西（关闭动作本身也应当幂等，这是双保险）。
	order = nil
	_ = seq.Stop(context.Background())
	if len(order) != 0 {
		t.Fatalf("第二次 Stop 又执行了关闭步骤: %v", order)
	}
}

// TestSequenceCriticalFailureRollsBack 钉住 critical 的语义：失败终止启动，并把**已启动**的
// 步骤按逆序回滚。改造前 `Init` 中途失败不会关闭已开的库句柄——那是一条真实的资源泄漏。
func TestSequenceCriticalFailureRollsBack(t *testing.T) {
	var order []string
	boom := errors.New("坏了")
	seq := NewSequence(
		Step{Name: "opened", Start: func(context.Context) error {
			order = append(order, "start:opened")
			return nil
		}, Stop: func(context.Context) error {
			order = append(order, "stop:opened")
			return nil
		}},
		Step{Name: "critical", Critical: true, Start: func(context.Context) error {
			order = append(order, "start:critical")
			return boom
		}},
		Step{Name: "never", Start: func(context.Context) error {
			order = append(order, "start:never")
			return nil
		}, Stop: func(context.Context) error {
			order = append(order, "stop:never")
			return nil
		}},
	)
	err := seq.Start(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("critical 失败应被返回（且用 %%w 保留原错误）: %v", err)
	}
	if got := join(order); got != "start:opened,start:critical,stop:opened" {
		t.Fatalf("回滚形状不对（未启动的步骤不得被关闭）: %s", got)
	}
}

// TestSequenceNonCriticalContinues 钉住"只该 WARN 的步骤"：非 critical 失败必须继续启动。
// 这一条与 critical 那条是一对——只钉一侧，判据就只剩"总是回滚"或"从不回滚"两种错法之一。
func TestSequenceNonCriticalContinues(t *testing.T) {
	var order []string
	seq := NewSequence(
		Step{Name: "warn", Start: func(context.Context) error {
			order = append(order, "start:warn")
			return errors.New("可选目录缺失")
		}},
		Step{Name: "next", Critical: true, Start: func(context.Context) error {
			order = append(order, "start:next")
			return nil
		}},
	)
	if err := seq.Start(context.Background()); err != nil {
		t.Fatalf("非 critical 失败不该终止启动: %v", err)
	}
	if got := join(order); got != "start:warn,start:next" {
		t.Fatalf("非 critical 失败后必须继续: %s", got)
	}
}

// TestSequenceTimeoutIsReported 步骤自己不观察 ctx 时（例如 `store.Open`），超时无法中断它，
// 但必须在返回后如实报出来——"超了还当成功继续"是这一层最坏的假绿。
func TestSequenceTimeoutIsReported(t *testing.T) {
	seq := NewSequence(Step{
		Name: "slow", Critical: true, Timeout: 5 * time.Millisecond,
		Start: func(context.Context) error {
			time.Sleep(20 * time.Millisecond)
			return nil
		},
	})
	err := seq.Start(context.Background())
	if !IsTimeout(err) {
		t.Fatalf("超时应被报成 DeadlineExceeded，实际: %v", err)
	}
}

func join(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
