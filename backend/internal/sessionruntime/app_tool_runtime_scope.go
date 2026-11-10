package sessionruntime

// ——— WP-C4：运行期包工具面的注入端口 ———
//
// 一期 WP5 的 `appendAppMcpServers` 只处理「编译期 app」：这些 app 的工具域 handler 编在二进制里
// （`infra/<app>mcp`），子进程 `mate mcp app --apps=a,b` 起来后按域前缀分派即可。二期 C 的**运行期包**
// 不同——它们的工具住在 C3 的隔离 host 子进程里，二进制里没有对应 handler，因此不能靠同一条
// `mate mcp app --apps=` 覆盖。
//
// 本端口把「运行期包 app → 它的 stdio 适配 server 条目」的解算**上移给装配根**：
//
//	连到已起 app-host / 起 host 的 stdio 适配——两种形态的细节（同二进制 `mate app-host` 适配、
//	还是连一条已在跑的 host 的桥）由装配根决定并在这里以 `capability.McpServer` 条目给出；
//	sessionruntime 只负责「按会话 app scope 是否注入、注入哪几条」，不认具体 host/命令。
//
// 这样 sessionruntime 保持对 apphost 的零依赖（核心进程不认识 host 实现），也复用同一套注入面
// （都进 plan.Direct，与指纹共用）。
//
// 不变量（与 WP5 完全一致，不因新增端口而变）：
//   - **默认零注入**：会话 Apps nil/空 → 不询问本端口、不注入任何运行期工具。
//   - **越权 0 例外**：端口只被问「会话已声明且非空的这些 app id」；运行期包不能越过 session 三态。
//   - **平台恒注入不动**：本端口只往 app 侧追加，绝不碰 platformMcpServers/appendPlatformMcpServers。

import (
	"sort"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/runtime"
)

// RuntimeAppToolScope 解算运行期包 app 的 stdio 适配 server 条目（装配根实现；nil = 无运行期包工具）。
type RuntimeAppToolScope interface {
	// RuntimeAppServers 返回给定 app id 里**属于运行期包**且已启用者的 stdio 适配 server 条目。
	//
	// 语义（与 AppToolScope.EnabledToolDomains 同口径）：
	//   - appIDs 是会话已声明的 app 白名单（本端口只被以非空列表调用，默认零注入在上层保证）。
	//   - 未知 / 停用 / 已删除 / **非运行期包（编译期 app）** 的 id → **不返回条目**（自然收窄，
	//     不是错误：编译期 app 由 appendAppMcpServers 既有通道处理，本端口只管运行期包）。
	//   - 返回空切片 = 这些 id 里没有运行期包 app（或都停用/无工具面）→ 零注入。
	//   - 单条条目应自带稳定 ID（进指纹），且 Transport 为 stdio（本机启动）。
	RuntimeAppServers(appIDs []string) []capability.McpServer
}

// SetRuntimeAppToolScope 注入运行期包工具面解算端口（装配根在包装载就绪后接线）。
func (s *Service) SetRuntimeAppToolScope(sc RuntimeAppToolScope) { s.runtimeAppToolScope = sc }

// appendRuntimeAppMcpServers 把运行期包 app 的 stdio 适配条目追加进 plan.Direct（WP-C4）。
//
// 三条边界与 appendAppMcpServers **完全一致**（两者并列、互不影响，也都只碰 app 侧）：
//  1. **默认零注入**：Apps nil/空 → 不询问端口、零注入（越权 0 例外的结构性保证）。
//  2. **只注入本机 runtime**：远程 ACP 端点没有本机可执行文件，注入 stdio 会反复拉起失败。
//  3. **注入面与指纹共用**：条目进 plan.Direct，注入（McpServersFor）与指纹（CapabilityFactsFor）
//     看到同一份。
//
// 红线：本函数**绝不修改** platformMcpServers / appendPlatformMcpServers；运行期工具只走这条 app 侧通道。
func (s *Service) appendRuntimeAppMcpServers(plan *mcpInjectionPlan, runtimeID string, sel *session.CapabilitySelection) {
	if plan == nil || s.runtimeAppToolScope == nil {
		// 端口未接线 = 本进程没有运行期包工具面：零注入。
		return
	}
	// 三态：nil（未声明）与 `[]`（明确不要）都零注入。只有非空 app 白名单才可能注入。
	appIDs := sel.AppIDs()
	if len(appIDs) == 0 {
		return
	}
	// 只注入本机启动的 runtime（远程端点没有本机可执行文件）。
	spec, ok := s.regSpec(runtimeID)
	if !ok || !runtime.LaunchesLocally(spec) {
		return
	}
	// 解算运行期包 app 的 stdio 适配条目（装配根决定"连已起 host / 起 host 适配"的具体形态）。
	servers := s.runtimeAppToolScope.RuntimeAppServers(appIDs)
	if len(servers) == 0 {
		// 这些 id 里没有运行期包 app（或都停用/无工具面）→ 零注入。
		return
	}
	// 稳定顺序：按 server ID 升序追加，保证注入与指纹确定性（端口返回顺序不作保证）。
	stabilized := append([]capability.McpServer(nil), servers...)
	sort.SliceStable(stabilized, func(i, j int) bool { return stabilized[i].ID < stabilized[j].ID })
	plan.Direct = append(plan.Direct, stabilized...)
}
