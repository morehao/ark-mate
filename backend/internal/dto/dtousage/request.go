package dtousage

// UsageQueryReq 用量统计查询参数（GET /usage/summary）。
type UsageQueryReq struct {
	// From/To 半开区间 [From, To)（RFC3339 或 YYYY-MM-DD；空 = 不限该侧）。
	From string
	To   string
	// AgentID 仅统计该 agent（空 = 全部；未知 agent → 40401）。
	AgentID string
	// GroupBy agent（默认）| day（UTC 日期）。
	GroupBy string
}
