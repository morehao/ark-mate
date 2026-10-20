// Package memory 是会话域的**权限记忆值类型**（叶子包，理由见 `session/queue` 的包注释）：
// 域与 store 适配器共同依赖它，但谁都不 import 对方，避免 Go 的 test import cycle。
package memory

import "time"

// Item 一条会话级权限记忆（一张 `permission_memories` 行的完整形状）。
//
// 唯一键是 `(session_id,title,kind)`；`Scope` 与 `GrantInputDigest` 描述的是**最近一次**
// 那条授权（同键覆盖时一起刷新，见 store 的 Upsert 口径）。
type Item struct {
	// ID 平台铸号 `pm_<26 位 ULID>`。
	ID string
	// SessionID 所属会话。
	SessionID string
	// Title 工具标题（与 Kind 一起构成"哪一类调用"）。
	Title string
	// Kind 工具类别。
	Kind string
	// Decision 决策方向（`allow` / `reject`）。
	Decision string
	// Scope 授权形态（今天唯一可写值 `session`）。
	Scope string
	// GrantInputDigest 当初那次授权那条工具入参的摘要（审计用，不进唯一键）。
	GrantInputDigest string
	// CreatedAt 授予时刻。
	CreatedAt time.Time
}
