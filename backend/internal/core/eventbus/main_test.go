package eventbus

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain 给事件总线挂 goroutine 泄漏判据（goleak）。
//
// 为什么只在这里：bus 是全仓并发原语之一（订阅/广播/淘汰），且实测**零泄漏**——漏一个订阅者
// goroutine 就是一条永久吃内存的通道。这是本次「补泄漏判据」的第一个落点（P1-4）。
//
// 为什么只用 verify-none、不列 IgnoreTopFunction：bus 的测试路径里没有该常驻的后台 goroutine
// （订阅者都由测试自己启停），一旦将来有人在测试里忘了退订/停播，这里就会红——这正是想要的。
//
// 说明：sessionruntime / runtimeconn 两个包当前**有存量测试 hygiene 泄漏**（coalescer 的 loop、
// acp 传输读循环在部分用例里没 Close），暂不挂 goleak（会直接红）；待测试逐步补 Close 后再接入。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
