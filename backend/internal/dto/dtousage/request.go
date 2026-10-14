package dtousage

// UsageQueryReq 用量统计查询参数（GET /usage/summary）。
type UsageQueryReq struct {
	// From/To 半开区间 [From, To)（RFC3339 或 YYYY-MM-DD；空 = 不限该侧）。
	From string
	To   string
	// GroupBy 只支持 day（UTC 日期）。
	GroupBy string
	// AgentID 是**已下线的查询键**（v19：agent 概念整个并入 runtime）。
	//
	// 留着它只为**显式拒绝**：gin 的 `c.Query` 对不认识的键一个字都不说，用户带
	// `?agent_id=x` 会得到一份看起来正常的全量统计——"我筛了那个底座"与"平台忽略了它"
	// 在响应里长得一模一样（B1 类静默 no-op）。非空即 40001（见 `UsageSummary`）。
	AgentID string
}
