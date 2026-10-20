package runtimeconn

import (
	"context"
	"github.com/morehao/ark-mate/internal/core/code"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// ——— 连接**拨号**（P4a 正文第五十七刀：从 `internal/manager` 搬入）———
//
// 取键 → 复用活连接 / 代际准入 → spawn+握手 → 登记进池，这一整条是**执行面**的事：
// 键怎么算、连接怎么验活、子进程怎么登记都只在本包说话。编排侧只交两样东西：
//
//	① 三个 ACP 回调（事件出口 / 权限 / 提问）——它们是会话/交互的事实；
//	② 一个凭据检查（默认模型所在档案缺失 → 显式 credential_missing）。
//
// 拨号用「脱离调用方 ctx」的上下文：子进程以 exec.CommandContext 启动，若绑定请求 ctx，
// 请求返回即被杀（暖启动后立刻断连）。WithoutCancel 后 `runtime.Connect` 自带的默认握手限时
// 仍保证拨号有界；连接生命周期归执行面（watch/Close），与任一请求无关。

// DialOptions 一次拨号所需的编排侧事实。
type DialOptions struct {
	// Fingerprint 该会话钉住的能力指纹（空 = 缺省代际）。
	Fingerprint string
	// NativeMode 该会话钉住的原生权限模式（空 = 不注入）。
	NativeMode string
	// CheckCredentials 凭据面校验（nil = 跳过；离线/测试路径）。
	CheckCredentials func(ctx context.Context) error
	// ClientOptions ACP 的四个回调（OnEvent / RequestPermission / CreateElicitation /
	// CompleteElicitation）。它是 `session.DialOptions.Hooks` 的**具体形态**：域只做不透明
	// 转发，断言与转换在这一层（类型不符即明确报错，不静默忽略）。
	ClientOptions acp.ClientOptions
}

// Dial 取（或拨）一条 runtime 连接：活着复用，断开则作废重拨；并挂断连监听。
//
// fingerprint 空值走的是代际键的原样返回分支，所以缺省路径与"代际机制之前"完全等价。
func (pl *Pool) Dial(ctx context.Context, spec runtime.Spec, opt session.DialOptions) (session.Conn, error) {
	// 凭据面先校验（P2）：**默认模型所在的那条档案**缺失/无 key 属配置错误 → 显式
	// credential_missing（文案含出路），不静默以「无密钥」姿态起进程。放 Dial 内可一处
	// 覆盖两条入口：新建会话、按需恢复。
	if opt.CheckCredentials != nil {
		if err := opt.CheckCredentials(ctx); err != nil {
			return nil, err
		}
	}
	// 键由池一次算完：基础键（进程级能力面）、下发给子进程的 env、配置指纹与代际键
	// **同源**——分开取会得到"键说共享、注入的却是另一套 env"这种静默分叉。
	// 模型不进 env/缓存键：模型选择是**会话级**语义（session/set_config_option），
	// 同一连接可服务多个模型。
	//
	// 第三维是**原生权限模式**（P1-f 下半）：模式只能以进程 env 下发，所以它必须进基础键——
	// 不进键的表现是"后建的会话悄悄跑在先进那个进程的模式里"。
	keys := pl.KeyFor(spec, opt.Fingerprint, opt.NativeMode)

	if rc := pl.Get(keys.Key); rc != nil {
		if rc.client == nil {
			return rc, nil // 测试桩：没有句柄可验活，按登记事实返回
		}
		select {
		case <-rc.client.Done():
			// 旧连接已断开（Drop 已/将清理）：落到下方重拨
		default:
			return rc, nil
		}
	}
	// 代际上限准入（M3-7）：**新建之前**判定。超限就带码拒绝，绝不"复用一个差不多"的能力集。
	if err := pl.AdmitGeneration(keys.BaseKey); err != nil {
		return nil, err
	}

	// hooks 是域**不透明转发**过来的回调集合；在这里断言成协议形态（唯一一处）。
	hooks, ok := opt.Hooks.(acp.ClientOptions)
	if !ok {
		return nil, code.NewError(code.ClassConnection, "dial_hooks_invalid",
			"拨号回调集合的类型不符（装配侧交下来的不是执行面的 hooks 形态）", nil)
	}
	args := resolveArgs(spec.Launch.Args, "")
	// spawn + 握手由 runtime.Connect 承载（可执行投影的拨号底座）。
	client, err := runtime.Connect(context.WithoutCancel(ctx), spec, args, keys.Env, hooks)
	if err != nil {
		return nil, err
	}
	// §16.6/§17.4 开放项实测：握手能力断言留档日志（观测用，无行为变化）——
	// 底座调研/诊断依赖（sessionCapabilities resume/list/fork/close、loadSession 声明）。
	// INFO 级：每 runtime 连接仅一次，serve 默认级别可见。
	caps := client.Capabilities()
	sc := caps.Raw.SessionCapabilities
	glog.Infow(ctx, "runtime 握手能力",
		"runtime", spec.ID,
		"sessions_persistent", caps.SessionsPersistent,
		"multi_session_concurrent", caps.MultiSessionConcurrent,
		"load_session", caps.Raw.LoadSession,
		"resume", sc.Resume != nil,
		"list", sc.List != nil,
		"fork", sc.Fork != nil,
		"close", sc.Close != nil)

	rc := &Conn{client: client, spec: spec, key: keys.Key, baseKey: keys.BaseKey,
		configDigest: keys.ConfigDigest, Started: time.Now()}
	pl.Add(rc) // 登记子进程 + 状态置 running + 入表 + 起断连监听
	return rc, nil
}

// resolveArgs 替换 Launch.Args 中的 {config} 占位（保留给自定义 adapter 模板；
// 当前内置 runtime 的 Args 均已不含占位，此函数为幂等空操作）。
func resolveArgs(args []string, configPath string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, "{config}", configPath)
	}
	return out
}
