package manager

import (
	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
)

// ——— P2b：工作空间只读辅助面的过渡期转发（第三刀的一部分） ———
//
// 浏览 / 建目录 / 列内容 / 最近使用已落 `domain/workspace/view.go`：那一族只依赖本域的策略状态
// （起始点偏好、生效名单）与两个端口（库句柄、会话事实），留在 manager 会让"改一行策略"要跨两个包。

// TODO(P4b): 删转发（调用点直接依赖 workspace.Plane）
func (s *Service) BrowseWorkspace(path string, includeHidden bool) (*dtoworkspace.WorkspaceBrowseResp, error) {
	return s.planner.BrowseWorkspace(path, includeHidden)
}

// TODO(P4b): 删转发（同上）
func (s *Service) CreateWorkspaceDir(parent, name string) (*dtoworkspace.WorkspaceDirCreateResp, error) {
	return s.planner.CreateWorkspaceDir(parent, name)
}

// TODO(P4b): 删转发（同上）
func (s *Service) ListWorkspaceFiles(path string, includeHidden bool) (*dtoworkspace.WorkspaceFilesResp, error) {
	return s.planner.ListWorkspaceFiles(path, includeHidden)
}

// TODO(P4b): 删转发（同上）
func (s *Service) RecentWorkspaces(limit int) ([]dtoworkspace.WorkspaceRecentResp, error) {
	return s.planner.RecentWorkspaces(limit)
}
