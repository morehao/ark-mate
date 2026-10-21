package queue

import "time"

// ——— 排队输入的**领域类型**（C1 第一刀）———
//
// 为什么是 `domain/session/queue` 这样一个叶子包而不是直接放 `domain/session`：
// `store` 适配器要实现 `domain/session.Store` 端口，于是它必须 import 域类型；而域自己的
// **包内测试**又用真 SQLite 库（`testutil.OpenStore`）——同包测试 import 一个 import 本包的
// 包是 Go 明令禁止的（`import cycle not allowed in test`）。把**值类型**放叶子包后：
// `session` → `session/queue`、`store` → `session/queue`，两边都不再形成环。
// 等域测试改走内存 store（计划 §2.2 的"内存 store 跑通领域测试"）后，这个包可以并回 `session`。
//
// 改造前 `inputs` 行的形状（`model.InputEntity`）直接出现在域的 `Store` 端口签名里：领域因此
// import `adapters/outbound/sqlite/model`，换一份存储实现就要改域。现在领域自持 `QueueItem`，行结构只出现在
// `adapters/outbound/sqlite/store` 的行↔聚合映射里。
//
// 字段与列一一对应（含台账列 `Status` / `Seq` / `ClaimedAt`）：`QueuedInput` 是对外投影
// （REST/事件形状），`QueueItem` 是**完整聚合**——两者刻意分开，投影加字段不该逼聚合改形状。

// 队列项状态（与 inputs.status 同值；权威取值集见 `QueuedInputReasons` 的另一侧）。
const (
	// StatusQueued 等待投递。
	StatusQueued = "queued"
	// StatusDelivered 已投递。
	StatusDelivered = "delivered"
	// StatusStale 已作废（进程重启 / 会话执行代际已变）。
	StatusStale = "stale"
)

// Item 一条排队输入（一张 `inputs` 行的完整形状）。
type Item struct {
	// ID 平台铸号 `inp_<26 位 ULID>`。
	ID string
	// SessionID 所属会话。
	SessionID string
	// Text 用户原文（投递时原样作为 prompt 发出）。
	Text string
	// Delivery 投递模式（`after_run` / `interrupt_then_prompt`；入队路径不会写 reject）。
	Delivery string
	// TargetRunID 入队时正在执行的那一轮（审计快照，不作投递判据）。
	TargetRunID string
	// Seq 会话内位次（队列顺序的判据）。
	Seq int64
	// Generation 入队时的会话执行代际（投递前比对，不等即作废）。
	Generation int64
	// Status 队列项状态（`Status*`）。
	Status string
	// CreatedAt 入队时刻。
	CreatedAt time.Time
	// ClaimedAt 被 claim（投递）的时刻；未投递 → 零值。
	ClaimedAt time.Time
}
