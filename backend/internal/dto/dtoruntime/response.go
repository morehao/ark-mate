package dtoruntime

import "github.com/morehao/ark-mate/pkg/runtime"

// RuntimeResp Runtime 的对外表示。
type RuntimeResp struct {
	Name string         `json:"name"`
	ID   string         `json:"id"`
	Vers string         `json:"version"`
	Caps []CapabilityKV `json:"capabilities"`
	// ProviderProjection 该 runtime 认的**运行时参数** schema（H1/H3）。
	//
	// 它是前端那张「每个底座的运行时参数」表单的**唯一数据源**：加一个底座不需要改前端。
	// 空 = 该 runtime 不承载平台模型清单（未声明 ConfigProjection），对它写运行时参数没有意义。
	ProviderProjection *runtime.ProviderProjectionSchema `json:"provider_projection,omitempty"`
}

// CapabilityKV 单个能力基线（静态声明）的 key/value 表示。
type CapabilityKV struct {
	Key   string `json:"key"`
	Value bool   `json:"value"`
}
