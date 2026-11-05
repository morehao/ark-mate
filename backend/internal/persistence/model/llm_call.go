package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameLLMCall 内部模型调用的**流水**表（一次调用一行，可跨调用累加）。
	//
	// 名字用 `llm_` 前缀而非 `model_`：`models`（模型清单）回答"能用什么"，`llm_calls` 回答
	// "实际调了什么"——两者语义正交，前缀必须能一眼分开（详见 objid 的 Call 与 objusage 包注释）。
	TableNameLLMCall = "llm_calls"
)

// LLMCallEntity 是 llm_calls 表的一行：**一次模型调用**的流水。
//
// 语义与红线：
//   - 一次调用 = 一行，**可跨行累加**（这是"流水"，不是"快照"——勿与 `runs.usage_json` 的
//     上下文快照混淆，见 internal/core/object/objusage 包注释）。
//   - `SessionID` / `RunID` **可空**是明确设计：旁路（无会话）调用两者皆空。会话调用的流水
//     就是 `session_id != ”` 那批——聚合按 `purpose / model / provider / session_id` 任意切。
//   - 与 `runs` 表**无关**：流水是独立事实，不随会话读模型重建。
type LLMCallEntity struct {
	// ID 平台铸号 `call_<26 位 ULID>`（objid.Call）。
	ID string `gorm:"column:id;primaryKey"`
	// At 调用发生时刻（RFC3339 文本，与全仓时间列同一 serializer）。
	At time.Time `gorm:"column:at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// Purpose 用途标签（agent_turn / summary / judge / embed / rewrite / title / extract…）。
	Purpose string `gorm:"column:purpose;not null"`
	// SessionID 关联平台会话 id（旁路调用为空串）。
	SessionID string `gorm:"column:session_id;not null"`
	// RunID 关联轮次 id（旁路调用为空串）。
	RunID string `gorm:"column:run_id;not null"`
	// Provider / Model 实际使用的供应商与模型（空串 = 未指定/未上报）。
	Provider string `gorm:"column:provider;not null"`
	Model    string `gorm:"column:model;not null"`
	// ——— token 明细（累加型；未上报 → 全 0）———
	InputTokens       int64 `gorm:"column:input_tokens;not null"`
	OutputTokens      int64 `gorm:"column:output_tokens;not null"`
	CachedReadTokens  int64 `gorm:"column:cached_read_tokens;not null"`
	CachedWriteTokens int64 `gorm:"column:cached_write_tokens;not null"`
	ThoughtTokens     int64 `gorm:"column:thought_tokens;not null"`
	TotalTokens       int64 `gorm:"column:total_tokens;not null"`
	// CostAmount/CostCurrency 本次费用（可选；零值 = 未上报）。多币种不混加由聚合侧处理。
	CostAmount   float64 `gorm:"column:cost_amount;not null"`
	CostCurrency string  `gorm:"column:cost_currency;not null"`
	// LatencyMS 本次调用耗时（毫秒）。
	LatencyMS int64 `gorm:"column:latency_ms;not null"`
	// Status 结果状态：ok / error / timeout / cancelled（llm.Status* 常量）。
	Status string `gorm:"column:status;not null"`
}

// TableName 实现 gormdao.Entity。
func (LLMCallEntity) TableName() string { return TableNameLLMCall }

// LLMCallEntityList 是 LLMCallEntity 切片类型（gormdao 泛型参数要求具名切片）。
type LLMCallEntityList []LLMCallEntity
