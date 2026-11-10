package sessionruntime

// ——— 平台内置 MCP server 注入（automation / material 工具面）———
//
// 把平台自带的两个 MCP stdio 子命令注入到会话：`mate mcp automation`（自然语言创建/编辑定时任务）
// 与 `mate mcp material`（资料库文件级工具 + 产物收录）。目的是让模型在会话里直接能用这些能力，
// 不必让用户手动在能力表里注册一条 stdio server。
//
// 三条边界：
//  1. **Command 用本进程自己的可执行文件**（`os.Executable()`），不依赖 PATH 里有 `mate`
//     ——与网关条目（`gatewayEntry`）同款配方。已知代价同网关：`go run` 开发进程路径每次不同
//     （只影响钉住会话的指纹，缺省路径不受影响）。
//  2. **只注入本机启动的 runtime**：远程 ACP 端点（`Launch.Transport` 是 http/ws…）上没有我们的
//     可执行文件，注入一条指向不存在命令的 stdio 条目会让底座反复拉起失败——故对远程端点跳过。
//  3. **注入面与指纹共用**：这些 server 进 `plan.Direct`，于是 `McpServersFor` 与
//     `CapabilityFactsFor` 看到的是同一份（不会"指纹说没变、注入却多了"）。但注意：
//     `sel == nil` 时域侧指纹恒为 ""（缺省零回归），故缺省会话不会因这次注入而换代际——
//     新会话立即有工具，老会话重启后才有。

import (
	"os"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/runtime"
)

// 平台内置 MCP server 的 id / 展示名（稳定；测试与指纹都认它）。
//
// `mate-platform` 聚合了三域（一个进程、一条注入）：automation_*（定时任务）+ expert_*（专家定义）
// + library_*（资料库）。`mate mcp material` / `mate mcp automation` 子命令仍保留为独立入口
// （调试/兼容），但**默认只注入 `mate-platform` 这一条**。
const (
	PlatformMcpPlatformID   = "platform"
	PlatformMcpPlatformName = "mate-platform"

	// AppMcpAppID / AppMcpAppName 是 app 业务工具面那条独立 stdio server 的稳定 id / 展示名
	// （WP5，设计 §5.3a）。与平台内置 server **是两条**：前者按会话 app scope 注入、默认零；
	// 后者恒注入。测试与指纹都认这两个常量。
	AppMcpAppID   = "app"
	AppMcpAppName = "mate-app"
)

// platformMcpServers 构造平台内置 MCP server 定义（stdio；Command = 本进程可执行文件）。
//
// 返回顺序稳定（platform 在前），保证注入与指纹确定性。取不到自身可执行文件路径 → 返回 nil
// （宁可少几个工具，也不注入指向空命令的条目——那会毁掉整条会话的 session/new）。
func (s *Service) platformMcpServers() []capability.McpServer {
	exe, err := os.Executable()
	if err != nil {
		s.mcpGatewayWarnOnce("platform-entry", "平台内置 MCP server 不可用：取不到自身可执行文件路径，本轮不注入", "err", err)
		return nil
	}
	return []capability.McpServer{
		{ID: PlatformMcpPlatformID, Name: PlatformMcpPlatformName, Transport: capability.TransportStdio, Command: exe, Args: []string{"mcp", "platform"}},
	}
}

// appendPlatformMcpServers 把平台内置 server 追加进 plan.Direct（满足三态白名单与开关与远程端点判断）。
//
// 门槛（任一不满足就不注入）：
//   - 开关 `MATE_PLATFORM_MCP=off` 关掉；
//   - 会话钉 `[]`（整个 MCP 面关掉）；
//   - runtime 未注册、或远程 ACP 端点（本机没有该可执行文件）；
//   - 取不到自身可执行文件路径。
//
// 会话钉非空白名单**不**挡平台内置：它们是产品默认能力，不是用户可选集。
func (s *Service) appendPlatformMcpServers(plan *mcpInjectionPlan, runtimeID string, sel *session.CapabilitySelection) {
	if plan == nil || !s.planes.PlatformMcpEnabled() {
		return
	}
	// 未知 runtime：core 已返回空计划（不下发），这里也不注入。
	spec, ok := s.regSpec(runtimeID)
	if !ok {
		return
	}
	// 只有本机启动的 runtime 才注入（远程端点没有本机可执行文件）。
	if !runtime.LaunchesLocally(spec) {
		return
	}
	// 会话钉 `[]`：整个 MCP 面关掉，平台内置也不注入（与网关条目同口径）。
	if sel != nil && sel.Mcps != nil && len(*sel.Mcps) == 0 {
		return
	}
	plan.Direct = append(plan.Direct, s.platformMcpServers()...)
}

