package acl

// ACP `_meta` 归属读取（C3 归属来源扩展）。
//
// 背景：SDK v0.13.5 未建模 elicitation 的规范 scope 字段（顶层 sessionId/requestId 在解码时被丢弃），
// 但**变体内的 `_meta` 会被完整保留**（变体解码走 json.Unmarshal 到具体类型，
// 见 acp-go-sdk@v0.13.5 types_gen.go 的 UnstableCreateElicitationRequest 分支）。
// 因此把 scope 放进 `_meta` 的发送方**今天就能零 fork 读到** → 本层先读 `_meta` 置 ScopeWire，
// 读不到才由宿主按连接上下文推断（ScopeInferred）；两者都不成立时置 ScopeAmbiguous。
//
// 读取原则：**只认非空字符串**、键按序回退、宁可不认也不猜——把数字/对象/空串当成归属，
// 比不归属更坏（会把提问卡投到错误的会话上）。

// metaSessionKeys 会话归属键序列（发送方风格不一，按此顺序取第一个命中的非空字符串）。
var metaSessionKeys = []string{"sessionId", "session_id", "conversationId", "conversation_id"}

// metaRequestKeys JSON-RPC 请求归属键序列（权限请求在会话外的可寻址键，对齐 compozy
// permission_helpers.go 的 request_id/requestId 回退）。
var metaRequestKeys = []string{"requestId", "request_id"}

// scopeFromMeta 从 `_meta` 按 keys 顺序取第一个非空字符串值；无命中返回空串。
func scopeFromMeta(meta map[string]any, keys []string) string {
	for _, k := range keys {
		if s, ok := meta[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// sessionIDFromMeta 从 `_meta` 读会话归属（elicitation/权限两侧共用；无命中返回空串）。
func sessionIDFromMeta(meta map[string]any) string {
	return scopeFromMeta(meta, metaSessionKeys)
}

// requestIDFromMeta 从 `_meta` 读请求归属键（requestId/request_id；无命中返回空串）。
func requestIDFromMeta(meta map[string]any) string {
	return scopeFromMeta(meta, metaRequestKeys)
}
