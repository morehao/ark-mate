package dtosearch

// MessageSearchHit 一条消息级命中（跨会话；seq 为锚点，前端据此滚动定位）。
type MessageSearchHit struct {
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	Role           string `json:"role"` // user | assistant
	Seq            int64  `json:"seq"`
	SeqHi          int64  `json:"seq_hi,omitempty"`
	MessageID      int64  `json:"message_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	Snippet        string `json:"snippet"`
	CreatedAt      string `json:"created_at"`
}

// MessageSearchResp 消息级搜索响应（source 标明数据面，便于排障与降级提示）。
type MessageSearchResp struct {
	Query   string             `json:"query"`
	Source  string             `json:"source"` // sqlite | transcript
	Hits    []MessageSearchHit `json:"hits"`
	Total   int                `json:"total"`
	HasMore bool               `json:"has_more"`
}
