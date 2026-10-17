package dtousage

// UsageBucketResp 一行用量聚合（group_by=day → key=YYYY-MM-DD）。
type UsageBucketResp struct {
	Key         string `json:"key"`
	Runs        int    `json:"runs"`
	Completed   int    `json:"completed"`
	Failed      int    `json:"failed"`
	Cancelled   int    `json:"cancelled"`
	Running     int    `json:"running"`
	Sessions    int    `json:"sessions"`
	DurationMS  int64  `json:"duration_ms"`
	ContextUsed int64  `json:"context_used"`
	ContextSize int64  `json:"context_size"`
	// UsageReported 上报过上下文的会话数；CostReported 上报过费用的会话数（两者分开，不可互相替代）。
	UsageReported int `json:"usage_reported"`
	CostReported  int `json:"cost_reported"`
	// CostAmount/CostCurrency：快照金额与币种；多币种时 amount=0、currency=MIXED、明细走 CostByCurrency。
	CostAmount     float64            `json:"cost_amount"`
	CostCurrency   string             `json:"cost_currency,omitempty"`
	CostByCurrency map[string]float64 `json:"cost_by_currency,omitempty"`
}

// UsageSummaryResp 用量统计响应：总计 + 分组桶 + 生效条件回显。
type UsageSummaryResp struct {
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	GroupBy string `json:"group_by"`
	// TZOffset 生效的客户端时区偏移分钟数（东为正；0 = UTC）。day 桶的键与日期参数都按它解释。
	TZOffset int    `json:"tz_offset"`
	Source   string `json:"source"` // sqlite | memory（memory 模式无统计，返回 40501）
	// ContextSemantics/CostSemantics 口径：均为「区间末快照」，不是区间增量。
	ContextSemantics string `json:"context_semantics"`
	CostSemantics    string `json:"cost_semantics"`
	// BucketsAdditive 桶之间能否相加：day 分组不可加（跨天会话的累计快照会在多个日期桶重复出现）。
	BucketsAdditive bool              `json:"buckets_additive"`
	Totals          UsageBucketResp   `json:"totals"`
	Buckets         []UsageBucketResp `json:"buckets"`
}
