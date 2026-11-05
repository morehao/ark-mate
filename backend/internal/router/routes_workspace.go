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
//   - browse    = 选工作空间（只列目录，**全系统**）
//   - files     = 看资料内容（列文件，条目不可当 cwd）
//   - recent    = 最近用过但**还没登记**的目录（v14.3 起面板无消费者，保留为可用的只读视图）
//
// 选目录的**宿主能力**（v14.5，对齐 dsh 的 `directory-picker-native`）：
//   - POST   /workspaces/pick-directory  抬一次宿主原生目录选择器（系统面板，核心进程 spawn）
//     它让**本机浏览器入口**也能用上系统面板（面板由核心进程抬，与客户端形态无关）；
//     宿主抬不起来时前端开应用内浏览（`/capabilities` 的 `native-dialog` 声明这件事）。
//
// 目录浏览面的写面（v14.3）：
//   - POST   /workspaces/dirs   在已存在的目录下建子目录（目录选择器的「新建文件夹」）
//     它也是写动作但**不落库**：只在磁盘上建一层目录。
//
// 注意路由注册顺序：`/workspaces/:id` 这类带参路径与本组几条**固定字面量**路径
// （browse/files/recent/dirs）在 gin 里可以共存（gin 的 radix 树对静态段优先），
// 但新增固定路径时要放在参数路径**之前**读一遍——顺序反了会让 `/workspaces/browse`
// 被当成 id=browse 打到 Move/Rename 上。
//
// 目录浏览的起点偏好（`MATE_WORKSPACE_ROOTS`）是**部署级**配置，只经 env/config.json 设置，
// 不对外暴露设置面（v14.6 起去掉「工作区起始位置」的用户设置接口）。
func registerWorkspace(v1 *gin.RouterGroup, ctr ctrworkspace.WorkspaceCtr) {

	// 固定字面量路径（静态段）在前，参数路径在后。
	v1.GET("/workspaces/browse", ctr.Browse)
	v1.GET("/workspaces/files", ctr.Files)
	v1.GET("/workspaces/recent", ctr.Recent)
	v1.POST("/workspaces/dirs", ctr.CreateDir)
	v1.POST("/workspaces/pick-directory", ctr.PickDirectory)

	v1.GET("/workspaces", ctr.List)
	v1.POST("/workspaces", ctr.Create)
	v1.PATCH("/workspaces/:id", ctr.Rename)
	v1.DELETE("/workspaces/:id", ctr.Delete)
	v1.POST("/workspaces/:id/move", ctr.Move)
}
