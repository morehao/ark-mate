package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/golib/glog"
)

// ——— M3 能力选定（会话级子集） ———
//
// 语义只有三态，且**逐 kind 独立**：
//
//	nil           该 kind 未声明 → 回落上一层（skill 走它自己的 runtimes 声明）
//	非 nil 空数组  **这一类一个都不要**（不是"全部"——那样用户就没法表达"这个会话不要任何 MCP"）
//	非 nil 非空    白名单
//
// 「整个 capabilities 未传」与「传了 {}」是两件事，由 `Selection` 指针本身区分：
// `nil` = 未传（缺省路径，**行为与引入代际之前逐字相同**）；非 nil = 声明了（哪怕字段全空）。
// 这个区分是刻意的：代际绑定是"用户钉住了一套能力"，而"什么都没填"是一个合法的钉子
// （代价只是可能多一个内容相同但指纹不同的代际，属于可见的浪费，不是静默的错误行为）。
//
// P3c：随 `capability_fingerprint.go` 从 `internal/manager` 搬进会话域——它是**会话钉住的
// 那一份选定**（落 `sessions.capability_json` 列、进代际指纹），归属会话这个聚合。
type CapabilitySelection struct {
	// Skills 选定的 skill id 白名单（nil = 未声明 → 按 skill 自己的 runtimes 声明投放）。
	Skills *[]string `json:"skills,omitempty"`
	// Mcps 选定的 MCP server id 白名单（nil = 未声明 → 全量）。
	Mcps *[]string `json:"mcps,omitempty"`
	// Expert 会话级专家（0..1）：nil = 不选专家（只有整个 capabilities 缺省时，工作空间默认才可能
	// 带入专家）；`*""` = 明确不用专家；非空 = 专家 id。与 Skills/Mcps 三态同构。
	//
	// 专家 id 与 persona **不进指纹**（指纹职责是"这套能力是否需要另一个进程"；专家只影响默认值，
	// P1 的 instructions 是首轮提示词文本，不改变进程能力）。因此改专家名字/描述不换代际。
	Expert *string `json:"expert,omitempty"`
}

// NewCapabilitySelection 构造一个"声明了但什么都不限制"的选定（= `capabilities: {}`）。
func NewCapabilitySelection() *CapabilitySelection { return &CapabilitySelection{} }

// Empty 该选定是否什么都没限制（三个 kind 都未声明）。
func (c *CapabilitySelection) Empty() bool {
	return c == nil || (c.Skills == nil && c.Mcps == nil && c.Expert == nil)
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
		Skills: NormalizeIDs(c.Skills),
		Mcps:   NormalizeIDs(c.Mcps),
		Expert: normalizeExpertID(c.Expert),
	}
}

// normalizeExpertID 专家 id 只做空白归一（""=明确不用，非空=id）；nil 保持 nil（未声明）。
func normalizeExpertID(in *string) *string {
	if in == nil {
		return nil
	}
	v := strings.TrimSpace(*in)
	return &v
}

// NormalizeIDs 去空串、去重、升序；nil 保持 nil（"未声明"不能被规范化成"空集"）。
//
// 导出：建会话的请求面（`manager`）也要按同一条规则规范化用户传进来的 id 列表。
func NormalizeIDs(in *[]string) *[]string {
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
		glog.Warnw(context.Background(), "会话能力选定读取失败（按未声明处理，回落缺省能力集）", "raw", raw, "err", err)
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
	return fmt.Sprintf("skills=%v mcps=%v expert=%s", DerefIDs(c.Skills), DerefIDs(c.Mcps), DerefExpert(c.Expert))
}

// DerefExpert nil 安全的专家 id 回显（nil = "<未声明>"）。
func DerefExpert(in *string) string {
	if in == nil {
		return "<未声明>"
	}
	return *in
}

// DerefIDs nil 安全的取值（日志用；nil 显示为 "<未声明>"，空数组显示为 "[]"）。
func DerefIDs(in *[]string) string {
	if in == nil {
		return "<未声明>"
	}
	return fmt.Sprintf("%v", *in)
}

// SkillIDs 取 skill 选定的 id 集（nil 安全；供 Library/输入卡回显）。
func (c *CapabilitySelection) SkillIDs() []string { return DerefList(c.Skills) }

// McpIDs 同 SkillIDs（MCP 侧）。
func (c *CapabilitySelection) McpIDs() []string { return DerefList(c.Mcps) }

// DerefList 把 `*[]string` 解成**副本**切片（nil → nil）。
//
// 导出：读面回显与请求面归一都走它（两份实现会在"nil 还是空切片"这条语义上分叉——那正是
// `omitempty` 与三态语义的分界）。
func DerefList(in *[]string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(*in))
	copy(out, *in)
	return out
}

// SessionCapabilitiesRespOf 把会话钉住的选定折叠成对外形态（nil = 未声明 → 缺省路径）。
//
// 判据是**记录本身**（CapabilityJSON 空 = 未声明），不是"指纹是否为空"：
// 两者今天同向（未声明 ⇔ 空指纹），但记录才是事实源，回显不该依赖一个派生量。
//
// P3c：随选定值对象进域——它读的是本域的记录字段，此前靠 `capabilityViewOf` 端口在
// manager 侧实现（那个端口因此删掉）。
func (pl *Plane) SessionCapabilitiesRespOf(rec *SessionRec) *SessionCapabilities {
	if rec == nil || strings.TrimSpace(rec.CapabilityJSON) == "" {
		return nil
	}
	sel := ParseCapabilitySelection(rec.CapabilityJSON)
	if sel == nil {
		return nil
	}
	// 逐 kind 原样回显：**缺键 = 未声明该 kind**（回落上一层），`[]` = 明确一个都不要。
	// 不做"未声明就补空数组"的归一——那会把两个语义相反的状态画成同一个样子，
	// 用户看到 `skills: []` 会以为"这个会话没有技能"，实际是"投给它的全部技能"。
	var skills, mcps *[]string
	if sel.Skills != nil {
		v := DerefList(sel.Skills)
		skills = &v
	}
	if sel.Mcps != nil {
		v := DerefList(sel.Mcps)
		mcps = &v
	}
	var expert *string
	expertMissing := false
	if sel.Expert != nil {
		v := *sel.Expert
		expert = &v
		// 读面判定定义是否已删除（空串 = 明确不用，不查；未登记查表 = 不做判定）。
		if v != "" && pl.capLookup != nil {
			if _, ok := pl.capLookup.LookupExpert(v); !ok {
				expertMissing = true
			}
		}
	}
	return &SessionCapabilities{Skills: skills, Mcps: mcps, Expert: expert, ExpertMissing: expertMissing, Fingerprint: rec.Fingerprint}
}
