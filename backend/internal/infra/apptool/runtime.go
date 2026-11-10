package apptool

// ——— WP-C4：运行期包工具域的「并列聚合」———
//
// 一期 A+Slot 的工具域 handler 全部**编在二进制里**（`infra/<app>mcp`），由装配根收进一个
// `map[domainKey]mcp.Handler` 交给 `NewAggregator`。二期 C 里，**运行期包**（第三方包）的工具
// 不在二进制里——它们住在 C3 的隔离 host 子进程（`infra/apphost.Host`）里，通过 JSON-lines RPC 的
// `listTools` / `invokeTool` 暴露。
//
// 本文件把「一个运行期 host 暴露的工具」包成与编译期 handler **同形**的 `mcp.Handler`，于是二者可以
// **并列**塞进同一个 `platformmcp.New(...)` 的分派表：域前缀分派、重复前缀 fail-closed 冲突裁决全部
// **复用** `platformmcp`（不重写）。运行期 handler 只是「一个转发到 host RPC 的域实现」。
//
// 与编译期 handler 的两个差别（都体现"运行期"语义）：
//  1. **域由包自述**：编译期 handler 的域是构造时写死的；运行期 handler 的域来自包 `listTools` 自述
//     的工具名前缀。故本文件先 `ResolveRuntimeDomains(host)` 读一次自述，把工具按「名字首段」分域，
//     再为每个域造一个 handler。
//  2. **声明了域却没有实现 → FAILED**（设计 §13）：包清单（apppkg manifest `provides.tools`）声明的
//     某个域在 host 自述里**一个工具都没有**，则该 app 的工具面 FAILED 并告警——不静默吞掉，避免
//     "app 说它提供 foo 工具、实际一个都没有"这种最难查的静默失效。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
)

// ToolInvoker 是运行期 host 暴露给本层的最小端口（`infra/apphost.Host` 结构上满足它；抽成接口
// 是为了本包可测试——无需真起子进程，用桩即可，也避免 apptool 反向依赖 apphost 具体类型）。
//
// 两个方法对应 host RPC 的 listTools / invokeTool：
//   - ListTools：本进程提供的工具自述（name + schema）。
//   - InvokeTool：调用一个工具，返回 (内容原文, 是否业务错误, 错误)。error 非空 = RPC 层失败。
type ToolInvoker interface {
	ListTools() ([]ToolDesc, error)
	InvokeTool(name string, args []byte) (content []byte, isError bool, err error)
}

// ToolDesc 是运行期 host 自述的一个工具（本层只关心名字用于分域；其余透传给 MCP 面）。
//
// 与 `apphost.ToolSpec` 字段对齐但**不 import apphost**（本包在最内层的聚合层，不依赖具体 host
// 实现；装配根用一个小适配器把 ToolSpec 转成本类型即可，或 apphost.ToolSpec 直接结构兼容）。
type ToolDesc struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// domainOf 取工具名的域前缀（第一段，`_` 之前）。与 `platformmcp` 的分派口径一致：工具 `foo_create`
// 的域是 `foo`；没有下划线的工具名整段即域。
func domainOf(name string) string {
	if i := strings.IndexByte(name, '_'); i >= 0 {
		return name[:i]
	}
	return name
}

// runtimeDomainHandler 是把「某 host 在某域下的工具」包成 `mcp.Handler` 的实现：
//
//   - ListTools：从 host 自述里筛出该域的工具（按名字首段 == domain），转成 `protocol.McpTool`。
//   - CallTool：工具名不属于本域 → `mcp.ErrToolNotFound`（协议层错误，与 platformmcp 一致：
//     不该由非本域的 handler 兜底一个它不认识的工具）；否则转发 host.invokeTool，把 host 的
//     isError / RPC 失败翻成 MCP 语义（业务错误 → `CallResult{IsError:true}` 结果；RPC 失败 →
//     error，由协议层包成 isError 结果，会话不炸）。
type runtimeDomainHandler struct {
	domain string
	host   ToolInvoker
}

