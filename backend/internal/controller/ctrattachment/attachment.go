// Package ctrattachment 承载附件面控制器：只做参数绑定/简单校验/调 svc/回响应，
// 任何业务判断（类型探测、大小、生命周期）都在 svcattachment。
package ctrattachment

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/service/svcattachment"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// AttachmentCtr 附件面控制器四端点。
type AttachmentCtr interface {
	Upload(c *gin.Context)
	Delete(c *gin.Context)
	Get(c *gin.Context)
	Content(c *gin.Context)
}

var _ AttachmentCtr = (*attachmentCtr)(nil)

type attachmentCtr struct {
	svc svcattachment.AttachmentSvc
}

// NewAttachmentCtr 构造 AttachmentCtr（只收**已构造的用例**）。
func NewAttachmentCtr(svc svcattachment.AttachmentSvc) AttachmentCtr {
	return &attachmentCtr{svc: svc}
}

// Upload `POST /attachments`（multipart/form-data，单文件；前端循环多传）。
//
// 字段：`file`（必填）+ 可选 `name`。响应 {id,name,mime_type,size_bytes,sha256,kind,state,created_at}。
func (h *attachmentCtr) Upload(c *gin.Context) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: 缺少 file 字段（multipart 单文件）: "+err.Error()))
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: 无法读取上传文件: "+err.Error()))
		return
	}
	defer func() { _ = f.Close() }()

	declaredName := c.PostForm("name")
	declaredMime := fileHeader.Header.Get("Content-Type")
	resp, err := h.svc.Upload(c.Request.Context(), fileHeader.Filename, declaredName, declaredMime, f)
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

// Content `GET /attachments/:id/content`（字节流：预览用；Content-Type 取探测后的 MIME，inline）。
//
// 只有本机回环可访问（受既有 Host/Origin 矩阵约束，由 middleware 承担）。
func (h *attachmentCtr) Content(c *gin.Context) {
	id := c.Param("id")
	rc, contentType, err := h.svc.OpenContent(c.Request.Context(), id)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	defer func() { _ = rc.Close() }()
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", "inline")
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, rc)
}
