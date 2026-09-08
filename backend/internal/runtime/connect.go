package runtime

import (
	"context"
	"log/slog"

	"github.com/morehao/ark-forge/internal/acl"
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
	all := append([]string(nil), spec.Launch.Env...)
	all = append(all, env...)
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	t := acl.NewStdioTransport(spec.Launch.Command, args, all)
	t.SetLogger(logger)

	client, err := acl.Dial(context.WithoutCancel(ctx), t, opts)
	if err != nil {
		_ = t.Close()
		return nil, err
	}
	return client, nil
}
