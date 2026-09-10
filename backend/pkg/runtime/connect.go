package runtime

import (
	"context"
	"log/slog"
	"strings"

	"github.com/morehao/ark-forge/pkg/acl"
)

// Connect 依投影 Spec 拉起 ACP server 子进程（stdio）并完成 initialize 握手。
// 这是「可执行半场」的拨号底座：Runtime 接口（§4.2）落地时各投影复用同一拉起/握手路径，
// 决策钩子（权限/elicitation/事件归集）仍由调用方经 ClientOptions 注入。
//
// 参数：
//   - args：已解析启动参数（调用方先做 {config} 替换；现三投影参数均无该 token）；
//   - env：调用方附加环境（如 provider 密钥注入），内部叠加在 spec.Launch.Env 之后；
//   - opts.Logger：若设置，同源用于 stdio 传输日志（runtime/cmd 属性由调用方预置）。
//
// 连接生命周期归调用方（编排器连接回收/Close），与任一请求 ctx 无关（拨号用脱离 ctx 的
// 派生 context，语义同 orchestrator.acquire——请求返回不杀已起子进程）。
func Connect(ctx context.Context, spec Spec, args []string, env []string, opts acl.ClientOptions) (*acl.Client, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	var t acl.Transport
	switch strings.ToLower(strings.TrimSpace(spec.Launch.Transport)) {
	case "", "stdio":
		all := append([]string(nil), spec.Launch.Env...)
		all = append(all, env...)
		st := acl.NewStdioTransport(spec.Launch.Command, args, all)
		st.SetLogger(logger)
		t = st
	case "http", "ws", "websocket", "streamable_http", "streamable-http":
		// 远程 runtime：不拉子进程，直接连远端 ACP 端点（A5）。
		rt, err := acl.NewRemoteTransport(acl.RemoteSpec{
			Transport: spec.Launch.Transport,
			Endpoint:  spec.Launch.Endpoint,
			Headers:   spec.Launch.Headers,
		})
		if err != nil {
			return nil, err
		}
		if ls, ok := acl.Transport(rt).(acl.LoggerSetter); ok {
			ls.SetLogger(logger)
		}
		t = rt
	default:
		return nil, acl.NewError(acl.ClassUnsupported, "unsupported_transport",
			"runtime "+spec.ID+" 声明了不支持的传输 "+spec.Launch.Transport, nil)
	}

	client, err := acl.Dial(context.WithoutCancel(ctx), t, opts)
	if err != nil {
		_ = t.Close()
		return nil, err
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
		Notes:             "远程 runtime：由 .forge/config/runtimes.json 声明（A5）",
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
