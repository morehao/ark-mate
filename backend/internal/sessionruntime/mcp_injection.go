package sessionruntime

// ——— MCP 注入计划与平台侧网关分派（M7-3）———
//
// 一个**值对象**决定三件事，且只算一次：
//
//	① 哪些 server 直连注入底座（stdio）；
//	② 要不要注入平台网关条目（存在远程 server 且开关允许时）；
//	③ 代际指纹拿什么当输入。
//
// 为什么是同一个值对象而不是两个函数：指纹必须**覆盖真正会被注入的东西**，否则会出现
// "指纹说这套能力含 X，实际没注入 X"（或反之）——两种都是"改了定义却不换代际/白换代际"。
// 而指纹又**绝不能**拿到解析后的明文密钥（那是 §13.4 的边界①，会进日志/事件/DB 列）。
// 所以计划里只放**定义**（含 `store:`/`env:` 引用）与**网关条目**（只有 scope，天生无密钥），
// 明文解析留给真正拼注入条目的那一刻。
//
// 网关分派（P-2/P-3 的落点）：
//
//	stdio → 原样直连（C-5：stdio 子进程仍归底座自建自管；S2 是另案）
//	远程  → 合成**一条** stdio 条目 `mate-gateway = <平台可执行文件> mcp bridge --runtime …`
//
// 条目里**不含任何每台 server 的信息**——于是"加一个远程 server"不改变注入项、也就不换代际
// （网关的头号收益）。唯一会让条目变的是 scope 本身与会话钉住的 `mcps`（用户的选定）。

