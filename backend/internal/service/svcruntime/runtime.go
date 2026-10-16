// Package svcruntime 定义**底座身份与诊断**面的服务端口（ADR-2：provider/模型目录已拆出）。
//
// **没有 runtime 概览**：底座由构建期固定（backend/pkg/runtime/build.go），用户面不存在
// "列出有哪些底座"的读面（`GET /runtimes` 已随 agent 候选面一并下线）。
package svcruntime

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
)

// RuntimeSvc 底座身份与诊断面的服务端口。
//
// 它只剩两个方法，是**刻意的**：这个包管的是"这个二进制认哪个底座、它此刻怎么样"，
// 而档案与模型清单各自有主人（`svcprovider` / `svcmodel`）。
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
