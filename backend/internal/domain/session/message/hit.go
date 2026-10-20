package message

import "time"

// Hit 一条全局消息命中（跨会话搜索 + 锚点跳转用）。
//
// `Seq` / `SeqHi` 是**锚点**：前端据此在会话里滚动定位到那条消息；它们不是"第几条消息"的
// 计数（那是会话内位次），所以两个字段都必须随命中一起出来。
type Hit struct {
	SessionID    string
	SessionTitle string
	RuntimeID    string
	Role         string
	MessageID    string
	RunID        string
	Seq          int64
	SeqHi        int64
	Text         string
	CreatedAt    time.Time
}

// TextHit 会话内消息文本命中（搜索片段与计数用）。
type TextHit struct {
	SessionID string
	Role      string
	Text      string
}
