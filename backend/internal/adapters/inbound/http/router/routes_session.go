package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/adapters/inbound/http/controller/ctrexport"
	"github.com/morehao/ark-mate/internal/adapters/inbound/http/controller/ctrsearch"
	"github.com/morehao/ark-mate/internal/adapters/inbound/http/controller/ctrsession"
)

// registerSession 会话面：概要/详情/新建/轮次执行/历史/权限与结构化提问决策，
// 以及跨会话消息检索与会话导出。
//
// 静态路径（/sessions/search、/sessions/export）先于 :id 注册，
// 保持与历史路由表一致的解析优先级。
func registerSession(v1 *gin.RouterGroup, ctr ctrsession.SessionCtr, search ctrsearch.SearchCtr, export ctrexport.ExportCtr) {

	v1.GET("/sessions", ctr.List)
	v1.GET("/sessions/search", ctr.Search)
	// M3 ②：跨会话消息级检索（锚点 = seq）与批量导出（markdown|text|json）。
	v1.GET("/search/messages", search.Messages)
	v1.POST("/sessions/export", export.Sessions)
	v1.POST("/sessions", ctr.Create)
	v1.GET("/sessions/:id", ctr.Get)
	v1.PATCH("/sessions/:id", ctr.Update)
	v1.GET("/sessions/:id/history", ctr.History)
	v1.GET("/sessions/:id/runs", ctr.Runs)
	v1.POST("/sessions/:id/prompt", ctr.Prompt)
	v1.POST("/sessions/:id/cancel", ctr.Cancel)
	// §6 busy input 队列：列出待投递项（冷挂载重建）+ 撤下一条（投递走 POST prompt 的 delivery）。
	v1.GET("/sessions/:id/inputs", ctr.QueuedInputs)
	v1.DELETE("/sessions/:id/inputs/:input_id", ctr.DeleteQueuedInput)
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
