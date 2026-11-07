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
