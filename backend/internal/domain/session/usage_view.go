package session

// ——— 用量面的对外形状（v3.2 刀 9）———
//
// 只有字段、没有 json tag：对外形态（`dtousage.*`）由用例层 `svcusage` 投影。

// UsageQuery 一次用量聚合的请求。**已下线的查询键不在这里**：那是"请求形状"的事，
//
// 归用例层的绑定期处理（`svcusage`），领域只收它真正要用的维度。
type UsageQuery struct {
	From     string
	To       string
	TZOffset string
	GroupBy  string
}

// UsageBucket 一个时间桶（或总计）。
type UsageBucket struct {
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

// UsageSummary 聚合结果。
type UsageSummary struct {
	From             string
	To               string
	GroupBy          string
	TZOffset         int
	Source           string
	ContextSemantics string
	CostSemantics    string
	BucketsAdditive  bool
	Totals           UsageBucket
	Buckets          []UsageBucket
}
