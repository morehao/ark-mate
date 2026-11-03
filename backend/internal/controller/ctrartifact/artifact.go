// Package ctrartifact 承载产物面控制器：只做参数绑定/简单校验/调 svc/回响应，
// 任何业务判断（种类映射、载体一致性、幂等收录）都在 svcartifact / svcmaterial。
package ctrartifact

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/dto/dtoartifact"
	"github.com/morehao/ark-mate/internal/service/svcartifact"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ArtifactCtr 产物面控制器三端点。
type ArtifactCtr interface {
	Register(c *gin.Context)
	Get(c *gin.Context)
	ListBySession(c *gin.Context)
}

var _ ArtifactCtr = (*artifactCtr)(nil)

type artifactCtr struct {
	svc svcartifact.ArtifactSvc
}

// NewArtifactCtr 构造 ArtifactCtr（只收**已构造的用例**）。
func NewArtifactCtr(svc svcartifact.ArtifactSvc) ArtifactCtr {
	return &artifactCtr{svc: svc}
}

// Register `POST /artifacts`（JSON {file_id?, content_text?, session_id, run_id?, tool_call_id?, origin, kind, title}）。
//
// **只收 file_id，不收 multipart**（字节已在 POST /files 落 filestore）。
func (h *artifactCtr) Register(c *gin.Context) {
	var req dtoartifact.RegisterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: 请求体不是合法 JSON: "+err.Error()))
		return
	}
	resp, err := h.svc.Register(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Get `GET /artifacts/:id`。
func (h *artifactCtr) Get(c *gin.Context) {
	resp, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ListBySession `GET /sessions/:id/artifacts`。
func (h *artifactCtr) ListBySession(c *gin.Context) {
	resp, err := h.svc.ListBySession(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
