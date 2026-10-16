package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/workspace"

	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
	"github.com/morehao/ark-mate/internal/model"
)

// ——— P2b：工作空间登记表的过渡期转发与别名（最后一刀） ———
//
// 登记表 CRUD、归属清理、代建默认工作空间（含 memory 模式的进程内那一份）已落
// `domain/workspace/registry.go`。manager 侧只剩一行转发——`Service` 上**不再有任何**
// 工作空间字段（策略、库句柄、默认工作空间、持久化端口全归域）。
//
// 会话域的回显端口（`SetWorkspaceDisplay`）也改指域：它在 `NewService` 里接的是
// `planner.WorkspaceDisplayOf`——那是同一个"用 id 换展示名"的判定，两处各写一遍会让
// 左栏分组名与读面 title 各说各话。

// TODO(P4b): 删转发（同上）
func (s *Service) CreateRegisteredWorkspace(path, title string, strict bool) (*dtoworkspace.WorkspaceResp, error) {
	return s.planner.CreateRegisteredWorkspace(path, title, strict)
}

// TODO(P4b): 删转发（同上）
func (s *Service) EnsureDefaultWorkspace() error { return s.planner.EnsureDefaultWorkspace() }

// TODO(P4b): 删转发（同上）
func (s *Service) DefaultWorkspace() (model.WorkspaceEntity, bool, error) {
	return s.planner.DefaultWorkspace()
}

// TODO(P4b): 删转发（同上）
func (s *Service) resolveTargetWorkspace(reqWorkspaceID string) (*workspace.DefaultWorkspaceRef, string, error) {
	return s.planner.ResolveTargetWorkspace(reqWorkspaceID)
}

// TODO(P4b): 删转发（同上）
func (s *Service) workspaceIDByPath(ctx context.Context, path string) string {
	return s.planner.WorkspaceIDByPath(ctx, path)
}

// WorkspacePlane 工作空间域平面（登记表 / 策略 / 设置 / 浏览全在域里）。读模型按第十一刀口径
// 不进用例层：`Plane.WorkspaceRegistry()` 已经把登记表折成 DTO，用例层再包一层就只是一行转发
// （P4a 正文第十八刀）。
func (s *Service) WorkspacePlane() *workspace.Plane { return s.planner }
