package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrworkspace"
)

// registerWorkspace 工作空间面的两条线：**登记表**（有 id、可改写的持久实体）与**派生视图**。
//
// 登记表（v14；选择器与左栏分组的事实源）：
//   - GET    /workspaces        列登记（顺序、目录现状、会话数）
//   - POST   /workspaces        登记一个目录（get-or-create；strict=true 时重复报错）
//   - PATCH  /workspaces/:id    改名（不改身份）
//   - DELETE /workspaces/:id    删登记（**不删会话、不删目录**；默认工作空间拒绝）
//   - POST   /workspaces/:id/move  重排（DOM insertBefore 语义，返回完整顺序）
//
// 派生视图（只读；Web 无原生目录选择器，故由控制面提供枚举）：
//   - libraries = 根下的一级子目录（选名字）
//   - browse    = 选工作空间（只列目录）
//   - files     = 看资料内容（列文件，条目不可当 cwd）
//   - recent    = 最近用过但**还没登记**的目录（选择器的"最近使用（未登记）"一栏）
//
// 设置面：GET 看生效名单/显式名单/来源与锁定态，PUT 全量替换（即"受限 ↔ 不限制"的开关）。
// 被 env/flag 锁定或本进程无配置目录时 PUT 被拒（面板转只读）。
//
// 注意路由注册顺序：`/workspaces/:id` 这类带参路径与本组几条**固定字面量**路径
// （libraries/browse/files/recent）在 gin 里可以共存（gin 的 radix 树对静态段优先），
// 但新增固定路径时要放在参数路径**之前**读一遍——顺序反了会让 `/workspaces/browse`
// 被当成 id=browse 打到 Move/Rename 上。
func registerWorkspace(v1 *gin.RouterGroup, ports Ports) {
	ctr := ctrworkspace.NewWorkspaceCtr(ports.Workspace)

	// 固定字面量路径（静态段）在前，参数路径在后。
	v1.GET("/workspaces/libraries", ctr.Libraries)
	v1.GET("/workspaces/browse", ctr.Browse)
	v1.GET("/workspaces/files", ctr.Files)
	v1.GET("/workspaces/recent", ctr.Recent)

	v1.GET("/workspaces", ctr.List)
	v1.POST("/workspaces", ctr.Create)
	v1.PATCH("/workspaces/:id", ctr.Rename)
	v1.DELETE("/workspaces/:id", ctr.Delete)
	v1.POST("/workspaces/:id/move", ctr.Move)

	v1.GET("/settings/workspace", ctr.Settings)
	v1.PUT("/settings/workspace", ctr.UpdateSettings)
}
