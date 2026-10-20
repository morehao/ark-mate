// Package event 是会话域的**事件值类型**（叶子包，理由见 `session/queue` 的包注释）。
//
// events 是 facts 的唯一事实源：`ID` 是身份（永不当游标），`Seq` 是会话内位次（0 起、稠密）。
package event

import "time"

// Item 一条事件（一张 `events` 行的完整形状）。
type Item struct {
	// ID 行身份（`evt_<26 位 ULID>`）。
	ID string
	// Seq 会话内位次（0 起、稠密；排序/比较/水位一律用它）。
	Seq int64
	// SessionID 所属会话。
	SessionID string
	// Type 事件类型（权威取值在 `domain/session/events.go`）。
	Type string
	// PayloadJSON 事件载荷（已序列化）。
	PayloadJSON string
	// CreatedAt 落库时刻。
	CreatedAt time.Time
}
