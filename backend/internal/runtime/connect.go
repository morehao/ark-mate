package runtime

import (
	"context"
	"strings"

	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/infra/acp"
)

// Connect 依投影 Spec 拉起 ACP server 子进程（stdio）并完成 initialize 握手。
// 这是「可执行半场」的拨号底座：Runtime 接口（§4.2）落地时各投影复用同一拉起/握手路径，
// 决策钩子（权限/elicitation/事件归集）仍由调用方经 ClientOptions 注入。
//
// 参数：
//   - args：已解析启动参数（调用方先做 {config} 替换；现三投影参数均无该 token）；
//   - env：调用方附加环境（如 provider 密钥注入），内部叠加在 spec.Launch.Env 之后；
//
// LaunchesLocally 报告该 runtime 是否由平台**在本机拉起子进程**（`Launch.Transport` 为空或
// `stdio`）。判据与 `Connect` 的传输分派**同源**（同一个 switch 的两支）：凡是本机起进程的形态
// → true，远程端点形态（http/ws/streamable_http…）→ false。
//
// 为什么需要它：平台侧 MCP 网关条目是一条**本机 stdio 命令**（`<平台可执行文件> mcp bridge …`）。
// 把它下发给远程 ACP 端点，等于让对端按我们机器上的路径去起进程——对端根本没有那个文件。
func LaunchesLocally(spec Spec) bool {
	switch strings.ToLower(strings.TrimSpace(spec.Launch.Transport)) {
	case "", "stdio":
		return true
	default:
		return false
	}
}

// 连接生命周期归调用方（编排器连接回收/Close），与任一请求 ctx 无关（拨号用脱离 ctx 的
// 派生 context，语义同 orchestrator.acquire——请求返回不杀已起子进程）。
func Connect(ctx context.Context, spec Spec, args []string, env []string, opts acp.ClientOptions) (*acp.Client, error) {
	var t acp.Transport
	switch strings.ToLower(strings.TrimSpace(spec.Launch.Transport)) {
	case "", "stdio":
		// 合并顺序＝优先级：`spec.Launch.Env` 是**平台对该 runtime 的声明**（含设计 §6.6 的配置隔离，
		// M3-3），调用方附加项（provider 密钥 / 端点）是**每次连接**的增量。`mergeEnv` 的语义正是
		// "base 已有的键不被 add 覆盖"，所以平台声明赢——否则一个 provider 档案里顺手写的
		// `XDG_CONFIG_HOME` 就能把隔离改掉，而这类覆盖不会有任何提示。
		// 去重还有一个更硬的理由：同名两条在 POSIX `getenv`（取第一条）与 Node `process.env`（取最后一条）
		// 下语义相反，留着就是"同一个环境在两种子进程里行为不同"。
		all := connectEnv(spec, env)
		t = acp.NewStdioTransport(spec.Launch.Command, args, all)
	case "http", "ws", "websocket", "streamable_http", "streamable-http":
		// 远程 runtime：不拉子进程，直接连远端 ACP 端点（A5）。
		rt, err := acp.NewRemoteTransport(acp.RemoteSpec{
			Transport: spec.Launch.Transport,
			Endpoint:  spec.Launch.Endpoint,
			Headers:   spec.Launch.Headers,
		})
		if err != nil {
			return nil, err
		}
		t = rt
	default:
		return nil, code.NewError(code.ClassUnsupported, "unsupported_transport",
			"runtime "+spec.ID+" 声明了不支持的传输 "+spec.Launch.Transport, nil)
	}

	client, err := acp.Dial(context.WithoutCancel(ctx), t, opts)
	if err != nil {
		_ = t.Close()
		// 握手/拉起失败是本包交给上层的错误出口：协议错误在这里折成平台视图
		// （分类、稳定 code、stderr 诊断尾巴一并带走，见 FoldError）。
		return nil, FoldError(err)
	}
	return client, nil
}

// RemoteSpec 构造一条「远程 ACP 端点」runtime 的投影 Spec（A5）。
//
// 与内置 adapter 的区别：不拉本地子进程，Launch.Transport/Endpoint 直接描述远端端点；
// 其余能力（Caps/ManagesOwnContext 等）取保守基线——真实能力仍以 initialize 探测为准。
func RemoteSpec(id, name, transport, endpoint string, headers map[string]string) Spec {
	tr := strings.ToLower(strings.TrimSpace(transport))
	if tr == "" {
		tr = "ws"
	}
	label := "remote " + tr
	return Spec{
		ID:      id,
		Name:    firstNonEmptyStr(name, id),
		Version: label + " " + endpoint,
		Launch: LaunchSpec{
			Transport: tr,
			Endpoint:  endpoint,
			Headers:   headers,
		},
		Description:       "远程 ACP 端点（" + tr + "：" + endpoint + "）",
		McpTransports:     []string{"stdio"}, // MCP 注入面由远端 agent 决定，平台侧保守声明
		ManagesOwnContext: true,
		Notes:             "远程 runtime：由 .ark-mate/config/runtimes.json 声明（A5）",
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// connectEnv 交给 acp 的最终附加环境：**平台声明（spec.Launch.Env）优先于调用方增量**。
//
// 单独提出来是为了能直接单测这条优先级——它是"配置隔离不会被 provider 档案悄悄改掉"（M3-3）
// 的唯一保证点，而 `Connect` 本身要拉真进程、没法在单测里断言 cmd.Env。
func connectEnv(spec Spec, env []string) []string {
	return mergeEnv(spec.Launch.Env, env)
}
