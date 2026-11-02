package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrattachment"
)

// registerAttachment 附件面（v32）：
//   - POST   /attachments                 上传一个文件（multipart 单文件；前端循环多传）→ pending
//   - GET    /attachments/:id             回读元数据（不回字节）
//   - GET    /attachments/:id/content     字节流（预览：Content-Type 取探测后的 MIME，inline）
//   - DELETE /attachments/:id             撤下待挂附件（幂等；linked → 40901）
//
// 单文件上传、不做批量端点（多文件是前端循环；批量会把「部分失败」的事务语义搅浑，设计 §4.1）。
// `/attachments/:id/content` 与 `/attachments/:id` 在 gin 的 radix 树里静态段 `content` 优先，
// 不会把 `content` 当成 id。
func registerAttachment(v1 *gin.RouterGroup, ctr ctrattachment.AttachmentCtr) {
	v1.POST("/attachments", ctr.Upload)
	v1.GET("/attachments/:id", ctr.Get)
	v1.GET("/attachments/:id/content", ctr.Content)
	v1.DELETE("/attachments/:id", ctr.Delete)
}
