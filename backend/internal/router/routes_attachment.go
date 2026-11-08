package router

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/controller/ctrattachment"
)

// registerAttachment 附件面（v32；P0′ 收口后 POST 只建索引）：
//   - POST   /attachments                 从已落 filestore 的字节（file_id）建附件索引 → pending（JSON {file_id,name?}）
//   - GET    /attachments/:id             回读元数据（不回字节；含 file_id）
//   - DELETE /attachments/:id             撤下待挂附件（幂等；linked → 40901）
//
// **收字节统一在 `POST /files`（ginupload）**：本端点不再收 multipart 流，只认 `POST /files` 返回的
// file_id。**字节回读/预览走签名通路（presign-url + /objects），不再有 `/attachments/:id/content`**——
// 前端凭 AttachmentResp.file_id 换 presign-url 直接取字节，见 `docs/contract.md` 的 `/files` 组。
func registerAttachment(v1 *gin.RouterGroup, ctr ctrattachment.AttachmentCtr) {
	v1.POST("/attachments", ctr.Register)
	v1.GET("/attachments/:id", ctr.Get)
	v1.DELETE("/attachments/:id", ctr.Delete)
	// office 预览：把已落 filestore 的 docx/pptx 在服务端转成 PDF 字节流（前端复用 PdfRenderer）。
	// 路径刻意挂在 `/files/:id/...` 下（与 ginupload 的 presign-url 同形、同一 `/files` 资源面），
	// 但**收字节仍在 ginupload**、本端点只读已有 blob 转格式，不破坏「全局唯一收字节面」。
	v1.POST("/files/:id/convert-pdf", ctr.ConvertPDF)
}
