package dtoruntime

// RuntimeResp Runtime 的对外表示。
type RuntimeResp struct {
	Name string         `json:"name"`
	ID   string         `json:"id"`
	Vers string         `json:"version"`
	Caps []CapabilityKV `json:"capabilities"`
}

// CapabilityKV 单个能力基线（静态声明）的 key/value 表示。
type CapabilityKV struct {
	Key   string `json:"key"`
	Value bool   `json:"value"`
}
