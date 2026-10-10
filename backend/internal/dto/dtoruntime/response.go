package dtoruntime

// RuntimeResp Runtime 的对外表示。
//
// **v12 删掉了 `provider_projection`**：那是一张"每个底座认哪些运行时参数"的 schema，前端据此
// 渲染逐底座的投影表单——而逐底座投影本身就是被删掉的东西（ADR-7）。运行时参数不再暴露给用户，
// 协议的差异由各 adapter 自己知道（ADR-11）。
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