// ——— WP5：app 业务工具独立注入通道（会话级）———
//
// 设计 §5.3(a)：app 的**业务**工具绝不挂在 `mate mcp platform`（那条恒注入且会话不挡）上，
// 而是按**会话 app scope**（`CapabilitySelection.Apps`）注入一条**独立的** stdio server：
//
//	{ID: "app", Name: "mate-app", Command: <本进程可执行文件>, Args: ["mcp","app","--apps=a,b"]}
//
// **通道形状 = 单条合并**（设计 §5.3 给的二选一里的"按会话内启用集合合并成一条"）：
// 一个会话只需一条 `mate mcp app --apps=a,b` 子进程，`--apps` 里按升序放该会话启用且**确实绑了
// 工具域**的 app id 并集；域的分派在子进程内由 `infra/apptool` 聚合器按域前缀完成。选单条而不是
// 每 app 一条的理由：一条进程 = 一处注入 = 一个稳定指纹片段（每 app 一条会让"app 越多、注入条目
// 越多"，进程数与会话注入面随 app 数线性膨胀），且与平台侧"一条 mate-platform 聚合"同构。
//
// 三条边界（与 platformMcpServers 并列，但**互不影响**——两个函数各管各的）：
//  1. **默认零注入**：`Apps` nil（未声明）或空数组（明确不要）→ 一个 app 工具都不注入。
//     这是"关得干净"（G6）的结构性保证：没有 app scope 就结构上不可能漏出 app 工具。
//  2. **只注入本机 runtime**：远程 ACP 端点（http/ws…）没有本机可执行文件，注入 stdio 会让底座反复
//     拉起失败——与平台内置同款判断（`runtime.LaunchesLocally`）。
//  3. **注入面与指纹共用**：本条目进 `plan.Direct`，于是 `McpServersFor`（注入）与 `CapabilityFactsFor`
//     （指纹）看到的是**同一份**——"app 工具多了/少了"必然换代际（`Args` 里的 `--apps=` 进指纹，
//     与 stdio server 同口径）。这条**不依赖**改 capability_fingerprint.go：指纹边界③"只纳入真正会
//     生效的东西"已由 facts.Direct 满足。
//
// 红线：本函数**绝不修改** `platformMcpServers` / `appendPlatformMcpServers` 的任何逻辑——平台注入恒化
// 是设计不可退让点，app 工具只走这条独立通道。
func (s *Service) appendAppMcpServers(plan *mcpInjectionPlan, runtimeID string, sel *session.CapabilitySelection) {
	if plan == nil || s.appToolScope == nil {
		// 端口未接线 = 本进程没有 app 工具面（memory 测试、未装配 app 的入口）：零注入。
		return
	}
	// 三态：nil（未声明）与 `[]`（明确不要）都零注入。只有非空 app 白名单才可能注入。
	appIDs := sel.AppIDs()
	if len(appIDs) == 0 {
		return
	}
	// 把这些 app id 解析成"已启用且绑了域"的工具域并集（未知/停用 app 不贡献域，自然收窄）。
	domains := s.appToolScope.EnabledToolDomains(appIDs)
	if len(domains) == 0 {
		// 启用的 app 都没绑工具域（或全被停用/删除）→ 没有可注入的东西，零注入。
		return
	}
	// 只注入本机启动的 runtime（远程端点没有本机可执行文件）。
	spec, ok := s.regSpec(runtimeID)
	if !ok || !runtime.LaunchesLocally(spec) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		// 与平台内置同款降级：宁可不注入，也不注入一条指向空命令的条目（那会毁掉整条 session/new）。
		s.mcpGatewayWarnOnce("app-entry", "app 工具 server 不可用：取不到自身可执行文件路径，本轮不注入 app 工具", "err", err)
		return
	}
	// `--apps=a,b`：升序、逗号分隔（`EnabledToolDomains` 已保证域去重升序，这里对 app id 排序保证稳定）。
	ids := append([]string(nil), appIDs...)
	sort.Strings(ids)
	server := capability.McpServer{
		ID:        AppMcpAppID,
		Name:      AppMcpAppName,
		Transport: capability.TransportStdio,
		Command:   exe,
		Args:      []string{"mcp", "app", "--apps=" + strings.Join(ids, ",")},
	}
	plan.Direct = append(plan.Direct, server)
}
