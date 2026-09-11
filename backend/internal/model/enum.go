package model

// 本文件是**持久层内部**使用的具名常量，用于消掉 SQL 与判空里的裸字面量。
//
// 刻意不把这些列定义成具名类型（`type ConversationStatus string`）：会话/轮次状态的
// 权威定义在 `internal/manager/events.go`，那里是一组**无类型字符串常量**，
// 且 manager 全程按 string 传参（`markConversation(id, status string)`、
// `ConversationMetaPatch{Status: &status}`）。把列改成具名类型会连锁改动 manager 全包
// 与其测试，收益只是类型别名，故此处只统一取值、不改变类型身份。
const (
	// DecisionAllow 权限记忆的「允许」方向。
	DecisionAllow = "allow"
	// DecisionReject 权限记忆的「拒绝」方向。
	DecisionReject = "reject"

	// RoleUser 用户消息角色。
	RoleUser = "user"
	// RoleAssistant 助手消息角色。
	RoleAssistant = "assistant"

	// MetaKeyEventsLastSeq 全局单调事件序号的计数行键（events.seq 的唯一分配源）。
	MetaKeyEventsLastSeq = "events_last_seq"
	// MetaKeyImportMarker transcript 导入标记：value = 导入目录指纹（幂等依据）。
	MetaKeyImportMarker = "imported_transcripts"
)