// ListTools 列出本域工具（每次都重读 host 自述，不缓存：host 侧工具面可随状态变，与 MCP 语义一致）。
func (h *runtimeDomainHandler) ListTools(context.Context) ([]protocol.McpTool, error) {
	descs, err := h.host.ListTools()
	if err != nil {
		return nil, fmt.Errorf("apptool: 运行期域 %q 列工具失败: %w", h.domain, err)
	}
	out := make([]protocol.McpTool, 0, len(descs))
	for _, d := range descs {
		if domainOf(d.Name) != h.domain {
			continue
		}
		out = append(out, protocol.McpTool{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema})
	}
	return out, nil
}

// CallTool 转发到 host.invokeTool（仅当工具名属于本域）。
func (h *runtimeDomainHandler) CallTool(_ context.Context, name string, args json.RawMessage) (protocol.McpCallResult, error) {
	if domainOf(name) != h.domain {
		// 防御：本域 handler 只认本域工具。platformmcp 已按前缀分派到这里，正常不会走到；
		// 万一被直接调用（测试/误接），返回协议层 ErrToolNotFound 而不是转发到 host 造成越界。
		return protocol.McpCallResult{}, mcp.ErrToolNotFound
	}
	content, isError, err := h.host.InvokeTool(name, args)
	if err != nil {
		// RPC 层失败：返回 error，由协议层包成 isError 结果（模型看得到失败原因，会话不炸）。
		return protocol.McpCallResult{}, fmt.Errorf("apptool: 运行期域 %q 调用 %q 失败: %w", h.domain, name, err)
	}
	res := protocol.McpCallResult{IsError: isError}
	if len(content) > 0 {
		// host 返回的 content 已是「MCP 风格 content 数组或任意 JSON 原文」，透传（与 apphost 契约一致）。
		res.Content = append(res.Content, json.RawMessage(content))
	}
	return res, nil
}

// RuntimeHandlerSet 是一个运行期包的域 handler 集合 + 该包声明的域集合（用于 §13 的「声明了却无实现」检查）。
type RuntimeHandlerSet struct {
	// PackageID 包 id（诊断/失败归属用）。
	PackageID string
	// DeclaredDomains 包清单声明的工具域（apppkg manifest `provides.tools[].domain`）。
	DeclaredDomains []string
	// Handlers 由 host 自述解析出来的、按域分的 handler（域 = 工具名首段）。
	Handlers map[string]mcp.Handler
}

// ResolveRuntimeDomains 读一次 host 的 listTools 自述，按「工具名首段」分域，为每个**出现过的域**造一个
// 运行期 handler。
//
// 返回 (*RuntimeHandlerSet, error)：
//   - error 非空 = host 自述本身读不到（listTools RPC 失败/超时）→ 调用方应把整个运行期 app 工具面
//     判 FAILED（该包所有工具不可用）。
//   - 成功时 Handlers 只含「自述里确实有工具」的域；**声明了域但自述里没有**的处置由调用方
//     `CheckDeclaredDomains` 判定（那是"包说不一致"，与本函数读不到自述是两个层次）。
func ResolveRuntimeDomains(packageID string, host ToolInvoker) (*RuntimeHandlerSet, error) {
	descs, err := host.ListTools()
	if err != nil {
		return nil, fmt.Errorf("apptool: 运行期包 %q 读取工具自述失败: %w", packageID, err)
	}
	seen := make(map[string]bool, len(descs))
	for _, d := range descs {
		dom := domainOf(d.Name)
		if dom == "" {
			continue
		}
		seen[dom] = true
	}
	handlers := make(map[string]mcp.Handler, len(seen))
	for dom := range seen {
		handlers[dom] = &runtimeDomainHandler{domain: dom, host: host}
	}
	return &RuntimeHandlerSet{PackageID: packageID, Handlers: handlers}, nil
}

