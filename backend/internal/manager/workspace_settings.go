package manager

import (
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
)

// ——— P2b：工作空间设置面的过渡期转发与别名 ———
//
// 设置面（端口 + 快照 + 全量替换 + 交互式校验）已落 `domain/workspace/settings.go`（P2b 第二刀）：
// 它读写的是**域自己的**策略状态（`Plane` 的 roots/dir），留在 manager 只会让那边隔着一层转发
// 去改域的字段。

// WorkspaceRootsStore 是域里那个持久化端口的**类型别名**（全仓只有域里那一份定义）。
//
// TODO(P4b): 删别名（调用点直接依赖 workspace 包）
type WorkspaceRootsStore = workspace.WorkspaceRootsStore

// TODO(P4b): 删转发（装配侧直接依赖 workspace.Plane）
func (s *Service) SetWorkspaceSettingsStore(st WorkspaceRootsStore) {
	s.planner.SetRootsStore(st)
}

// TODO(P4b): 删转发（同上）
func (s *Service) UpdateWorkspaceSettings(roots []string) (*dtoworkspace.WorkspaceSettingsResp, error) {
	return s.planner.UpdateSettings(roots)
}
