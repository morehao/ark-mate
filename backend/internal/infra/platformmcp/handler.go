// Package platformmcp 把平台各能力域（automation / expert [/ material]）聚合为**一个** MCP 工具面，
// 供 `mate mcp platform` 拉起（见 internal/bootstrap/maintenance/platform_mcp.go）。
//
// 为什么聚合而不是每域一个 server：一个 server 进程 = 一条 ACP `mcpServers` 注入 + 一处契约快照 +
// 一处 stdout 纪律；新增一域只是往 map 加一项，不必再动注入表（`sessionruntime/platform_mcp.go`）。
//
// 分派规则：工具名自带**域前缀**（`automation_create` / `expert_list`），按首个下划线前的段分派。
// 因此各域工具名必须在自己的前缀下（`expert_set_enabled` 的前缀是 `expert`，不是 `expert_set`）。
//
// 边界：本包只做路由聚合，**不碰任何业务**，也不 print——stdout 属于协议（见 mcp.Serve 纪律）。
package platformmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
)

// Handler 聚合多个域的 MCP handler：ListTools 合并，CallTool 按域前缀分派。
type Handler struct {
	domains map[string]mcp.Handler
	// order 稳定的域前缀顺序（ListTools 输出确定性；map 遍历本身无序）。
	order []string
}

// New 构造聚合器。key 是**域前缀**（工具名的第一段，如 "automation"/"expert"）。
//
// 重复 key 直接报错（fail-closed）：两个域抢同一前缀会让路由静默指向其中一个。
func New(domains map[string]mcp.Handler) (*Handler, error) {
	if len(domains) == 0 {
		return nil, fmt.Errorf("platformmcp: 至少需要一个域")
	}
	order := make([]string, 0, len(domains))
	for k, d := range domains {
		if strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("platformmcp: 域前缀不能为空")
		}
		if strings.Contains(k, "_") {
			return nil, fmt.Errorf("platformmcp: 域前缀 %q 不能含下划线（它就是工具名的第一段）", k)
		}
		if d == nil {
			return nil, fmt.Errorf("platformmcp: 域 %q 的 handler 为 nil", k)
		}
		order = append(order, k)
	}
	sort.Strings(order)
	return &Handler{domains: domains, order: order}, nil
}

// ListTools 合并所有域的工具（顺序按域前缀字典序，域内保持各域自报顺序）。
func (h *Handler) ListTools(ctx context.Context) ([]protocol.McpTool, error) {
	out := make([]protocol.McpTool, 0, 16)
	for _, k := range h.order {
		ts, err := h.domains[k].ListTools(ctx)
		if err != nil {
			return nil, fmt.Errorf("platformmcp: 域 %q 列工具失败: %w", k, err)
		}
		out = append(out, ts...)
	}
	return out, nil
}

// CallTool 按工具名的域前缀分派；前缀不存在 → mcp.ErrToolNotFound（协议层错误）。
func (h *Handler) CallTool(ctx context.Context, name string, args json.RawMessage) (protocol.McpCallResult, error) {
	prefix := name
	if i := strings.IndexByte(name, '_'); i >= 0 {
		prefix = name[:i]
	}
	d, ok := h.domains[prefix]
	if !ok {
		return protocol.McpCallResult{}, mcp.ErrToolNotFound
	}
	return d.CallTool(ctx, name, args)
}
