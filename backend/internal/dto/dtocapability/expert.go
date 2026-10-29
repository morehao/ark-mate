package dtocapability

import "encoding/json"

// ——— 专家定义面（P0）———
//
// 契约见设计 `.dsh/docs/specs/2026-10-29-session-capability-selection-design.md` 的「专家定义面」。

// ExpertWriteReq 新建/覆盖一个专家定义（POST /experts、PUT /experts/:id）。
//
// 与 skill/mcp 写面同形：**原文 JSON 对象**，键的出现与否有语义（未出现的键保留原值，`null` 删键）。
// 专家 body 的已知键：id / name / description / avatar / model / skills / mcps / enabled / tags /
// instructions；未知键原样保留。
type ExpertWriteReq map[string]json.RawMessage

// ExpertToggleReq 启停一个专家（PATCH /experts/:id）。只认 enabled 一个开关。
type ExpertToggleReq struct {
	Enabled bool `json:"enabled"`
}

// ExpertResp 专家读面的一条。
//
// 列表（GET /experts）**不返回 instructions 正文**（避免列表膨胀），只用 `has_instructions` 标记
// 「有角色设定」；详情（GET /experts/:id）返回 `instructions` 全文。
type ExpertResp struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Avatar      string   `json:"avatar,omitempty"`
	Model       string   `json:"model,omitempty"`
	Skills      []string `json:"skills,omitempty"`
	Mcps        []string `json:"mcps,omitempty"`
	Enabled     bool     `json:"enabled"`
	Tags        []string `json:"tags,omitempty"`
	// HasInstructions 该专家是否带角色设定（列表用它标记，不返回正文）。
	HasInstructions bool `json:"has_instructions"`
	// Instructions 角色设定全文（仅详情返回；列表为空 → omitempty 不出现在 JSON 里）。
	Instructions string `json:"instructions,omitempty"`
}

// ExpertWriteResp 新建/覆盖专家定义的结果。
type ExpertWriteResp struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// Created true = 本次新建；false = 覆盖既有定义。
	Created bool `json:"created"`
}

// ExpertDeleteResp 删除专家定义的结果（不存在 → deleted:false 且无错：幂等）。
type ExpertDeleteResp struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
	Path    string `json:"path,omitempty"`
}

// ExpertToggleResp 启停专家定义的结果。
type ExpertToggleResp struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}
