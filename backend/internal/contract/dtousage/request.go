package dtousage

// UsageQueryReq 用量统计查询参数（GET /usage/summary）。
type UsageQueryReq struct {
	// From/To 半开区间 [From, To)（RFC3339 或 YYYY-MM-DD；空 = 不限该侧）。
	//
	// 日期形式（YYYY-MM-DD）按 `TZOffset` 指定的时区解释——界面给的是**用户本地日期**，
	// 不是 UTC 日期。RFC3339 自带偏移，按它自己的瞬时解析（不受 TZOffset 影响）。
	From string
	To   string
	// TZOffset 客户端相对 UTC 的偏移分钟数（东为正，如 Asia/Shanghai = 480；空 = 0/UTC）。
	//
	// 为什么必须有它：day 桶的键是**用户看到的日期**。此前只按 UTC 分桶，UTC+8 的用户
	// 每天 00:00–08:00 的轮次会被算进前一天，区间边界也整体偏 8 小时——界面上看不出来，
	// 只是数字对不上（`docs/unimplemented.md` C28／原 O8 的那条「跨时区 ≤1 天偏差」）。
	TZOffset string
	// GroupBy 只支持 day（键 = `TZOffset` 时区下的日期）。
	GroupBy string
	// AgentID 是**已下线的查询键**（v19：agent 概念整个并入 runtime）。
	//
	// 留着它只为**显式拒绝**：gin 的 `c.Query` 对不认识的键一个字都不说，用户带
	// `?agent_id=x` 会得到一份看起来正常的全量统计——"我筛了那个底座"与"平台忽略了它"
	// 在响应里长得一模一样（B1 类静默 no-op）。非空即 40001（见 `UsageSummary`）。
	AgentID string
}
