package manager

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// ——— M3 能力选定（会话级子集） ———
//
// 语义只有三态，且**逐 kind 独立**（与 AgentRec.Mcps 同构）：
//
//	nil           该 kind 未声明 → 回落上一层（MCP 走 agent.Mcps 白名单；skill 走 runtimes 声明）
//	非 nil 空数组  **这一类一个都不要**（不是"全部"——那样用户就没法表达"这个会话不要任何 MCP"）
//	非 nil 非空    白名单
//
// 「整个 capabilities 未传」与「传了 {}」是两件事，由 `Selection` 指针本身区分：
// `nil` = 未传（缺省路径，**行为与引入代际之前逐字相同**）；非 nil = 声明了（哪怕字段全空）。
// 这个区分是刻意的：代际绑定是"用户钉住了一套能力"，而"什么都没填"是一个合法的钉子
// （代价只是可能多一个内容相同但指纹不同的代际，属于可见的浪费，不是静默的错误行为）。
type CapabilitySelection struct {
	// Skills 选定的 skill id 白名单（nil = 未声明 → 按 skill 自己的 runtimes 声明投放）。
	Skills *[]string `json:"skills,omitempty"`
	// Mcps 选定的 MCP server id 白名单（nil = 未声明 → 回落 agent.Mcps）。
	Mcps *[]string `json:"mcps,omitempty"`
}

// NewCapabilitySelection 构造一个"声明了但什么都不限制"的选定（= `capabilities: {}`）。
func NewCapabilitySelection() *CapabilitySelection { return &CapabilitySelection{} }

// Empty 该选定是否什么都没限制（两个 kind 都未声明）。
func (c *CapabilitySelection) Empty() bool {
	return c == nil || (c.Skills == nil && c.Mcps == nil)
}

// normalized 返回**规范化副本**：去空串、去重、升序。
//
// 必须规范化的理由是它进指纹：`["a","b"]` 与 `["b","a"]` 是同一套能力，若不去重排序，
// 用户换个勾选顺序就会白换一次代际（新建一个进程）。规范化只影响存储与指纹，不影响语义。
func (c *CapabilitySelection) normalized() *CapabilitySelection {
	if c == nil {
		return nil
	}
	return &CapabilitySelection{
		Skills: normalizeIDs(c.Skills),
		Mcps:   normalizeIDs(c.Mcps),
	}
}

// normalizeIDs 去空串、去重、升序；nil 保持 nil（"未声明"不能被规范化成"空集"）。
func normalizeIDs(in *[]string) *[]string {
	if in == nil {
		return nil
	}
	seen := make(map[string]bool, len(*in))
	out := make([]string, 0, len(*in))
	for _, id := range *in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return &out
}

// MarshalCapabilitySelection 序列化成 `capability_json` 列的形态（空选定 → 空串）。
//
// 空串 = 未声明：与 v7 迁移给旧数据填的默认值同形，所以"旧会话"天然落到缺省路径。
func MarshalCapabilitySelection(c *CapabilitySelection) string {
	n := c.normalized()
	if n == nil {
		return ""
	}
	b, err := json.Marshal(n)
	if err != nil {
		// *[]string 的 JSON 编码不会失败；真失败也只可能是程序错误，落空串（= 未声明）最安全。
		return ""
	}
	return string(b)
}

// ParseCapabilitySelection 从 `capability_json` 列读回选定（空串/非法 → nil = 未声明）。
//
// 非法 JSON 按"未声明"处理而不是报错：这一列由平台自己写，读到坏值说明库被外部改过，
// 此时让会话按缺省能力集照常可用（并留下日志）比让会话打不开更有价值——能力选择不是安全边界，
// 它决定的是"注入哪些 MCP / 投放哪些技能"。
func ParseCapabilitySelection(raw string) *CapabilitySelection {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var sel CapabilitySelection
	if err := json.Unmarshal([]byte(raw), &sel); err != nil {
		// 不静默：这一列由平台自己写，读到坏值说明库被外部改过，日志里必须留痕。
		slog.Warn("会话能力选定读取失败（按未声明处理，回落缺省能力集）", "raw", raw, "err", err)
		return nil
	}
	if sel.Empty() {
		// `{}` 是合法的"声明了但什么都不限制"，序列化后同样是 `{}`（非空串），故这里保持非 nil。
		return &sel
	}
	return &sel
}

// String 只用于日志/事件载荷（不含任何秘密：这里只有 id 列表）。
func (c *CapabilitySelection) String() string {
	if c == nil {
		return "(未声明能力选择)"
	}
	return fmt.Sprintf("skills=%v mcps=%v", derefIDs(c.Skills), derefIDs(c.Mcps))
}

// derefIDs nil 安全的取值（日志用；nil 显示为 "<未声明>"，空数组显示为 "[]"）。
func derefIDs(in *[]string) string {
	if in == nil {
		return "<未声明>"
	}
	return fmt.Sprintf("%v", *in)
}

// CapabilityIDs 取某 kind 的选定 id 集（nil 安全；供 Library/输入卡回显）。
func (c *CapabilitySelection) SkillIDs() []string { return derefList(c.Skills) }

// McpIDs 同 SkillIDs（MCP 侧）。
func (c *CapabilitySelection) McpIDs() []string { return derefList(c.Mcps) }

func derefList(in *[]string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(*in))
	copy(out, *in)
	return out
}
