// Package apptool 把各 app 的**业务工具域**聚合成 MCP 工具面，供 `mate mcp app --apps=a,b` 拉起
// （设计 §5.3 a：app 业务工具走**独立的、逐 app 的注入通道**，绝不挂在 `mate mcp platform` 上）。
//
// 为什么要独立一个包而不是并进 platformmcp：
//   - `mate mcp platform` 是**一整条恒注入**的 stdio server，且"会话白名单不挡平台内置"——app 业务
//     工具若挂在它上面，就对所有会话恒定可见、单 app 停用无效（设计 §5.3 的评审修正点）。
//   - app 业务工具必须能被**会话 app scope**（`CapabilitySelection.Apps`）收窄、能被单 app 停用。
//     因此每个会话只按它启用的 app 集合，注入一条独立的 `mate mcp app --apps=...` stdio server。
//
// 本包**只做路由聚合**（复用 `platformmcp.Handler` 的前缀分派 + fail-closed 冲突裁决，不重写），
// 不碰业务、不 print。域键约束（单段、无下划线）与重复前缀的 fail-closed 全部由 `platformmcp.New`
// 保证——这正是"复用而非重写"的落点。
package apptool

import (
	"fmt"
	"sort"

	"github.com/morehao/ark-mate/internal/infra/platformmcp"
	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
)

// Aggregator 一个 app 工具面的聚合器。内部就是 `*platformmcp.Handler`（同一套前缀分派与
// fail-closed 裁决）——本类型只是给它一个语义更贴切的名字，并收口"至少一个域"的构造错误。
type Aggregator struct {
	handler *platformmcp.Handler
	// domains 聚合进来的域键（升序稳定；供诊断/测试断言注入集合）。
	domains []string
}

// NewAggregator 用 `map[domainKey]mcp.Handler` 构造聚合器。
//
// domainKey 必须**单段、无下划线**（它就是工具名的第一段，`platformmcp.New` 的硬约束，设计 §5.3）：
// 一个 app 的工具前缀若叫 `foo_create`，域键必须是 `foo`。重复域键、空域键、nil handler 一律由
// `platformmcp.New` fail-closed 拒绝（两个域抢同一前缀 → 路由会静默指向其中一个，宁可构造失败）。
//
// `domains` 为空时报错（与 platformmcp 一致）：一个空聚合器没有注入意义，构造它只会掩盖调用方
// 的 bug。调用方应先判"有没有域"，没有就不注入（默认零注入）。
func NewAggregator(domains map[string]mcp.Handler) (*Aggregator, error) {
	h, err := platformmcp.New(domains)
	if err != nil {
		return nil, fmt.Errorf("apptool: %w", err)
	}
	keys := make([]string, 0, len(domains))
	for k := range domains {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return &Aggregator{handler: h, domains: keys}, nil
}

// Handler 返回底层聚合 MCP handler（`mate mcp app` 子命令用它 serve）。
func (a *Aggregator) Handler() mcp.Handler {
	if a == nil {
		return nil
	}
	return a.handler
}

// Domains 聚合进来的域键（升序稳定）。
func (a *Aggregator) Domains() []string {
	if a == nil {
		return nil
	}
	return append([]string(nil), a.domains...)
}

// SelectDomains 从**全量域 handler 注册表**里挑出 `want` 这些域的子集（用于按会话 app scope 收窄）。
//
// 语义（fail-closed）：
//   - `want` 里出现注册表**没有**的域 → 报错（不静默丢弃：那会让"某个 app 的工具莫名消失"无处排查；
//     域 handler 由编译期登记，查不到就是装配/定义不一致，属程序错误）。
//   - 返回的 map 只含命中的域；调用方拿到空 map 就**不注入**（默认零注入）。
//
// 纯函数，不构造 Aggregator：装配期与注入期需要"先算出域集合、再决定注入/构造"。
func SelectDomains(all map[string]mcp.Handler, want []string) (map[string]mcp.Handler, error) {
	if len(want) == 0 {
		return nil, nil
	}
	out := make(map[string]mcp.Handler, len(want))
	for _, k := range want {
		h, ok := all[k]
		if !ok {
			return nil, fmt.Errorf("apptool: 域 %q 没有已登记的 handler（app 定义引用了未编入的工具域）", k)
		}
		out[k] = h
	}
	return out, nil
}
