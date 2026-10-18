package model

import "time"

// ——— 查询条件的列形状（v3.1 P4 从 `internal/store` 上移）———
//
// 为什么上移：`internal/domain/session` 要通过一个**窄端口**读库（它不 import store），
// 而这些结构出现在端口的方法签名里。它们是"查询条件/结果行的形状"，属于本包职责。
// 名字与字段一个未改：这次只是搬家，落库语义与 JSON 形状都不变。

// UsageQuery 用量聚合查询条件（只有时间区间；分组恒为 day）。
type UsageQuery struct {
	From time.Time // 零值 = 不限起点
	To   time.Time // 零值 = 不限终点（半开区间）
	// Location 分桶用的日历时区（nil = UTC）。由用例层从客户端的 tz_offset 造出来，
	// 存储层不认识"偏移分钟"这种传输形态。
	Location *time.Location
}

// 费用币种约定：
//   - 底座未给币种（空串）按 USD 归一（与前端 `$` 展示口径一致，避免同一美元被拆成两个币种）；
//   - 一个桶内出现多种币种时不可相加：金额置零、币种标 CostCurrencyMixed，明细走 CostByCurrency。
const (
	// CostCurrencyDefault 底座未上报币种时的归一币种。
	CostCurrencyDefault = "USD"
	// CostCurrencyMixed 桶内多币种哨兵（此时 CostAmount 为 0，明细见 CostByCurrency）。
	CostCurrencyMixed = "MIXED"
)

// UsageBucket 一行聚合结果（group_by=day → Key=YYYY-MM-DD）。
type UsageBucket struct {
	Key           string `json:"key"`
	Runs          int    `json:"runs"`
	Completed     int    `json:"completed"`
	Failed        int    `json:"failed"`
	Cancelled     int    `json:"cancelled"`
	Running       int    `json:"running"`
	Sessions      int    `json:"sessions"`
	DurationMS    int64  `json:"duration_ms"`
	ContextUsed   int64  `json:"context_used"`
	ContextSize   int64  `json:"context_size"`
	UsageReported int    `json:"usage_reported"`
	// CostReported 上报过费用的会话数（与 UsageReported 分开：二者上报率不同）。
	CostReported int     `json:"cost_reported"`
	CostAmount   float64 `json:"cost_amount"`
	CostCurrency string  `json:"cost_currency,omitempty"`
	// CostByCurrency 多币种（CostCurrency=CostCurrencyMixed）时的分币种金额；单币种为空。
	CostByCurrency map[string]float64 `json:"cost_by_currency,omitempty"`
}

// UsageSummaryResult 聚合结果：全区间总计 + 分组桶（day 键升序）。
type UsageSummaryResult struct {
	Totals  UsageBucket   `json:"totals"`
	Buckets []UsageBucket `json:"buckets"`
}

// MessageHit 一条全局消息命中（跨会话搜索 + 锚点跳转用）。
type MessageHit struct {
	SessionID    string
	SessionTitle string
	RuntimeID    string
	Role         string
	MessageID    string
	RunID        string
	Seq          int64 // 锚点：消息起始事件序号（前端据此滚动定位）
	SeqHi        int64
	Text         string
	CreatedAt    time.Time
}

// MessageTextHit 会话内消息文本命中（搜索用）。
type MessageTextHit struct {
	SessionID string
	Role      string
	Text      string
}
