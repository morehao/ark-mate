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
	// Apps 会话启用的 app id 白名单（三态与 Skills/Mcps 同构：nil=未声明→无 app 工具、
	// `[]`=明确不要任何 app 工具、非空=只启用这些 app 的业务工具面）。
	//
	// 与 Skills/Mcps/Expert 不同，Apps 是**唯一来源**的 app 工具面：会话没声明（nil）就**零 app 工具**
	// （设计 §5.5 表"app 工具（新增）"行；平台注入恒化、业务工具必须独立注入且默认零）。它也是
	// **越权红线**的守点——app 只能缩小/提供默认，不能借 Apps 把会话能力扩张到 session 白名单之外。
	//
	// **进代际指纹**（与 Skills/Mcps 同侧，不进指纹的反例是 Expert）：app 工具面是逐会话独立注入的
	// stdio server（`mate mcp app --apps=a,b`，见 sessionruntime `appendAppMcpServers`），它直接改变
	// 进程的 mcpServers⇒"这套能力是否需要另一个进程/是否多了工具"确实变了，必须换代际。
	// 复用平台内置 server 的同一判据（`platform_mcp.go` 注释③：注入面与指纹共用同一份 plan.Direct）。
	Apps *[]string `json:"apps,omitempty"`
}

// NewCapabilitySelection 构造一个"声明了但什么都不限制"的选定（= `capabilities: {}`）。
func NewCapabilitySelection() *CapabilitySelection { return &CapabilitySelection{} }

// Empty 该选定是否什么都没限制（四个 kind 都未声明）。
func (c *CapabilitySelection) Empty() bool {
	return c == nil || (c.Skills == nil && c.Mcps == nil && c.Expert == nil && c.Apps == nil)
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
		Apps:   NormalizeIDs(c.Apps),
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
	return fmt.Sprintf("skills=%v mcps=%v expert=%s apps=%v", DerefIDs(c.Skills), DerefIDs(c.Mcps), DerefExpert(c.Expert), DerefIDs(c.Apps))
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

// SkillIDs 取 skill 选定的 id 集（**nil 接收者安全**；供 Library/输入卡回显）。
//
// 注意 `c` 本身可能为 nil：会话没声明 `capabilities` 时 `opt.Selection` 就是 nil，注入链会拿 nil
// 调这些取值器（`capability_selection.go` 曾因此 nil deref panic——真起 serve + 真模型建无 capabilities
// 的会话暴露的 bug）。故 receiver 也要判 nil，而不只是字段 nil。
func (c *CapabilitySelection) SkillIDs() []string {
	if c == nil {
		return nil
	}
	return DerefList(c.Skills)
}

// McpIDs 同 SkillIDs（MCP 侧；nil 接收者安全）。
func (c *CapabilitySelection) McpIDs() []string {
	if c == nil {
		return nil
	}
	return DerefList(c.Mcps)
}

// AppIDs 取启用的 app id 集（nil 接收者安全；nil=未声明→nil，`[]`=明确不要→空切片）。
//
// sessionruntime 的 app 工具注入通道用它读会话 app scope（§5.5）：nil/空都表示"无 app 工具"，
// 但两者措辞不同，注入侧对二者**都**必须零注入（默认零是硬要求）。
func (c *CapabilitySelection) AppIDs() []string {
	if c == nil {
		return nil
	}
	return DerefList(c.Apps)
}

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

// ——— M3-8：会话内改能力的三态补丁 ———
//
// 补丁的形状必须是三态：**键缺失 = 不改**、`null` = 置回「未声明」、值 = 置为该值。
// 只用 `*[]string` 表达不了第一条与第二条的区别（两者都反序列化成 nil），所以每层都多一个
// `Set` 布尔：wire 侧由 `json.RawMessage` 区分「键在不在」，域侧由 `Set` 区分「要不要动」。
//
// 为什么不干脆做「整体替换」（像 PUT）：前端只改了技能那一格时，整体替换会把另外两格一起
// 写一遍——那意味着"只换技能"也可能顺手把专家清掉，而用户什么都没点。

// OptionalIDs 一次「可缺席的 id 列表」赋值：Set=false → 不改；Set=true → 置为 Value（nil = 未声明）。
type OptionalIDs struct {
	Set   bool
	Value *[]string
}

// OptionalString 同 OptionalIDs，元素是单个字符串（专家 id：nil = 未声明、"" = 明确不用）。
type OptionalString struct {
	Set   bool
	Value *string
}

// CapabilityPatch 会话内改能力的补丁（逐 kind 独立，语义见 OptionalIDs）。
type CapabilityPatch struct {
	Skills OptionalIDs
	Mcps   OptionalIDs
	Expert OptionalString
	Apps   OptionalIDs
}

// ApplyCapabilityPatch 把补丁合并到当前选定上，返回**新对象**（不改入参，current 可为 nil）。
//
// 结果一律规范化（去空串/去重/升序、专家 id 去空白）：与创建路径同一条规则，
// 否则"同一个选定换个写法"会算出不同指纹，白起一个进程。
func ApplyCapabilityPatch(current *CapabilitySelection, p CapabilityPatch) *CapabilitySelection {
	out := &CapabilitySelection{}
	if current != nil {
		out.Skills, out.Mcps, out.Expert, out.Apps = current.Skills, current.Mcps, current.Expert, current.Apps
	}
	if p.Skills.Set {
		out.Skills = cloneIDs(p.Skills.Value)
	}
	if p.Mcps.Set {
		out.Mcps = cloneIDs(p.Mcps.Value)
	}
	if p.Expert.Set {
		out.Expert = cloneString(p.Expert.Value)
	}
	if p.Apps.Set {
		out.Apps = cloneIDs(p.Apps.Value)
	}
	return out.normalized()
}

// EquivalentCapabilitySelection 两份选定对**用户**是不是同一件事（规范化后逐字节比较）。
//
// nil（未声明）与 `{}`（声明了但不限制）在这里等价：两者都回落默认投放，用户看到的能力集一样。
// 它只用于回答「这次编辑要不要写库」——**不要**拿它判代际：空串与 `{}` 的指纹不同
// （前者为空、后者非空），代际要的是指纹比较（见 `capability_fingerprint.go`）。
func EquivalentCapabilitySelection(a, b *CapabilitySelection) bool {
	if a.Empty() && b.Empty() {
		return true
	}
	return MarshalCapabilitySelection(a) == MarshalCapabilitySelection(b)
}

// cloneIDs 深拷贝一份 id 列表（nil 保持 nil；**空切片保持空切片**）。
//
// 不能用 `append([]string(nil), (*in)...)`：空切片会被它变成 nil，于是"明确一个都不要"
// 在合并后悄悄变成"未声明"——那正是三态里最不该被抹平的一格。
func cloneIDs(in *[]string) *[]string {
	if in == nil {
		return nil
	}
	out := make([]string, len(*in))
	copy(out, *in)
	return &out
}

// cloneString 深拷贝一个可空字符串（nil 保持 nil）。
func cloneString(in *string) *string {
	if in == nil {
		return nil
	}
	v := *in
	return &v
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
	var skills, mcps, apps *[]string
	if sel.Skills != nil {
		v := DerefList(sel.Skills)
		skills = &v
	}
	if sel.Mcps != nil {
		v := DerefList(sel.Mcps)
		mcps = &v
	}
	if sel.Apps != nil {
		v := DerefList(sel.Apps)
		apps = &v
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
	return &SessionCapabilities{Skills: skills, Mcps: mcps, Apps: apps, Expert: expert, ExpertMissing: expertMissing, Fingerprint: rec.Fingerprint}
}
