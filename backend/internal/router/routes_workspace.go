package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/controller/ctrworkspace"
)

// registerWorkspace 工作空间只读面与白名单设置面。
//
// Web 无原生目录选择器 → 控制面提供目录枚举与"最近使用"（从会话派生）：
//   - libraries = 根下的一级子目录（选名字）
//   - browse = 选工作空间（只列目录）
//   - files = 看资料内容（列文件，条目不可当 cwd）
//
// 设置面：GET 看生效名单/显式名单/来源与锁定态，PUT 全量替换（即"受限 ↔ 不限制"的开关）。
// 被 env/flag 锁定或本进程无配置目录时 PUT 被拒（面板转只读）。
func registerWorkspace(v1 *gin.RouterGroup, ports Ports) {
	ctr := ctrworkspace.NewWorkspaceCtr(ports.Workspace, ports.ErrMapper)

	v1.GET("/workspaces/libraries", ctr.Libraries)
	v1.GET("/workspaces/browse", ctr.Browse)
	v1.GET("/workspaces/files", ctr.Files)
	v1.GET("/workspaces/recent", ctr.Recent)
	v1.GET("/settings/workspace", ctr.Settings)
	v1.PUT("/settings/workspace", ctr.UpdateSettings)
}