import (
	"context"
	"fmt"
	"os"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/capability"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/infra/mcp"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// mcpInjectionPlan MCP 注入计划（**不含明文密钥**）。
type mcpInjectionPlan struct {
	// Direct 直连注入的定义（含密钥引用，不含明文）：正常情况下只有 stdio（远程经网关），
	// **远程端点 runtime** 例外——它们没有网关条目，仍按自己声明的传输直连注入（见 planMcpInjection）。
	Direct []capability.McpServer
	// Gateway 平台网关条目（nil = 不需要）。远程 server **全部**由它代连，故这里只有一条。
	Gateway *acl.McpServer
	// Remote 交由网关代连的远程定义——**只用于日志/诊断**：它绝不进指纹，也不进条目。
	// （任何"把远程定义塞进指纹"的改动都会让"加一个远程 server 换代际"回潮，是 M7 的头号禁忌。）
	Remote []capability.McpServer
}

// empty 报告计划里一台 server 都没有（含网关条目）。
func (p mcpInjectionPlan) empty() bool {
	return len(p.Direct) == 0 && p.Gateway == nil
}

// planMcpInjection 算出该 runtime 的 MCP 注入计划（唯一实现：注入面与指纹共用）。
//
// 与 `mcpSelectionFor`（白名单优先级那一段）的关系：选择语义在 `pkg/capability.SelectMcpServers`，
// 这里负责**按传输分派**——直连那一支只收 stdio，远程那一支收远程三种。开关关掉（`mcp_gateway=off`）
// 时回到老路：远程 server 按该 runtime 声明的承载能力直连注入（能承载才注入，承载不了就跳过）。
func (s *Service) planMcpInjection(runtimeID string, sel *session.CapabilitySelection) mcpInjectionPlan {
	var plan mcpInjectionPlan
	if s.planes.Capabilities == nil {
		return plan
	}
	supported, ok := s.planes.Registry.McpTransportsFor(runtimeID)
	if !ok {
		return plan // 未知 runtime → 不下发（维持现状）
	}
	// 白名单只有**会话钉住的选定**这一个来源（含空数组 = 本会话一个 MCP 都不要）；
	// v19 之前这里还有一层 agent 的 `mcps` 声明，agent 面删除后那一层随之消失。
	allowIDs := (*[]string)(nil)
	if sel != nil && sel.Mcps != nil {
		allowIDs = sel.Mcps
	}
	all := s.planes.Capabilities.ListMcpServers()

	if !s.planes.McpGatewayEnabled() {
		// 回退路径（MATE_MCP_GATEWAY=off）：远程也直连，故传输判据就是该 runtime 的声明。
		plan.Direct = capability.SelectMcpServers(all, supported, allowIDs)
		return plan
	}
	if spec, ok := s.regSpec(runtimeID); ok && !runtime.LaunchesLocally(spec) {
		// **远程 ACP 端点**（`Launch.Transport` 是 http/ws/…）：网关条目是"本机起子进程"的配方，
		// 对它没有意义（对端机器上没有那个可执行文件）。这类 runtime 保持 M7 之前的形态：按它
		// 自己声明的传输直连注入（远端 MCP 的 URL 由对端自己连，语义成立）——零回归。
		plan.Direct = capability.SelectMcpServers(all, supported, allowIDs)
		return plan
	}

	// 网关在环：直连那一支只收 stdio（其余经网关，别再直连一份，否则同一台 server 会出现两次）；
	// 远程那一支只是**为了日志**——条目本身与"有几台远程"无关（见下）。
	plan.Direct = capability.SelectMcpServers(all, []string{capability.TransportStdio}, allowIDs)
	plan.Remote = capability.SelectMcpServers(all, capability.RemoteMcpTransports(), allowIDs)

	// **条目恒定**（设计 §10.6-⑤：「增删一个远程 MCP 不再改变底座的配置面」）：用户没有显式关掉
	// MCP 面时，网关条目**一定**注入——哪怕此刻一台远程 server 都没有。这一条是整个 M7 收益的
	// 前提，不是可选优化：
	//   · 条件注入（"有远程才注入"）会让**从零到一**那次仍然改动底座的配置面 ⇒ 仍然换代际；
	//   · 而缺省路径（指纹恒 ""）根本不会迁移，那条会话就会永远看不到后来加的远程 server
	//     （静默——正是这套设计要消灭的失败面）。
	// 代价如实记在配置项注释里：没有远程 server 的用户也会多一个空闲的 `mate mcp bridge` 子进程；
	// 不接受这笔开销就用 `MATE_MCP_GATEWAY=off` 回退（那时远程返回直连注入，条目也不再存在）。
	if allowIDs != nil && len(*allowIDs) == 0 {
		// 显式"一个 MCP 都不要"（会话钉的 `[]`）：这是用户把整个 MCP 面
		// 关掉了。此时连网关条目都不该出现——它的存在本身就是"这个面还在"，与声明相悖。
		return plan
	}
	entry, err := s.gatewayEntry(runtimeID, sel)
	if err != nil {
		// 拼不出网关条目（取不到自身可执行文件路径）→ 远程 server 这轮**没有出路**。
		// 响亮降级：宁可少工具，也不要注入一条指向不存在命令的 stdio 条目（底座会反复拉起失败）。
		s.mcpGatewayWarnOnce("entry", "mcp 网关条目不可用，本轮远程 MCP 不注入", "err", err)
		plan.Remote = nil
		return plan
	}
	plan.Gateway = &entry
	return plan
}

// gatewayEntry 合成网关注入条目（P-2：一条 stdio 条目 + 稳定 scope）。
//
// `Command` 用**当前进程自己的可执行文件**：网关就是平台的另一个子命令，底座按 stdio server
// 把它拉起来时不需要知道平台装在哪（也不该依赖 PATH 里有 `mate`）。
// 已知代价（写下来免得被当成 bug）：`go run` 起的开发进程路径是临时目录、每次重启都不同，
// 于是**钉住会话**的指纹会随之变化（缺省路径指纹恒空，不受影响）；打包/安装形态下路径稳定。
//
// `Env` 里带的是**凭据 cwd**（`mcp.GatewayCredCwdEnv`，不是密钥）：网关是底座 spawn 的子进程，
// 它的 cwd 是底座给的（会话工作区），而 `<cwd>/.env` 那一层的语义是"用户启动 `mate serve` 的
// 目录"——没有这个变量，同一个引用会在两个进程里解析出不同的值。放 `Env` 而不是 `Args`：`Args`
// 整体进指纹（换个目录启动就换代际），而 `Env` 只把**键名**入指纹（键名恒定、值不入）。
func (s *Service) gatewayEntry(runtimeID string, sel *session.CapabilitySelection) (acl.McpServer, error) {
	exe, err := os.Executable()
	if err != nil {
		return acl.McpServer{}, fmt.Errorf("取自身可执行文件路径失败：%w", err)
	}
	scope := mcp.GatewayScope{RuntimeID: runtimeID}
	if sel != nil {
		// 三态原样传递：nil = 该会话没钉（网关按全量）；`[]` = 一个远程都不要。
		scope.McpsPinned = sel.Mcps
	}
	entry := acl.McpServer{
		Name:      mcp.GatewayServerName,
		Transport: capability.TransportStdio,
		Command:   exe,
		Args:      mcp.GatewayBridgeArgs(scope),
	}
	// 空 = 装配期没登记（测试、`mate demo` 这类不经装配的入口）：**不带这个键**，让网关侧
	// 明确知道"核心没交 cwd"，回落并 WARN——不臆造一个 `os.Getwd()` 冒充核心的答案。
	if cwd := s.planes.Credentials.Cwd(); cwd != "" {
		entry.Env = map[string]string{mcp.GatewayCredCwdEnv: cwd}
	}
	return entry, nil
}

// mcpGatewayWarnOnce 逐 key 只 WARN 一次（组装发生在每次建会话/取指纹时，不节流会刷屏）。
func (s *Service) mcpGatewayWarnOnce(key, msg string, args ...any) {
	s.mcpGatewayWarnMu.Lock()
	if s.mcpGatewayWarned == nil {
		s.mcpGatewayWarned = make(map[string]bool)
	}
	seen := s.mcpGatewayWarned[key]
	s.mcpGatewayWarned[key] = true
	s.mcpGatewayWarnMu.Unlock()
	if seen {
		return
	}
	glog.Warnw(context.Background(), msg, args...)
}
