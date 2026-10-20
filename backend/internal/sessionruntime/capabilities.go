package sessionruntime

import (
	"context"
	"github.com/morehao/ark-mate/internal/runtime"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/golib/glog"
)

// ——— 能力面的**注入适配**（P5a 之后本文件只剩这一半）———
//
// 能力定义的装配与装载（`LoadCapabilityDefinitions*` / `ReloadCapabilityDefinitions` /
// `RegisterTool`）已随 P5a 归 `internal/drivers/registry.Planes`：那三件事只碰注册表、定义机制与库，
// 没有一样来自会话/编排，而网关子进程也要同一份实现。
//
// 留在本文件的是**注入适配**：把注册表里的定义折成 `acp.McpServer`（解析密钥、拼网关条目）。
// 它要读进程自己的可执行文件路径与凭据 cwd，属于编排侧的事实。

// McpServersFor 选出可注入某 runtime 的 MCP server 子集（P2 §5.2/§5.3）：
// **stdio 直连** + （有远程 server 时）**一条平台网关条目**（M7-3 的分派）。
//
// 老口径（M7 之前）是"注册表全量 → 该 runtime 的 McpTransports 过滤 → 白名单"，远程 server 由
// 底座自己按传输直连；现在远程一律经网关（P-2），故这里只保留直连那一半，条目的合成在
// `planMcpInjection`（唯一实现，指纹也用它——两处各写一遍会出现"指纹说含 X、注入里没有 X"）。
//
// `sel` 是**会话钉住的能力选定**：新建会话时来自本次请求，恢复会话时来自
// `sessions.capability_json`。两处调用点都必须传它——恢复路径漏传的后果是
// 会话重启后注入集悄悄变回全量默认（用户看不到任何提示）。
//
// v19 起没有"agent 白名单"这一层：会话没钉就是全量（按 runtime transport 过滤）。
func (s *Service) McpServersFor(runtimeID string, sel *session.CapabilitySelection) []runtime.McpServer {
	plan := s.planMcpInjection(runtimeID, sel)
	out := make([]runtime.McpServer, 0, len(plan.Direct)+1)
	for _, m := range plan.Direct {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		// §13.3 M1/M6：密钥型绑定在这里**才**解析成明文（定义文件里只有引用）。
		env, headers, problems := s.facts.McpSecrets.Resolve(m)
		if len(problems) > 0 {
			// 解析不了就不注入这个 server：半配置的 MCP server 会让 agent 侧报一堆看不懂的连不上，
			// 而少一个工具是"看得见的降级"（日志里有原因、有引用、有出路）。**不静默**：逐条 WARN。
			glog.Warnw(context.Background(), "mcp 注入跳过：密钥引用解析失败", "server", m.ID, "problems", problems)
			continue
		}
		out = append(out, runtime.McpServer{
			Name: name, Transport: m.Transport,
			Command: m.Command, Args: m.Args, URL: m.URL,
			Env: env, Headers: headers,
		})
	}
	if plan.Gateway != nil {
		// 网关条目天生无密钥（只有 scope），故直接追加；远程凭据留在网关进程里（§10.6-②）。
		gw := runtime.McpServersOfACP([]acp.McpServer{*plan.Gateway})
		out = append(out, gw...)
		if len(plan.Remote) > 0 {
			glog.Debugw(context.Background(), "mcp 注入：远程 server 经平台网关代连",
				"runtime", runtimeID, "remote", len(plan.Remote))
		}
	}
	return out
}
