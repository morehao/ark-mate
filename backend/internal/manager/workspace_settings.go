package manager

import (
	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
)

// ——— P2b：工作空间设置面的过渡期转发与别名 ———
//
// 设置面（端口 + 快照 + 全量替换 + 交互式校验）已落 `domain/workspace/settings.go`（P2b 第二刀）：
// 它读写的是**域自己的**策略状态（`Plane` 的 roots/dir），留在 manager 只会让那边隔着一层转发
// 去改域的字段。

// TODO(P4b): 删转发（同上）
func (s *Service) UpdateWorkspaceSettings(roots []string) (*dtoworkspace.WorkspaceSettingsResp, error) {
	return s.planner.UpdateSettings(roots)
}
