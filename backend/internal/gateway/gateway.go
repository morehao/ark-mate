// Package gateway 是「远程 MCP 收进平台侧 stdio 网关」的**网关侧**实现（计划 M7）。
//
// 形态（计划的 P-1/P-2，先认清否则会照抄错模板）：底座按 ACP 规范在
// `session/new` 里收到一条 **stdio** MCP 条目，条目指向平台自己的可执行文件
// （`mate mcp bridge --agent <id> --runtime <id>`）；网关进程**只带 scope、不带 server 列表**，
// 自己打开本机 store 读该 scope 的能力定义、自己从库/凭据文档取密钥、再把工具面暴露给底座。
//
// 为什么这样切（三条都是硬理由，不是偏好）：
//  1. **密钥不出平台**：远程 MCP 的凭据只在网关进程内存里，不进底座可见面（env/argv/配置）；
//  2. **条目恒定 ⇒ 不换代际**：条目只由稳定的 (agent, runtime) 决定，增删一个远程 server
//     不改变注入项、也就不触发能力代际迁移（网关的头号收益，见计划的 P-2）；
//  3. **调用期策略**：每次 `tools/call` 按**当下**定义重查 —— 停用/删除一个 server 或一个
//     工具后调用立即失败，不等换代际、不等下一轮（"收窄即时生效"，P-4）。
//
// 分层：本包落 `internal/`（要读 `internal/store` 与凭据面），协议面在 `pkg/mcp`
// 的**服务端方向**（`mcp.Serve`）——C8 的 `pkg/` 不得 import `internal/` 是那条边界的由来。
//
// **C10（stdout 纯度）**：本包只往 `out` 写 JSON-RPC 帧，一切诊断走 Logger（网关的 Logger
// 由 CLI 固定为 stderr）。一行日志写错地方就会让底座的协议流错位。
package gateway

import (
	"context"
	"encoding/json"
	"io"

	"github.com/morehao/ark-mate/internal/version"
	"github.com/morehao/ark-mate/pkg/mcp"
	"github.com/morehao/golib/glog"
)

// Scope 一条网关进程服务的范围：**只由稳定的标识构成**（P-2）。
//
// 它同时也是"注入条目的全部信息"——所以条目里绝不能出现 server 列表、数量、启用集：
// 那些都会随定义变化，从而改变注入项、触发代际迁移。（唯一多出来的一项是会话**钉住的**
// 白名单 `McpsPinned`，它是用户的选定而不是定义集，见 `mcp.GatewayScope` 的注释。）
type Scope struct {
	RuntimeID string
	// McpsPinned 会话钉住的 MCP 白名单（nil = 未钉 → 全量）。
	McpsPinned *[]string
}

// Source 网关的数据面（M7-2 的实现：按 scope 取数 + 密钥就地解析 + 远端代理与连接池）。
//
// M7-1（本骨架）允许为 nil：此时工具面恒为空、调用一律"未知工具"。这是**计划内的过渡形态**
// ——网关在被注入之前（M7-3）对任何用户都不可达，所以"空面"不会出现在真实会话里；
// 但它必须**响亮**（每次 tools/list 记一条 WARN），否则将来接线失败会表现成"工具莫名不见了"。
type Source interface {
	// Tools 返回该 scope **当下**的全部工具（每次调用都重读定义与启用状态）。
	Tools(ctx context.Context) ([]mcp.Tool, error)
	// Call 代理一次工具调用。工具不在当下工具面里 → 返回 `mcp.ErrToolNotFound`。
	Call(ctx context.Context, tool string, args json.RawMessage) (mcp.CallResult, error)
}

// Options 网关运行参数。
type Options struct {
	Scope Scope
	// Source 数据面（nil = M7-1 的骨架形态，见 Source 注释）。
	Source Source
	// PageSize tools/list 每页条数（<=0 → mcp 包默认）。
	PageSize int
}

// Run 在 stdio 上跑网关，阻塞到 stdin EOF（父进程结束）或 ctx 取消。
//
// 返回值语义见 `mcp.Serve`：EOF → nil（正常退出路径，M7-5 的孙进程回收靠它）。
func Run(ctx context.Context, in io.Reader, out io.Writer, opts Options) error {
	if opts.Source == nil {
		glog.Warnw(ctx, "mcp bridge: 数据面未接线（M7-1 骨架形态）：工具面恒为空、调用一律未知工具",
			"runtime", opts.Scope.RuntimeID)
	}
	h := &handler{src: opts.Source, scope: opts.Scope}
	glog.Infow(ctx, "mcp bridge: 网关启动（scope 只由 runtime 决定 ⇒ 条目恒定、不换代际）",
		"runtime", opts.Scope.RuntimeID, "pid", pidOf())

	return mcp.Serve(ctx, in, out, mcp.ServerInfo{
		Name:         mcp.GatewayServerName,
		Version:      version.Version,
		Instructions: instructions(opts.Scope),
	}, h, mcp.ServerOptions{PageSize: opts.PageSize})
}

// handler 把 Source 接到 MCP 服务端的能力面上。
type handler struct {
	src   Source
	scope Scope

	warnedEmpty bool // "数据面未接线"只响一次，别把 stderr 刷爆
}

func (h *handler) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	if h.src == nil {
		if !h.warnedEmpty {
			h.warnedEmpty = true
			glog.Warnw(ctx, "mcp bridge: tools/list 时数据面仍为空（骨架形态；M7-2 接线后这里才该有工具）")
		}
		return nil, nil
	}
	tools, err := h.src.Tools(ctx)
	if err != nil {
		// 取数失败（store 打不开 / 定义全是坏的）：**回空面而不是炸会话**——网关是单点，
		// 炸掉整条会话的代价远大于"这一轮没有工具"。错误如实进日志（M7-4 的可观测面）。
		glog.Errorw(ctx, "mcp bridge: 取工具面失败，本轮回空面", "err", err)
		return nil, nil
	}
	glog.Debugw(ctx, "mcp bridge: tools/list", "tools", len(tools))
	return tools, nil
}

func (h *handler) CallTool(ctx context.Context, tool string, args json.RawMessage) (mcp.CallResult, error) {
	if h.src == nil {
		return mcp.CallResult{}, mcp.ErrToolNotFound
	}
	// 每次调用都走 Source：数据面自己承担"按当下定义重查 + 密钥重解析 + 远端代理"（P-4）。
	return h.src.Call(ctx, tool, args)
}

// instructions 给模型的一段说明：网关把多个 server 的工具汇到一条 stdio 连接上，
// 名字带 `<serverID>__` 前缀是**唯一**能分辨来源的地方（P-3 的命名空间）。
func instructions(s Scope) string {
	// 不用尖括号：`json.Marshal` 会把 `<` `>` 转义成 `\u003c`，模型读起来反而更差。
	return "本 server 是 ark-mate 平台的 MCP 网关：它把该 runtime 作用域下的远程 MCP server " +
		"汇聚成一条 stdio 连接。工具名形如 serverID__toolName（分隔符是两个下划线），前缀即来源 server。" +
		"作用域：runtime=" + s.RuntimeID + "。"
}
