// Package ctrmaterial 承载资料库面控制器：只做参数绑定/简单校验/调 svc/回响应，
// 任何业务判断（种类映射、载体一致性、成环、级联删）都在 svcmaterial。
package ctrmaterial

import (
	"net/http"
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
	Search(c *gin.Context)
	GetRows(c *gin.Context)
	PutRows(c *gin.Context)
	Publish(c *gin.Context)
	View(c *gin.Context)
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

// Upload 已随 P0′ 上传收口删除：上传文件成 drive 节点改两步式（先 `POST /files` 拿 file_id，
// 再 `POST /materials/nodes {node_kind:drive, drive_upload_id}`）。`svcmaterial.Upload` 用例仍保留，
// 供进程内 MCP `library_upload` 使用（不经过本机回环 HTTP）。

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

// GetRows `GET /materials/nodes/:id/rows`（table 节点的 schema + 行）。
func (h *materialCtr) GetRows(c *gin.Context) {
	resp, err := h.svc.GetTableRows(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// PutRows `PUT /materials/nodes/:id/rows`（整表替换行数据，rows 为 JSON 数组字符串）。
func (h *materialCtr) PutRows(c *gin.Context) {
	var req dtomaterial.SetRowsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: 请求体不是合法 JSON: "+err.Error()))
		return
	}
	resp, err := h.svc.SetTableRows(c.Request.Context(), c.Param("id"), req.Rows)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Publish `POST /materials/nodes/:id/publish`（page 落盘 + 本地查看路径）。
func (h *materialCtr) Publish(c *gin.Context) {
	resp, err := h.svc.PublishPage(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// View `GET /materials/nodes/:id/view`（page 节点 body 的 HTML，只绑回环）。
func (h *materialCtr) View(c *gin.Context) {
	html, err := h.svc.PageHTML(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusOK)
	_, _ = c.Writer.WriteString(html)
}
