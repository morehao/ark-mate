package runtimeconn

import (
	"context"
	"strings"

	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/infra/protocol/acp"
	"github.com/morehao/ark-mate/internal/runtime"
)

// 连接（拨号）是**执行面的事**：拉子进程、连远端端点、跑 initialize 握手，全部是"跟外面说话"。
// 2026-10-21（P3-d）从 `internal/runtime` 搬到这里——契约包只留 `Spec`/`Adapter`/注册表/隔离投影。
// 连接生命周期归调用方（编排器连接回收/Close），与任一请求 ctx 无关（拨号用脱离 ctx 的
// 派生 context，语义同 orchestrator.acquire——请求返回不杀已起子进程）。
func connect(ctx context.Context, spec runtime.Spec, args []string, env []string, opts acp.ClientOptions) (*acp.Client, error) {
	var t acp.Transport
	switch strings.ToLower(strings.TrimSpace(spec.Launch.Transport)) {
	case "", "stdio":
		// 合并顺序＝优先级：`spec.Launch.Env` 是**平台对该 runtime 的声明**（含设计 §6.6 的配置隔离，
		// M3-3），调用方附加项（provider 密钥 / 端点）是**每次连接**的增量。`mergeEnv` 的语义正是
		// "base 已有的键不被 add 覆盖"，所以平台声明赢——否则一个 provider 档案里顺手写的
		// `XDG_CONFIG_HOME` 就能把隔离改掉，而这类覆盖不会有任何提示。
		// 去重还有一个更硬的理由：同名两条在 POSIX `getenv`（取第一条）与 Node `process.env`（取最后一条）
		// 下语义相反，留着就是"同一个环境在两种子进程里行为不同"。
		all := runtime.ConnectEnv(spec, env)
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
		return nil, failure.NewError(failure.ClassUnsupported, "unsupported_transport",
			"runtime "+spec.ID+" 声明了不支持的传输 "+spec.Launch.Transport, nil)
	}

	client, err := acp.Dial(context.WithoutCancel(ctx), t, opts)
	if err != nil {
		_ = t.Close()
		return nil, err
	}
	return client, nil
}
