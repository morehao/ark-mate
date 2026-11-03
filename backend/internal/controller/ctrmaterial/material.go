// Package ctrmaterial 承载资料库面控制器：只做参数绑定/简单校验/调 svc/回响应，
// 任何业务判断（种类映射、载体一致性、成环、级联删）都在 svcmaterial。
package ctrmaterial

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/dto/dtomaterial"
	"github.com/morehao/ark-mate/internal/service/svcmaterial"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// MaterialCtr 资料库面控制器（目录树 + 上传 + 关键词检索）。
type MaterialCtr interface {
	ListNodes(c *gin.Context)
	GetNode(c *gin.Context)
	CreateNode(c *gin.Context)
	PatchNode(c *gin.Context)
	DeleteNode(c *gin.Context)
	Upload(c *gin.Context)
	Search(c *gin.Context)
}

var _ MaterialCtr = (*materialCtr)(nil)

type materialCtr struct {
	svc svcmaterial.MaterialSvc
}

// NewMaterialCtr 构造 MaterialCtr（只收**已构造的用例**）。
func NewMaterialCtr(svc svcmaterial.MaterialSvc) MaterialCtr {
	return &materialCtr{svc: svc}
}

// listIntParam 取 query 里的整数（缺省 0）。
func listIntParam(c *gin.Context, key string) int {
	if v := c.Query(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// ListNodes `GET /materials/nodes?parent=<id>&limit=&offset=`。
func (h *materialCtr) ListNodes(c *gin.Context) {
	parent := c.Query("parent")
	limit := listIntParam(c, "limit")
	offset := listIntParam(c, "offset")
	resp, err := h.svc.ListNodes(c.Request.Context(), parent, limit, offset)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// GetNode `GET /materials/nodes/:id`。
func (h *materialCtr) GetNode(c *gin.Context) {
	resp, err := h.svc.GetNode(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// CreateNode `POST /materials/nodes`。
func (h *materialCtr) CreateNode(c *gin.Context) {
	var req dtomaterial.CreateNodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: 请求体不是合法 JSON: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateNode(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// PatchNode `PATCH /materials/nodes/:id`。
func (h *materialCtr) PatchNode(c *gin.Context) {
	var req dtomaterial.PatchNodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: 请求体不是合法 JSON: "+err.Error()))
		return
	}
	resp, err := h.svc.PatchNode(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// DeleteNode `DELETE /materials/nodes/:id`（幂等）。
func (h *materialCtr) DeleteNode(c *gin.Context) {
	resp, err := h.svc.DeleteNode(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Upload `POST /materials/upload`（multipart，单文件；前端循环多传）。
//
// 字段：`file`（必填）+ 可选 `parent_id`。
func (h *materialCtr) Upload(c *gin.Context) {
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
	parentID := c.PostForm("parent_id")
	resp, err := h.svc.Upload(c.Request.Context(), fileHeader.Filename, "", fileHeader.Header.Get("Content-Type"), parentID, f)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Search `GET /materials/search?q=&limit=&offset=`（关键词检索，非语义）。
func (h *materialCtr) Search(c *gin.Context) {
	q := c.Query("q")
	limit := listIntParam(c, "limit")
	offset := listIntParam(c, "offset")
	resp, err := h.svc.Search(c.Request.Context(), q, limit, offset)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
