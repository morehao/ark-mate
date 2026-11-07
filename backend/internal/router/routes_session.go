package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrexport"
	"github.com/morehao/ark-mate/internal/controller/ctrsearch"
	"github.com/morehao/ark-mate/internal/controller/ctrsession"
)

// registerSession 会话面：概要/详情/新建/轮次执行/历史/权限与结构化提问决策，
// 以及跨会话消息检索与会话导出。
//
// 静态路径（/sessions/search、/sessions/export）先于 :id 注册，
// 保持与历史路由表一致的解析优先级。
func registerSession(v1 *gin.RouterGroup, ctr ctrsession.SessionCtr, search ctrsearch.SearchCtr, export ctrexport.ExportCtr) {

	v1.GET("/sessions", ctr.List)
	v1.GET("/sessions/search", ctr.Search)
	// v4 P1：composer `/` 技能选择器的内置技能目录（平台内置、一次性注入的协作技能，
	// 与「能力数据面」的 `GET /skills`（技能定义目录）是两回事）。
	v1.GET("/composer/skills", ctr.ComposerSkills)
	// M3 ②：跨会话消息级检索（锚点 = seq）与批量导出（markdown|text|json）。
	v1.GET("/search/messages", search.Messages)
	v1.POST("/sessions/export", export.Sessions)
	v1.POST("/sessions", ctr.Create)
	v1.GET("/sessions/:id", ctr.Get)
	v1.PATCH("/sessions/:id", ctr.Update)
	// M3-8：会话内改能力（技能/MCP/专家）。独立端点而不是塞进 `PATCH /sessions/:id`：
	// 那条是元数据子集（title/pinned/archived/model），能力选定是另一份语义（逐 kind 三态补丁 +
	// 专家物化 + 可达性校验），混进去会让两边都长出对方的判断。
	v1.PATCH("/sessions/:id/capabilities", ctr.UpdateCapabilities)
	v1.GET("/sessions/:id/history", ctr.History)
	v1.GET("/sessions/:id/runs", ctr.Runs)
	v1.POST("/sessions/:id/prompt", ctr.Prompt)
	v1.POST("/sessions/:id/cancel", ctr.Cancel)
	// §6 busy input 队列：列出待投递项（冷挂载重建）+ 撤下一条（投递走 POST prompt 的 delivery）。
	v1.GET("/sessions/:id/inputs", ctr.QueuedInputs)
	v1.DELETE("/sessions/:id/inputs/:input_id", ctr.DeleteQueuedInput)
	// v4 行内编辑：改正文/附件，不出队（区别于上面「撤下」= 离队）。
	v1.PATCH("/sessions/:id/inputs/:input_id", ctr.EditQueuedInput)
	// 队列三动作（ADR-10）：重排 / 立即发送 / 取出编辑。
	//
	// 三条都是**参数路径下的固定字面量**，与上面那条 DELETE 同级——它们必须留在
	// `:input_id` **之后**注册：提到前面会让 `/inputs/move` 被当成 input_id="move" 打到撤下上。
	v1.POST("/sessions/:id/inputs/:input_id/move", ctr.MoveQueuedInput)
	v1.POST("/sessions/:id/inputs/:input_id/send-now", ctr.SendNowQueuedInput)
	v1.POST("/sessions/:id/inputs/:input_id/pop", ctr.PopQueuedInput)
	v1.GET("/sessions/:id/permissions", ctr.Permissions)
	v1.POST("/sessions/:id/permission", ctr.ResolvePermission)
	// 会话级权限记忆（allow/reject 双侧，P0 起对称语义）。
	v1.GET("/sessions/:id/permissions/memories", ctr.PermissionMemories)
	v1.POST("/sessions/:id/permissions/memories/revoke", ctr.RevokeMemory)
	// 兼容别名：旧路径 /permissions/always（历史客户端/脚本）；新路径语义一致。
	v1.GET("/sessions/:id/permissions/always", ctr.PermissionMemories)
	v1.POST("/sessions/:id/permissions/always/revoke", ctr.RevokeMemory)
	v1.GET("/sessions/:id/elicitations", ctr.Elicitations)
	v1.POST("/sessions/:id/elicitation", ctr.ResolveElicitation)
}
