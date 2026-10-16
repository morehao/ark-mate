package manager

import (
	"github.com/morehao/ark-mate/pkg/runtime"

	"github.com/morehao/ark-mate/internal/runtimeconn"
)

// ——— P3a-2：执行面（底座绑定）的过渡期转发 ———
//
// `runtimeBinding` 与它的读面（快照 / 进程状态 / 选定的 spec / 身份）已落
// `internal/runtimeconn`（P3a-2 第一刀）。manager 侧只剩一行转发。
//
// **P4a 正文第二刀**（2026-10-16）：`RuntimeIdentity` 的转发**已删**——底座面的用例实现
// 搬进了 `internal/service/svcruntime`（`Runtime` + `Plane` 端口）。P4a 收口后 `router.Ports.Runtime`
// 交的是**执行面本身**（`*runtimeconn.Plane`），用例实例由 `ctrruntime` 造。本文件只剩执行面自身
// 的转发（快照/状态/spec 解析）。
//
// **顺手清掉了五个死符号**（搬完才对得上账）：`bindingLocked`（零调用点）、
// `requireRuntime` + `runtimeByID`（只被彼此调用，外面无人用——`SelectedSpec` 已经把
// "构建期选定那个没注册"报得更准）、`runtimeCtx` + `runtimeRequestTimeout`（零调用点）。
// 它们与 §3.1.47 清掉的 `Service.inputs` 是同一类："编译全绿、看起来还在用"。

// runtimeBinding 是域里那份绑定的**类型别名**（全仓只有域里那一份定义）。
type runtimeBinding = runtimeconn.Binding

// TODO(P4b): 删转发（调用点直接依赖 runtimeconn.Plane）
func (s *Service) runtimeBindingSnapshot() runtimeBinding { return s.runtimePlane.Snapshot() }

// TODO(P4b): 删转发（同上）
func (s *Service) setRuntimeState(state string) { s.runtimePlane.SetState(state) }

// TODO(P4b): 删转发（同上）
func (s *Service) ResolveRuntime() (string, error) { return s.runtimePlane.Resolve() }

// TODO(P4b): 删转发（同上）
func (s *Service) selectedSpec() (runtimeSpec, error) { return s.runtimePlane.SelectedSpec() }

// runtimeSpec 是 `pkg/runtime.Spec` 的别名视图（`selectedSpec` 的返回类型；
// 调用点原本就直接用 `runtime.Spec`，这里只为让转发签名读起来短一点）。
type runtimeSpec = runtime.Spec
