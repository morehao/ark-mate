package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/workspace"
)

// ——— P2b：工作空间域（策略 / 校验 / 解析）的过渡期转发与别名 ———
//
// 纯函数（`ValidateWorkspace` / `ResolveWorkspace` / `DiscardWorkspace` / `withinRoots` 那一族）
// 与**起始点偏好策略**（代建根 + 生效名单，`workspace.Plane` 自持锁）已落 `domain/workspace`
// （P2b 第一刀）。
//
// 为什么用**别名**而不是让调用点直接改指域：`WorkspaceResolution` / `DefaultWorkspaceRef` 与那
// 三个纯函数在 `internal/router` 的测试里直接用（`manager.ResolveWorkspace` /
// `manager.DefaultWorkspaceRef`），别名让那些点一个字都不用改，而"两份定义"这种真正危险的
// 状态也不会出现——别名与域里的类型**是同一个类型**。
//
// 策略状态**不再挂在 `Service` 上**（它此前与 `sessions` 共用 `Service.mu`）：现在只有
// `Service.planner` 一个持有者，读写都经它自己的锁。P2b 前置（§3.1.49）已经把"持 manager 锁
// 请求会话快照"清零，所以那两把锁之间没有互指的边。

// WorkspaceResolution 是域里那份解析结果的**类型别名**。
type WorkspaceResolution = workspace.WorkspaceResolution

// DefaultWorkspaceRef 同上。
type DefaultWorkspaceRef = workspace.DefaultWorkspaceRef

// TODO(P4b): 删转发（调用点直接依赖 workspace 包）
func ValidateWorkspace(spec string) (string, error) { return workspace.ValidateWorkspace(spec) }

// TODO(P4b): 删转发（调用点直接依赖 workspace 包）
func ResolveWorkspace(spec string, def *DefaultWorkspaceRef) (WorkspaceResolution, error) {
	return workspace.ResolveWorkspace(spec, def)
}

// TODO(P4b): 删转发（调用点直接依赖 workspace 包）
func DiscardWorkspace(res WorkspaceResolution) { workspace.DiscardWorkspace(res) }

// planner 工作空间域（装配期构造；策略状态在它自己的锁下）。
//
// 它是 `Service` 上**唯一**的工作空间策略持有者：字段不再复刻一份，否则"域写一份、manager 读
// 另一份"就是第二个事实源（而表现是面板改了名单、校验却按旧的走——静默）。
var _ = context.Background
