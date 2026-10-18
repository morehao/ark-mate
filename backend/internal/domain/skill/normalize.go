package skill

import (
	"github.com/morehao/ark-mate/internal/capability"
	"github.com/morehao/ark-mate/internal/runtime"
)

// NormalizeRuntimeDecls 归一一条技能的投放声明（`runtimes[]`）。
//
// 归一而不是拒绝：底座 id 的大小写与空白写法在用户定义里很随意，而这条声明要参与
// **连接键**的计算（同一声明必须得到同一个键，否则同一份声明会被当成两份而重开子进程）。
// 判据在 `pkg/runtime.NormalizeIDs` 一处实现——这里只负责"在哪一步归一"。
//
// 纯函数（只改传入的定义），所以它随技能平面走，不需要 `*Service` 的任何状态。
func NormalizeRuntimeDecls(sk *capability.Skill) {
	sk.Runtimes = runtime.NormalizeIDs(sk.Runtimes)
}
