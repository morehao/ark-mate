package session

import (
	"fmt"
	"github.com/morehao/ark-mate/internal/object/objdelivery"
	"strings"

	"github.com/morehao/ark-mate/internal/capability"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 能力可达性判决：**唯一实现** ———
//
// 这份判决同时服务两件事，两处各写一遍必然漂移，而漂移的表现恰恰是最难自证的一种：
//
//	Library 读面（`GET /library` 的逐 runtime applicability）：告诉用户"这条能力投得过去吗"；
//	建会话的选定校验（M3-3）：用户钉住的能力**不可达就报错，绝不静默丢弃**（静默丢弃的代价是
//	  用户以为"选了就生效了"——那正是本方案要消灭的失败面）。
//
// 漂移的方向有两种，都不可接受：「Library 说不可达、建会话却放行」（用户以为钉住了，
// 实际没投）与「Library 说可达、建会话被拒」（用户没法理解为什么）。
//
// **状态集与文案是同一份**（`projected`/`untested`/`not_configured`/`not_applicable`/`disabled`）：
// 与物化体检（§13.4 S2）共用同一套词，两处各造一套词就会让用户对着两个页面猜哪个是真的。
//
// P3c：从 `manager/library.go` 的 `skillRuntimeVerdict` / `mcpRuntimeVerdict` 搬进本域
// （判据与文案**逐字未改**），manager 的 Library 读面改经同一份实现——它此前是本包的
// 私有函数、只有 Library 与校验两处调用，搬进来正好给"唯一实现"这条纪律一个物理位置。

// SkillVerdictFacts 判一条 skill 在某个 runtime 上可达性所需的**事实**（不含判断）。
type SkillVerdictFacts struct {
	// Runtime 目标 runtime。
	Runtime string
	// Projection 该 runtime 的 skill 投影事实。
	Projection ProjectedSkill
	// ProjectionOK 该 runtime 有没有解析出 skill 投影。
	ProjectionOK bool
}

// McpVerdictFacts 判一条 MCP server 在某个 runtime 上可达性所需的**事实**。
type McpVerdictFacts struct {
	// Runtime 目标 runtime。
	Runtime string
	// Supported 该 runtime 承载的 MCP 传输。
	//
	// **空列表 = 什么都不承载**（未知 runtime 也走这条），所以装配侧不许在这里做"空 → stdio"
	// 的默认化：那条约定属于 `supportedMcpTransports`（它区分"声明为空 → 只收 stdio"与
	// "runtime 不在册 → 空"）。在这里再默认一次会把未知 runtime 静默变成"只收 stdio"，
	// 于是"未注册 runtime 应不可达"这条判据失效。
	Supported []string
	// SupportedKnown 该 runtime 是否在册（`spec.McpTransports` 空 → 默认只收 stdio）。
	SupportedKnown bool
	// GatewayEnabled 平台侧 MCP 网关开关（开着时远程 server 对每个已注册 runtime 都可达）。
	GatewayEnabled bool
}

// SkillVerdictResult 一条 (skill, runtime) 的判决结果。
type SkillVerdictResult struct {
	OK     bool
	Status string
	Reason string
}

// McpVerdictResult 一条 (mcp, runtime) 的判决结果。
type McpVerdictResult struct {
	OK     bool
	Status string
	Reason string
}

// SkillVerdict 单个 (skill, 投影) 的可达性判定——**唯一实现**。
//
// 判据顺序（先判"平台这边有没有关掉"，再判"落点解析出来没有"，最后才判"实测过没有"）：
// 关掉是用户的显式决定，最先说；未解析是配置/声明问题；untested 只是"我们还没实测"，
// 会照常投放——所以它必须排在 projected 之前但排在 not_applicable 之后。
func SkillVerdict(sk capability.Skill, facts SkillVerdictFacts) SkillVerdictResult {
	rt := facts.Runtime
	if rt == "" {
		rt = facts.Projection.Runtime
	}
	p := facts.Projection
	switch {
	case !sk.IsEnabled():
		return SkillVerdictResult{Status: objdelivery.StatusDisabled,
			Reason: "该 skill 已被禁用（enabled:false）：改回 enabled:true 即可恢复"}
	case !sk.AppliesToRuntime(rt):
		return SkillVerdictResult{Status: objdelivery.StatusNotApplicable,
			Reason: fmt.Sprintf("该 skill 的 runtimes 白名单没有 %q（要投给它就把 %q 加进 runtimes，或清空 runtimes 表示全部）", rt, rt)}
	case !facts.ProjectionOK:
		return SkillVerdictResult{Status: objdelivery.StatusNotConfigured,
			Reason: fmt.Sprintf("%s 没有解析出 skill 投影（该 runtime 上技能无处安放）", rt)}
	case p.Err != "":
		return SkillVerdictResult{Status: objdelivery.StatusNotConfigured,
			Reason: fmt.Sprintf("%s 声明了 skill 投影但本进程没解析出落点：%s（检查 MATE_PROJECTION_DIRS 是否配了 %s:skill）", rt, p.Err, rt)}
	case !p.Projection.Tested:
		return SkillVerdictResult{OK: true, Status: objdelivery.StatusUntested,
			Reason: fmt.Sprintf("%s 的投影声明尚未经实测：会照常投放到 %s，但不担保底座开箱读到它", rt, p.Projection.Root)}
	default:
		return SkillVerdictResult{OK: true, Status: objdelivery.StatusProjected,
			Reason: fmt.Sprintf("投放到 %s（%s）", p.Projection.Root, p.Projection.Layout)}
	}
}

// McpVerdict 单个 (server, runtime) 的可达性判定——**唯一实现**（理由同 SkillVerdict）。
func McpVerdict(m capability.McpServer, facts McpVerdictFacts) McpVerdictResult {
	rt := facts.Runtime
	carried := false
	for _, t := range facts.Supported {
		if t == m.Transport {
			carried = true
			break
		}
	}
	switch {
	case !m.IsEnabled():
		return McpVerdictResult{Status: objdelivery.StatusDisabled, Reason: "该 server 已被禁用（enabled:false）"}
	case facts.GatewayEnabled && capability.IsRemoteTransport(m.Transport):
		// M7-3：远程 server 对每个已注册 runtime 都可达——底座拿到的是**一条 stdio 条目**，
		// 真正的 sse/http 由平台网关代连。故这里不再按底座声明的承载能力判"不适用"。
		return McpVerdictResult{OK: true, Status: objdelivery.StatusProjected,
			Reason: fmt.Sprintf("经平台 MCP 网关代连（对底座是一条 stdio 条目；远端传输 %s）——开关 MATE_MCP_GATEWAY=off 可回到直连口径", m.Transport)}
	case !carried:
		return McpVerdictResult{Status: objdelivery.StatusNotApplicable,
			Reason: fmt.Sprintf("%s 不承载 %s 传输（它支持：%s）", rt, m.Transport, strings.Join(facts.Supported, "/"))}
	default:
		return McpVerdictResult{OK: true, Status: objdelivery.StatusProjected,
			Reason: fmt.Sprintf("以 %s 传输注入（会话的 mcps 选定未声明时默认全量）", m.Transport)}
	}
}

// ProjectedSkillOf 把 `skill.Projection` 的形态收成域侧事实的平凡构造（装配侧用）。
func ProjectedSkillOf(runtimeID string, projection runtime.Projection, err string) ProjectedSkill {
	return ProjectedSkill{Runtime: runtimeID, Projection: projection, Err: err}
}
