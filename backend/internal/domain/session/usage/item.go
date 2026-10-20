// Package usage 是会话域的**用量聚合值类型**（叶子包，理由见 `session/queue` 的包注释）。
//
// 它是「一次用量查询的条件 + 聚合结果」的形状：条件只有时间区间（分组恒为 day），结果是
// 总计 + 日历日桶。store 适配器按这些值做聚合，投影成对外 DTO 是会话域的事。
package usage

import "time"

// Query 用量聚合查询条件（只有时间区间；分组恒为 day）。
type Query struct {
	From time.Time // 零值 = 不限起点
	To   time.Time // 零值 = 不限终点（半开区间）
	// Location 分桶用的日历时区（nil = UTC）。由用例层从客户端的 tz_offset 造出来，
	// 存储层不认识"偏移分钟"这种传输形态。
	Location *time.Location
}

// 费用币种约定：
//   - 底座未给币种（空串）按 USD 归一（与前端 `$` 展示口径一致，避免同一美元被拆成两个币种）；
//   - 一个桶内出现多种币种时不可相加：金额置零、币种标 Mixed，明细走 CostByCurrency。
const (
	// CostCurrencyDefault 底座未上报币种时的归一币种。
	CostCurrencyDefault = "USD"
	// CostCurrencyMixed 桶内多币种哨兵（此时 CostAmount 为 0，明细见 CostByCurrency）。
	CostCurrencyMixed = "MIXED"
)

// Bucket 一行聚合结果（group_by=day → Key=YYYY-MM-DD）。
type Bucket struct {
	Key            string
	Runs           int
	Completed      int
	Failed         int
	Cancelled      int
	Running        int
	Sessions       int
	DurationMS     int64
	ContextUsed    int64
	ContextSize    int64
	UsageReported  int
	CostReported   int
	CostAmount     float64
	CostCurrency   string
	CostByCurrency map[string]float64
}

// Summary 聚合结果：全区间总计 + 分组桶（day 键升序）。
type Summary struct {
	Totals  Bucket
	Buckets []Bucket
}
