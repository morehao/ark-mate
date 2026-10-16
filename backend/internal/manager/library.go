package manager

import (
	"fmt"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtolibrary"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/code"
)

// ——— M2-4：Library 统一读模型 ———
//
// Library **不拥有任何数据**（选型 E1）：一次调用内 join 三张表（skill/mcp/tool 注册表 +
// runtime 注册表），返回派生视图。**不做缓存**：几百条 × 一次子串匹配是几十微秒的量级，
// 而缓存要在五个写面（含手工改文件后重启装载）上各挂一次失效，收益微秒、代价是一个新的错误来源。
//
// 两件事在这里刻意**不做**：
//
//   - **不给合并结论**（如"这条能力整体可用吗"）：一条 skill 对某个底座可用、对另一个不可用是
//     有用信息，压成一个 bool 就没了。逐 runtime 报可达性 + 人话原因，由调用方决定怎么呈现。
//   - **不发明状态**：状态集与物化体检（§13.4 S2）共用同一套词（projected/untested/
//     not_configured/not_applicable/disabled），两处各造一套词就会让用户对着两个页面猜哪个是真的。

// ListLibrary 返回统一能力清单（按 kind、id 排序；排序稳定 → 面板与测试都不会"抖动"）。
func (s *Service) ListLibrary(q skill.Query) ([]dtolibrary.LibraryItem, error) {
	if q.Kind != "" && !dtolibrary.ValidKinds[q.Kind] {
		// 未知 kind 报 40000 而不是静默返回空表：静默会让"筛了但没有"与"我拼错了"长得一样。
		return nil, code.Error(code.InvalidParam,
			fmt.Sprintf("未知 kind %q（合法：skill|mcp|tool）", q.Kind))
	}
	needle := strings.ToLower(strings.TrimSpace(q.Q))

	items := make([]dtolibrary.LibraryItem, 0, 64)
	items = append(items, s.librarySkills()...)
	items = append(items, s.libraryMcps()...)
	items = append(items, s.libraryTools()...)

	out := items[:0]
	for _, it := range items {
		if q.Kind != "" && it.Kind != q.Kind {
			continue
		}
		if needle != "" && !libraryMatch(it, needle) {
			continue
		}
		// `applicability` 必须是**空数组**而不是 `null`：它是「逐候选 runtime 的清单」，
		// 没有候选时 `[]` 才是诚实的形状（`null` 让消费方把「没有候选」当成「字段缺失」，
		// 前端 map 上去直接炸）。松散的 nil 切片在 JSON 里就是 `null`，所以这里统一收口。
		if it.Applicability == nil {
			it.Applicability = []dtolibrary.Applicability{}
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// libraryMatch 子串匹配（id/name/description，大小写不敏感）。
func libraryMatch(it dtolibrary.LibraryItem, needle string) bool {
	return strings.Contains(strings.ToLower(it.ID), needle) ||
		strings.Contains(strings.ToLower(it.Name), needle) ||
		strings.Contains(strings.ToLower(it.Description), needle)
}

// librarySkills Skill 条目 + 逐 runtime 投影可达性。
//
// 候选 runtime = **本进程已解析出 skill 投影的全部 runtime**（不是技能声明白名单的子集）：
// 白名单决定"投不投"，可达性决定"能不能投"，两者混在一起就答不了"我把 runtimes 写成 [] 之后
// 到底会落到哪几个底座"。
func (s *Service) librarySkills() []dtolibrary.LibraryItem {
	if s.caps == nil {
		return nil
	}
	projections := s.skillProjectionsSnapshot()
	skills := s.caps.ListSkills()
	out := make([]dtolibrary.LibraryItem, 0, len(skills))
	for _, sk := range skills {
		it := dtolibrary.LibraryItem{
			ID: sk.ID, Kind: dtolibrary.KindSkill, Name: libraryName(sk.Name, sk.ID),
			Description: sk.Description, Origin: libraryOrigin(sk.SourceFile),
			Scope: sk.EffectiveScope(), Enabled: sk.IsEnabled(), SourceFile: sk.SourceFile,
			Warnings: skillWarningMessages(sk),
		}
		it.Applicability = skillApplicability(sk, projections)
		out = append(out, it)
	}
	return out
}

// skillApplicability 逐投影 runtime 报 skill 的可达性。
//
// 判据顺序（先判"平台这边有没有关掉"，再判"落点解析出来没有"，最后才判"实测过没有"）：
// 关掉是用户的显式决定，最先说；未解析是配置/声明问题；untested 只是"我们还没实测"，
// 会照常投放——所以它必须排在 projected 之前但排在 not_applicable 之后。
func skillApplicability(sk capability.Skill, projections []skill.Projection) []dtolibrary.Applicability {
	out := make([]dtolibrary.Applicability, 0, len(projections))
	for _, p := range projections {
		out = append(out, skillRuntimeVerdict(sk, p))
	}
	return out
}

// skillRuntimeVerdict 单个 (skill, 投影) 的可达性判定——**薄适配**（P3c）。
//
// **唯一实现**在 `domain/session.SkillVerdict`：Library 的逐 runtime 清单与建会话时的选定校验
// （M3-3）都走那一份。两处各写一遍必然漂移，而漂移的表现恰恰是最难自证的一种：
// 「Library 说不可达、建会话却放行」（或反过来）。
func skillRuntimeVerdict(sk capability.Skill, p skill.Projection) dtolibrary.Applicability {
	// P3c：判决本体已落 `domain/session`（唯一实现）；这里只把事实交过去、把结果套回读模型形态。
	// 状态集与文案**逐字未改**——Library 与"建会话被拒"必须永远同向。
	v := session.SkillVerdict(sk, session.SkillVerdictFacts{
		Runtime:      p.Runtime,
		Projection:   session.ProjectedSkillOf(p.Runtime, p.Projection, p.Err),
		ProjectionOK: true,
	})
	return dtolibrary.Applicability{Runtime: p.Runtime, OK: v.OK, Status: v.Status, Reason: v.Reason}
}

// libraryMcps MCP 条目 + 逐 runtime 可达性（按 transport 承载）。
func (s *Service) libraryMcps() []dtolibrary.LibraryItem {
	if s.caps == nil {
		return nil
	}
	servers := s.caps.ListMcpServers()
	runtimes := s.reg.IDs()
	out := make([]dtolibrary.LibraryItem, 0, len(servers))
	for _, m := range servers {
		it := dtolibrary.LibraryItem{
			ID: m.ID, Kind: dtolibrary.KindMcp, Name: libraryName(m.Name, m.ID),
			Description: libraryMcpDesc(m), Origin: libraryOrigin(m.SourceFile),
			Scope: m.EffectiveScope(), Enabled: m.IsEnabled(), SourceFile: m.SourceFile,
		}
		if !m.IsEnabled() {
			it.Warnings = append(it.Warnings, "该 server 已被禁用（enabled:false）：定义与密钥引用都保留，只是不注入")
		}
		if keys := s.unresolvedSecretKeys(m); len(keys) > 0 {
			it.Warnings = append(it.Warnings,
				fmt.Sprintf("密钥引用当前解析不出值（%s）：注入时会跳过这个 server", strings.Join(keys, ", ")))
		}
		it.Applicability = mcpApplicability(m, runtimes, s)
		out = append(out, it)
	}
	return out
}

// mcpApplicability 逐 runtime 报 MCP server 的可达性（判据 = 该 runtime 是否承载这种 transport）。
func mcpApplicability(m capability.McpServer, runtimes []string, s *Service) []dtolibrary.Applicability {
	out := make([]dtolibrary.Applicability, 0, len(runtimes))
	for _, rt := range runtimes {
		out = append(out, mcpRuntimeVerdict(m, rt, s))
	}
	return out
}

// mcpRuntimeVerdict 单个 (server, runtime) 的可达性判定——**薄适配**（P3c；理由同 skillRuntimeVerdict）。
func mcpRuntimeVerdict(m capability.McpServer, rt string, s *Service) dtolibrary.Applicability {
	// P3c：判决本体已落 `domain/session`（唯一实现）。事实（承载哪些传输 / 在不在册 / 网关开关）
	// 由装配侧如实交出——域只消费结论。
	v := session.McpVerdict(m, s.McpVerdictFacts(rt))
	return dtolibrary.Applicability{Runtime: rt, OK: v.OK, Status: v.Status, Reason: v.Reason}
}

// libraryTools Tool 条目 + 归属 server 的可承载 runtime（builtin/observed 对所有 runtime 可达）。
func (s *Service) libraryTools() []dtolibrary.LibraryItem {
	if s.caps == nil {
		return nil
	}
	tools := s.caps.ListTools()
	runtimes := s.reg.IDs()
	out := make([]dtolibrary.LibraryItem, 0, len(tools))
	for _, t := range tools {
		it := dtolibrary.LibraryItem{
			ID: t.Name, Kind: dtolibrary.KindTool, Name: t.Name, Description: t.Description,
			Origin: libraryOrigin(t.SourceFile), Scope: capability.SkillScopeGlobal, Enabled: true,
			SourceFile: t.SourceFile,
		}
		switch {
		case t.Source == capability.ToolSourceMCP && t.McpServer != "":
			server, ok := s.caps.GetMcpServer(t.McpServer)
			if !ok {
				it.Warnings = append(it.Warnings, fmt.Sprintf(
					"归属的 MCP server %q 已不在注册表里（这条工具暂时调不通，重新注册该 server 或删掉它）", t.McpServer))
				break
			}
			it.Scope = server.EffectiveScope()
			it.Applicability = mcpApplicability(server, runtimes, s)
		default:
			for _, rt := range runtimes {
				it.Applicability = append(it.Applicability, dtolibrary.Applicability{Runtime: rt, OK: true,
					Status: dtolibrary.StatusProjected, Reason: "平台内置/观察到的工具：不依赖 MCP server"})
			}
		}
		out = append(out, it)
	}
	return out
}

// libraryName 名称回落（未声明 name 的条目显示 id，而不是空字符串）。
func libraryName(name, id string) string {
	if strings.TrimSpace(name) == "" {
		return id
	}
	return name
}

// libraryOrigin 血缘判据：**有没有事实源文件**（有 = 本机定义文件 = local）。
//
// 只有一条判据是刻意的：多一条"看起来像内置"的启发式，就会出现"用户自己建了一个叫 dsh 的
// 定义却被标成 imported"这种自相矛盾的展示。
func libraryOrigin(sourceFile string) string {
	if sourceFile == "" {
		return dtolibrary.OriginImported
	}
	return dtolibrary.OriginLocal
}

// libraryMcpDesc MCP 的读模型描述（"以什么方式连到哪"——面板一行里最有用的那句话）。
func libraryMcpDesc(m capability.McpServer) string {
	if m.Transport == capability.TransportStdio {
		if m.Command == "" {
			return fmt.Sprintf("stdio：%s", m.Transport)
		}
		return fmt.Sprintf("stdio：%s %s", m.Command, strings.Join(m.Args, " "))
	}
	return fmt.Sprintf("%s：%s", m.Transport, m.URL)
}

// skillWarningMessages 把 skill 的装载期告警转成 Library 的字符串列表。
func skillWarningMessages(sk capability.Skill) []string {
	ws := skill.Warnings(sk)
	if len(ws) == 0 {
		return nil
	}
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Message)
	}
	return out
}
