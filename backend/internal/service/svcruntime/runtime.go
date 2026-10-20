// Package svcruntime 定义**底座身份与诊断**面的服务端口（ADR-2：provider/模型目录已拆出）。
//
// **没有 runtime 概览**：底座由构建期固定（backend/pkg/runtime/build.go），用户面不存在
// "列出有哪些底座"的读面（`GET /runtimes` 已随 agent 候选面一并下线）。
package svcruntime

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
)

// Plane 执行面（`internal/drivers/runtimeconn`）——**这两个方法名是执行面的口语，不是 API 的口语**：
// 执行面说"身份/诊断"，对外契约说 `RuntimeIdentity`/`RuntimeDiagnostics`（路径 `/runtime/...`）。
// 命名适配是这一层存在的理由（它是 API 面的一部分），所以这里刻意不做成 getter + 双层转发。
type Plane interface {
	Identity(ctx context.Context) dtoruntime.IdentityResp
	Diagnostics(ctx context.Context) (*dtoruntime.DiagnosticsResp, error)
}

// Runtime 底座身份与诊断面的用例实现（P4a：实现从 `manager` 收进这里）。
type Runtime struct{ plane Plane }

// New 造一个底座面用例（`plane` 由装配层交出）。
func New(plane Plane) *Runtime { return &Runtime{plane: plane} }

var _ RuntimeSvc = (*Runtime)(nil)

// RuntimeIdentity 见 `svcruntime.RuntimeSvc`。
func (r *Runtime) RuntimeIdentity(ctx context.Context) dtoruntime.IdentityResp {
	return r.plane.Identity(ctx)
}

// RuntimeDiagnostics 见 `svcruntime.RuntimeSvc`。
func (r *Runtime) RuntimeDiagnostics(ctx context.Context) (*dtoruntime.DiagnosticsResp, error) {
	return r.plane.Diagnostics(ctx)
}

// RuntimeSvc 底座身份与诊断面的服务端口。
//
// 它只剩两个方法，是**刻意的**：这个包管的是"这个二进制认哪个底座、它此刻怎么样"，
// 而档案与模型清单都归 `svcmodel`（2026-10-20 合并前分属两个包）。
//
// 实现方以编译期断言 var _ RuntimeSvc = (*T)(nil) 保证一致性。
type RuntimeSvc interface {
	// RuntimeIdentity 构建期固定的底座身份（`/meta` 的 `runtime` 段：id/name/version）。
	//
	// 它是**这个二进制的属性**（`-X pkg/runtime.BuildRuntime=<id>`，不注入 = dsh），所以没有参数、
	// 也不会失败：id 恒为 `runtime.SelectedRuntime()`，name 取注册表里的展示名（取不到回落 id），
	// version 取不到即空串（核心不探测本机安装）。
	RuntimeIdentity(ctx context.Context) dtoruntime.IdentityResp
	// RuntimeDiagnostics 返回 `GET /runtime/diagnostics` 的读面：底座**此刻**的事实
	// （进程状态、握手结论、stderr 尾巴、最后一条错误与补救指引）。
	//
	// v19：它原是 `GET /agents/:id/diagnostics`，agent 概念并入 runtime 后折回这里——
	// 底座只有一个，路径里再带一个 id 只会暗示"可以选"。**只读**：不主动拨号。
	RuntimeDiagnostics(ctx context.Context) (*dtoruntime.DiagnosticsResp, error)
}
