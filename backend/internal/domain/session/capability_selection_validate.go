package session

import (
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/platform/code"
)

// ——— M3-3 建会话时的能力选定校验 ———
//
// 纪律：**不可达就报错，绝不静默丢弃**（与 MCP 注入面同源）。静默丢弃的代价是用户以为"选了就
// 生效了"——这正是本方案要消灭的那个静默失败面。
//
// **一个码，原因逐条列**：全部落 `capability_not_applicable`(40001)。设计上刻意不再细分成
// "404 找不到 / 400 参数错"——对用户而言这两件事的下一步动作是同一个（回能力清单看看那个 id
// 现在是什么状态），而分成两个码只会让前端多一条分支、让文案在两处漂移。id 到底是不存在还是
// 不可达，由**原因文本**如实区分（它是给人和给日志看的，不是判据）。
//
// 判定用的是 Library 的同一批函数（`SkillVerdict`/`McpVerdict`），所以"面板上显示不可达"与
// "建会话被拒"永远同向。
//
// **刻意不校验密钥引用能不能解析**：那是**环境事实**（`store:` 那条在库里没有、`env:` 变量没设），
// 不是这次请求的错，用户可能稍后就补上；且它会让"钉住的代际"随环境抖动。注入期已逐条 WARN 跳过，
// Library 的 warnings 里也会呈现。
//
// P3c：从 `internal/manager/capability_selection_validate.go` 搬进本域——它判的是"会话钉住的
// 这份选定在当前定义下够不够得着"，归属会话聚合；判决本体（`capability_verdict.go`）也在本域。

// SelectionProblem 一条不可达/不存在的具体原因（错误文案与日志共用同一份措辞）。
type SelectionProblem struct {
	Kind   string // skill | mcp
	ID     string
	Reason string
}

func (p SelectionProblem) String() string {
	return fmt.Sprintf("%s %q：%s", p.Kind, p.ID, p.Reason)
}

// CapabilityLookup 装配侧登记的"能力定义查得到吗"（含定义本体）。
//
// 我**不做**成 `*capability.Registry`：注册表的读面比这里需要的多得多（写面、列举、工具表…），
// 把它交给域等于让域顺手长出第二条写路径。一个窄接口 + 两个方法就够了。
type CapabilityLookup interface {
	// LookupSkill 按 id 取 skill 定义（ok=false = 注册表里没有）。
	LookupSkill(id string) (capability.Skill, bool)
	// LookupMcp 按 id 取 MCP server 定义（ok=false = 注册表里没有）。
	LookupMcp(id string) (capability.McpServer, bool)
	// SkillVerdictFacts 判该 runtime 上 skill 可达性所需的事实。
	SkillVerdictFacts(runtimeID string) SkillVerdictFacts
	// McpVerdictFacts 判该 runtime 上 MCP 可达性所需的事实。
	McpVerdictFacts(runtimeID string) McpVerdictFacts
}

// ValidateSelection 校验选定对该 runtime 可达（nil = 未声明 → 直接通过）。
//
// 必须**在建连之前**调用：校验不过就一个副作用都不该有（不建会话、不拉进程、不写库）。
func (pl *Plane) ValidateSelection(sel *CapabilitySelection, runtimeID string) error {
	if sel == nil {
		return nil
	}
	if pl.capLookup == nil {
		// 没登记查表面（纯离线场景）→ 不做校验，与"未声明"同义。**不静默放过已知问题**：
		// 装配期一定会登记它（`NewService`），这条分支只服务单测与离线命令。
		return nil
	}
	// 先规范化（去空串/去重/升序）：空白 id 与"未声明"同义，不该报成"不存在"。
	n := &CapabilitySelection{Skills: NormalizeIDs(sel.Skills), Mcps: NormalizeIDs(sel.Mcps)}
	unknown, unreachable := pl.selectionProblems(n, runtimeID)
	if len(unknown) == 0 && len(unreachable) == 0 {
		return nil
	}
	// 不存在（拼错）排前面：它更可能是用户立刻要改的那个。
	problems := append(append([]SelectionProblem(nil), unknown...), unreachable...)
	return code.NewError(code.ClassRequest, "capability_not_applicable", fmt.Sprintf("能力选择对 runtime %q 不可达：%s", runtimeID, problemList(problems)), nil)
}

// SelectionProblemsOf 把选定拆成「不存在」与「不可达」两组（顺序：按 id 升序，便于比对与去抖动）。
//
// 导出：诊断面（`mate doctor`）与单测都要按同一口径分组。
func (pl *Plane) SelectionProblemsOf(sel *CapabilitySelection, runtimeID string) (unknown, unreachable []SelectionProblem) {
	if sel == nil || pl.capLookup == nil {
		return nil, nil
	}
	return pl.selectionProblems(sel, runtimeID)
}

func (pl *Plane) selectionProblems(sel *CapabilitySelection, runtimeID string) (unknown, unreachable []SelectionProblem) {
	lookup := pl.capLookup
	if sel.Skills != nil {
		for _, id := range *sel.Skills {
			sk, ok := lookup.LookupSkill(id)
			if !ok {
				unknown = append(unknown, SelectionProblem{Kind: "skill", ID: id, Reason: "注册表里没有这个 skill（检查 GET /library?kind=skill）"})
				continue
			}
			if v := SkillVerdict(sk, lookup.SkillVerdictFacts(runtimeID)); !v.OK {
				unreachable = append(unreachable, SelectionProblem{Kind: "skill", ID: id, Reason: v.Reason})
			}
		}
	}
	if sel.Mcps != nil {
		for _, id := range *sel.Mcps {
			m, ok := lookup.LookupMcp(id)
			if !ok {
				unknown = append(unknown, SelectionProblem{Kind: "mcp", ID: id, Reason: "注册表里没有这个 MCP server（检查 GET /library?kind=mcp）"})
				continue
			}
			if v := McpVerdict(m, lookup.McpVerdictFacts(runtimeID)); !v.OK {
				unreachable = append(unreachable, SelectionProblem{Kind: "mcp", ID: id, Reason: v.Reason})
			}
		}
	}
	byID := func(ps []SelectionProblem) {
		sort.Slice(ps, func(i, j int) bool {
			if ps[i].Kind != ps[j].Kind {
				return ps[i].Kind < ps[j].Kind
			}
			return ps[i].ID < ps[j].ID
		})
	}
	byID(unknown)
	byID(unreachable)
	return unknown, unreachable
}

// problemList 逐条列出（用 `；` 分隔，避免与原因文案里的逗号混淆）。
func problemList(ps []SelectionProblem) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, "；")
}
