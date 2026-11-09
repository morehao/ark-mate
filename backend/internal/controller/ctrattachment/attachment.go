// Package ctrattachment 承载附件面控制器：只做参数绑定/简单校验/调 svc/回响应，
// 任何业务判断（类型探测、大小、生命周期）都在 svcattachment。
package ctrattachment

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/service/svcattachment"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// AttachmentCtr 附件面控制器三端点。
type AttachmentCtr interface {
	Register(c *gin.Context)
	Delete(c *gin.Context)
	Get(c *gin.Context)
	// ConvertPDF `POST /files/:id/convert-pdf`：把某 file_id 的 office 文档转成 PDF 字节流返回。
	ConvertPDF(c *gin.Context)
}

var _ AttachmentCtr = (*attachmentCtr)(nil)

type attachmentCtr struct {
	svc svcattachment.AttachmentSvc
}

// NewAttachmentCtr 构造 AttachmentCtr（只收**已构造的用例**）。
func NewAttachmentCtr(svc svcattachment.AttachmentSvc) AttachmentCtr {
	return &attachmentCtr{svc: svc}
}

// registerReq `POST /attachments` 的请求体（收口后只收 file_id，不再收 multipart 流）。
type registerReq struct {
	// FileID 上传面 `POST /files` 返回的 file_id（= core_file_upload.id）。
	FileID string `json:"file_id"`
	// Name 可选展示名；缺省用上传时的原始名。
	Name string `json:"name"`
}

// Register `POST /attachments`（JSON `{file_id, name?}`）— 从已落 filestore 的字节建附件业务索引。
//
// 收口后字节统一经 `POST /files`（ginupload）已落 core_file；本端点只建索引（探测 kind、落 pending 行）。
// 响应 {id,name,mime_type,size_bytes,sha256,kind,state,created_at}。
func (h *attachmentCtr) Register(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: 请求体不是合法 JSON: "+err.Error()))
		return
	}
	resp, err := h.svc.Register(c.Request.Context(), req.FileID, req.Name)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Delete `DELETE /attachments/:id`（幂等：不存在 → deleted=false 且 code=0；linked → 40901）。
func (h *attachmentCtr) Delete(c *gin.Context) {
	id := c.Param("id")
	resp, err := h.svc.Delete(c.Request.Context(), id)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Get `GET /attachments/:id`（回读元数据；不回字节）。
func (h *attachmentCtr) Get(c *gin.Context) {
	id := c.Param("id")
	resp, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ConvertPDF `POST /files/:id/convert-pdf`：把某 file_id（= core_file_upload.id）的 office 文档在
// 服务端转成 PDF，**直接以 `application/pdf` 字节流返回**（不是 JSON 信封——调用方要的是 PDF 字节，
// 前端拿去喂 PdfRenderer）。
//
// 可选 query `?name=` 给原始文件名（用于推导输出后缀；缺省按上传记录名）。
// 失败一律走**信封**（HTTP 200 + code!=0）：转换器不可用 → 40501 office_preview_unsupported；
// 文件不存在 → 40401 attachment_not_found；转换失败 → 50001 office_preview_failed。
func (h *attachmentCtr) ConvertPDF(c *gin.Context) {
	fileID := c.Param("id")
	name := c.Query("name")
	pdf, err := h.svc.ConvertToPDF(c.Request.Context(), fileID, name)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	// 直接吐字节：Content-Disposition inline 便于浏览器内联展示；不设缓存（每次可能即时转换）。
	c.Header("Content-Disposition", "inline; filename=\"preview.pdf\"")
	c.Data(http.StatusOK, "application/pdf", pdf)
}
