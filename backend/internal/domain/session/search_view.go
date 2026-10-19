package session

// ——— 搜索命中面的对外形状（v3.2 刀 9）———
//
// 只有字段、没有 json tag：对外形态（`dtosearch.*`）由用例层 `svcsearch` 投影。
type MessageSearchHit struct {
	SessionID string
	Title     string
	RuntimeID string
	Role      string
	Seq       int64
	SeqHi     int64
	MessageID string
	RunID     string
	Snippet   string
	CreatedAt string
}

// MessageSearchResult 一次消息搜索的结果。
type MessageSearchResult struct {
	Query   string
	Source  string
	Hits    []MessageSearchHit
	Total   int
	HasMore bool
}