// CheckDeclaredDomains 检查「包声明要提供的域」是否都被 host 自述兑现了。
//
// 设计 §13：清单声明了某工具域，但 host 自述里**一个工具都没有** → 该 app 工具面 FAILED 并告警
// （不静默吞掉）。返回缺失的域列表（空 = 全部兑现）。声明为空视为「不声明工具域」，不报缺失。
//
// 注意：本函数只做「声明 vs 自述」的一致性检查；自述里多出来、清单未声明的域**不**据此扩权——
// 运行期包能注入哪些域最终由调用方按清单 `DeclaredDomains` 收窄（见 SelectRuntimeDomains）。
func CheckDeclaredDomains(declared []string, hs *RuntimeHandlerSet) []string {
	if hs == nil {
		return append([]string(nil), declared...)
	}
	var missing []string
	for _, d := range declared {
		if _, ok := hs.Handlers[d]; !ok {
			missing = append(missing, d)
		}
	}
	sort.Strings(missing)
	return missing
}

// SelectRuntimeDomains 按清单声明的域，从运行期 handler 集合里挑出子集（**以清单为准收窄**，防包
// 自述扩张出未声明的域）。声明为空 → 返回 nil（不贡献域）。
//
// 这与 `SelectDomains`（编译期版本，从全量注册表按 want 收窄）语义同构，只是数据源是运行期 handler。
// 若声明的域在集合里没有（已被 CheckDeclaredDomains 判 FAILED），这里返回 error 让调用方显式处置
// （fail-closed，不静默跳过）。
func SelectRuntimeDomains(hs *RuntimeHandlerSet, declared []string) (map[string]mcp.Handler, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	if hs == nil {
		return nil, fmt.Errorf("apptool: 运行期包清单声明了域 %v 但没有可用 handler", declared)
	}
	out := make(map[string]mcp.Handler, len(declared))
	for _, d := range declared {
		h, ok := hs.Handlers[d]
		if !ok {
			return nil, fmt.Errorf("apptool: 运行期包 %q 声明的域 %q 在 host 自述里没有对应工具（工具面 FAILED）", hs.PackageID, d)
		}
		out[d] = h
	}
	return out, nil
}

// MergeDomains 把编译期域 handler 与运行期域 handler **并列**合并成一个 `map[domainKey]mcp.Handler`，
// 再交给 `NewAggregator`（复用 `platformmcp.New` 的前缀分派 + fail-closed 冲突裁决）。
//
// 冲突语义（复用 platformmcp）：编译期与运行期**同域前缀** = 撞车 → `platformmcp.New` fail-closed 报错
// （两个实现抢同一前缀会让路由静默指向其中一个，宁可构造失败暴露装配错误）。故本函数在合并前先做一次
// 显式的重复键检测，给出**更可读的错误**（点名哪个域既有编译期又有运行期 handler），随后仍交给
// platformmcp 兜底。
//
// 返回的 map 为 nil/空 → 调用方不应构造 Aggregator（空聚合器无注入意义，默认零注入）。
func MergeDomains(compiled map[string]mcp.Handler, runtime map[string]mcp.Handler) (map[string]mcp.Handler, error) {
	if len(compiled) == 0 && len(runtime) == 0 {
		return nil, nil
	}
	merged := make(map[string]mcp.Handler, len(compiled)+len(runtime))
	for k, v := range compiled {
		merged[k] = v
	}
	for k, v := range runtime {
		if _, dup := merged[k]; dup {
			return nil, fmt.Errorf("apptool: 工具域前缀 %q 同时被编译期 handler 与运行期包 handler 占用（fail-closed：拒绝静默覆盖）", k)
		}
		merged[k] = v
	}
	return merged, nil
}

// AggregateCompiledAndRuntime 一步到位：合并编译期域 + 运行期域 → 构造 Aggregator。
//
// 空域集合（编译期与运行期都空）→ 返回 (nil, nil)，调用方据此零注入（默认零注入不变量）。
// 任何冲突/空域键/nil handler 由 `MergeDomains` + `platformmcp.New` fail-closed 拒绝。
func AggregateCompiledAndRuntime(compiled map[string]mcp.Handler, runtime map[string]mcp.Handler) (*Aggregator, error) {
	merged, err := MergeDomains(compiled, runtime)
	if err != nil {
		return nil, err
	}
	if len(merged) == 0 {
		return nil, nil
	}
	return NewAggregator(merged)
}

// 编译期断言：runtimeDomainHandler 必须满足 mcp.Handler。
var _ mcp.Handler = (*runtimeDomainHandler)(nil)
