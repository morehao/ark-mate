// Package objusage 承载会话用量的共享值对象。
//
// 为什么单独成包：这个形状同时被三层使用——协议层（internal/infra/protocol/acp 从 ACP
// session/usage_update 归一化出来）、持久层（internal/persistence/store 解析 runs.usage_json
// 并做聚合）、传输层（internal/dto/dtosession 直接作为响应载荷）。
// 放在其中任何一层都会让另外两层反向依赖该层；放在 internal/core/object 下则三方都只依赖值对象。
package objusage

// UsageInfo 会话用量快照（ACP session/usage_update，M3 用量统计）：
// Used/Size 为「当前上下文窗口占用/窗口大小」（快照语义，同一会话以最后一次为准）；
// Cost 为该会话累计费用（可选，底座不上报时为零值）。
type UsageInfo struct {
	Used         int64   `json:"used"`
	Size         int64   `json:"size"`
	CostAmount   float64 `json:"cost_amount,omitempty"`
	CostCurrency string  `json:"cost_currency,omitempty"`
}

// HasContext 是否携带可用的上下文占用快照（Used/Size 至少一项为正）。
// 压缩后 Used=0 而 Size 仍为正属正常上报；两者全零视为未上报上下文。
func (u UsageInfo) HasContext() bool { return u.Used > 0 || u.Size > 0 }

// HasCost 是否携带可用的费用（正金额）。底座上报「0 元」与「未上报」在 wire 上不可区分，
// 一律按未上报处理（不臆造零成本）。
func (u UsageInfo) HasCost() bool { return u.CostAmount > 0 }

// Reported 是否携带任一可用字段（上下文或费用）。
// 上下文与费用的上报率不同（实测底座多只报上下文），统计时分别用 HasContext/HasCost 计数，
// 避免「只报了费用」的会话被算进上下文上报数。
func (u UsageInfo) Reported() bool { return u.HasContext() || u.HasCost() }
