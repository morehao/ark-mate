// Package objtime 是平台内部的**时间文本约定**（纯值，零 I/O、零业务依赖）。
//
// 为什么需要它：领域读模型要把时间折成对外契约里的 ISO 文本，而库侧的时间布局住在
// `internal/adapters/outbound/sqlite/model`（L0，领域不得 import）。两处必须**逐字同一个布局**，
// 否则「库里存什么」与「接口回什么」会悄悄分叉——`time.RFC3339Nano` 与
// `time.RFC3339` 在整秒上看起来一样，带小数秒时才不同，那种差异只会在某个客户端里炸。
//
// 与 `model.LayoutTS` 的关系：两者是同一约定的两份声明（L0 不能 import L1，方向不允许
// 反过来），由 `objtime_test.go` 钉住它们逐字相等。
package objtime

import "time"

// Layout 是平台上下的时间文本布局（与 `model.LayoutTS` 逐字相同）。
const Layout = time.RFC3339Nano

// Format 把时间格式化为接口/落库文本；零值 → 空串（"未发生"的唯一表示）。
func Format(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(Layout)
}

// Parse 解析时间文本；空串或不可解析 → 零值（读路径容错，不因单行异常中断整批）。
func Parse(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(Layout, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
