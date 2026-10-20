package sessionruntime

import (
	"context"
	"errors"
	"github.com/morehao/ark-mate/internal/core/code"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 未注入执行面时的缺省端口（阶段 0.4b）———
//
// `NewService` 不再自造 `*runtimeconn.Plane/Pool`：那会让 L7 直接 import L6 的具体适配器，
// 阶段 0.6 的 `deny runtimeconn` 不成立。执行面由**装配根**构造并注入（见
// `bootstrap/assembly/build.go`），来源登记也在那边。
//
// 但 130+ 处测试与 `mate demo` 只想要"一个能跑的 Service"，其中大多数**根本不拨号**
// （纯列表/纯状态/纯定义）。它们拿到的是下面两个安全缺省：
//   - 不拨号、不记账、不回收（方法体为空）；
//   - 真要取连接时返回一个**明确的错误**而不是 panic —— 失效方式从"运行时空指针"降级成
//     "一条可读的装配错误"。
//
// 需要真执行面的夹具（`newRig` 等）显式注入真实端口；这条边界与 `WithPlanes` 的分工一致。

// errRuntimeNotWired 执行面未注入（只应出现在纯列表/纯状态路径上）。
var errRuntimeNotWired = errors.New("执行面未装配：连接池/运行时平面由装配根注入（纯列表/纯状态用例无需执行面）")

// nopRuntimePlane `session.RuntimePlane` 的安全缺省。
type nopRuntimePlane struct{}

func (nopRuntimePlane) RecordRuntimeError(string, *code.RuntimeError, []string) {}

func (nopRuntimePlane) NoteScopeAmbiguous(string) {}

func (nopRuntimePlane) SelectedSpec() (runtime.Spec, error) {
	return runtime.Spec{}, errRuntimeNotWired
}

func (nopRuntimePlane) SetState(string) {}

func (nopRuntimePlane) SampleConfigOptions(context.Context, session.Conn, string, string) ([]runtime.ConfigOption, error) {
	return nil, errRuntimeNotWired
}

// nopConnPool `session.ConnPool` 的安全缺省。
type nopConnPool struct{}

func (nopConnPool) Dial(context.Context, runtime.Spec, session.DialOptions) (session.Conn, error) {
	return nil, errRuntimeNotWired
}

func (nopConnPool) KeyFor(runtime.Spec, string, string) session.KeyInputs {
	return session.KeyInputs{}
}

func (nopConnPool) ConnOfClient(*acp.Client) session.Conn { return nil }

func (nopConnPool) ConnForRuntime(string) session.Conn { return nil }

func (nopConnPool) ReclaimGeneration(session.Conn) bool { return false }

func (nopConnPool) CloseAll() {}

func (nopConnPool) SetDropSink(session.DropSink) {}

func (nopConnPool) SetGenerationLimit(int) {}

func (nopConnPool) StartIdleReaper(time.Duration, time.Duration) {}
