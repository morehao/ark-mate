package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrattachment"
)

// registerAttachment 附件面（v32；P0′ 收口后 POST 只建索引）：
//   - POST   /attachments                 从已落 filestore 的字节（file_id）建附件索引 → pending（JSON {file_id,name?}）
//   - GET    /attachments/:id             回读元数据（不回字节）
//   - GET    /attachments/:id/content     字节流（预览：Content-Type 取探测后的 MIME，inline）
//   - DELETE /attachments/:id             撤下待挂附件（幂等；linked → 40901）
//
// **收字节统一在 `POST /files`（ginupload）**：本端点不再收 multipart 流，只认 `POST /files` 返回的
// file_id。`/attachments/:id/content` 与 `/attachments/:id` 在 gin 的 radix 树里静态段 `content` 优先，
// 不会把 `content` 当成 id。
func registerAttachment(v1 *gin.RouterGroup, ctr ctrattachment.AttachmentCtr) {
	v1.POST("/attachments", ctr.Register)
	v1.GET("/attachments/:id", ctr.Get)
	v1.GET("/attachments/:id/content", ctr.Content)
	v1.DELETE("/attachments/:id", ctr.Delete)
}
