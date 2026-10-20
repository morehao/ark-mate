// Package row 是自动化域的**行值类型**（叶子包，理由见 `session/queue` 的包注释）。
//
// 它与 `automation.Automation`（强类型定义）+ `automation.Rec`（调度态）的分工：
// `Item` 是 `automations` 表的完整行形状（定义正文 + 运行态列），`RuntimeState` / `Overlay`
// 是写运行态时的列载荷。三重转换（行 ⇄ 定义）住在领域里，store 只做形状翻译。
package row

import "time"

// Item 一条定时任务（一张 `automations` 行的完整形状）。
//
// **定义与运行态分列**：`Body` 只放用户写的定义（含 `state`），运行态走列。于是"用户往
// body 里塞运行态键"结构上不可能，而拒写名单仍然保留（否则用户会写出一个"写了不生效"的定义）。
//
// **运行态列不参与 revision CAS**：`Revision` / `UpdatedAt` 只记定义提交；调度器推进运行态
// 不动它们（否则面板上的定义会看起来"被别人改过"）。
type Item struct {
	ID       string
	Body     string
	Revision int64
	// UpdatedAt 最后一次**定义**提交的时刻。
	UpdatedAt time.Time
	// LastFiredAt 最近一次被认领的 occurrence 的文本形态（空 = 从未触发）。
	//
	// 它是"这个 occurrence 是否已触发过"的唯一判据，也是调度推进的 CAS 比较值——
	// 所以它在这一层就是**文本**（比较必须逐字节），格式化只有 `LastFiredText` 一处。
	LastFiredAt string
	LastStatus  string
	LastReason  string
	// LastSessionID 最近一次触发产生的会话（overlap 判据用它查会话是否还在跑）。
	LastSessionID string
	// ConsecutiveFailures 连续失败次数（跑成一次即清零）。
	ConsecutiveFailures int64
	// RecentRuns 最近若干次触发的留痕（JSON 数组文本，最新在前，有界）。
	RecentRuns string
}

// RuntimeState 一条定时任务的运行态（**列**；`state` 不在其中——它属于定义）。
type RuntimeState struct {
	LastFiredAt         string
	LastStatus          string
	LastReason          string
	LastSessionID       string
	ConsecutiveFailures int64
	RecentRuns          string
}

// Overlay 一次运行态写回（可能同时改定义里的 `state`）。
type Overlay struct {
	// Body 非空 = 同时覆盖定义正文（平台写 `state` 这类**非保留键**时用）。
	Body string
	// ExpectedRev 仅当 Body 非空时有意义（body 的 CAS 判据）。
	ExpectedRev int64
	// ExpectedLastFired 认领 CAS 判据（"这个 occurrence 还没被别人认领"）。
	ExpectedLastFired string
	// Runtime 要写入的运行态列。
	Runtime RuntimeState
}
