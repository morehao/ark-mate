package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/controller/ctrconversation"
	"github.com/morehao/ark-forge/internal/controller/ctrexport"
	"github.com/morehao/ark-forge/internal/controller/ctrsearch"
)

// registerConversation 会话面：概要/详情/新建/轮次执行/历史/权限与结构化提问决策，
// 以及跨会话消息检索与会话导出。
//
// 静态路径（/conversations/search、/conversations/export）先于 :id 注册，
// 保持与历史路由表一致的解析优先级。
func registerConversation(v1 *gin.RouterGroup, ports Ports) {
	ctr := ctrconversation.NewConversationCtr(ports.Conversation)
	search := ctrsearch.NewSearchCtr(ports.Search)
	export := ctrexport.NewExportCtr(ports.Export)

	v1.GET("/conversations", ctr.List)
	v1.GET("/conversations/search", ctr.Search)
	// M3 ②：跨会话消息级检索（锚点 = seq）与批量导出（markdown|text|json）。
	v1.GET("/search/messages", search.Messages)
	v1.POST("/conversations/export", export.Conversations)
	v1.POST("/conversations", ctr.Create)
	v1.GET("/conversations/:id", ctr.Get)
	v1.PATCH("/conversations/:id", ctr.Update)
	v1.GET("/conversations/:id/history", ctr.History)
	v1.GET("/conversations/:id/runs", ctr.Runs)
	v1.POST("/conversations/:id/prompt", ctr.Prompt)
	v1.POST("/conversations/:id/cancel", ctr.Cancel)
	// §6 busy input 队列：列出待投递项（冷挂载重建）+ 撤下一条（投递走 POST prompt 的 delivery）。
	v1.GET("/conversations/:id/inputs", ctr.QueuedInputs)
	v1.DELETE("/conversations/:id/inputs/:input_id", ctr.DeleteQueuedInput)
	v1.GET("/conversations/:id/permissions", ctr.Permissions)
	v1.POST("/conversations/:id/permission", ctr.ResolvePermission)
	// 会话级权限记忆（allow/reject 双侧，P0 起对称语义）。
	v1.GET("/conversations/:id/permissions/memories", ctr.PermissionMemories)
	v1.POST("/conversations/:id/permissions/memories/revoke", ctr.RevokeMemory)
	// 兼容别名：旧路径 /permissions/always（历史客户端/脚本）；新路径语义一致。
	v1.GET("/conversations/:id/permissions/always", ctr.PermissionMemories)
	v1.POST("/conversations/:id/permissions/always/revoke", ctr.RevokeMemory)
	v1.GET("/conversations/:id/elicitations", ctr.Elicitations)
	v1.POST("/conversations/:id/elicitation", ctr.ResolveElicitation)
}
