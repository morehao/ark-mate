// Package ctrlibrary 承载能力 Library 的传输层（M2-4）：统一能力清单的只读聚合。
//
// Library 不拥有任何数据（事实源是能力定义文件与各注册表），所以这里只有一个 GET：
// 过滤参数在这一层解析成查询对象，领域层不感知 HTTP。
package ctrlibrary

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/service/svclibrary"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// LibraryCtr Library 控制器。
type LibraryCtr interface {
	ListLibrary(c *gin.Context)
}

var _ LibraryCtr = (*libraryCtr)(nil)

type libraryCtr struct {
	svc svclibrary.LibrarySvc
}

// NewLibraryCtr 构造 LibraryCtr（只收**已构造的用例**）。
func NewLibraryCtr(svc svclibrary.LibrarySvc) LibraryCtr {
	return &libraryCtr{svc: svc}
}

// ListLibrary 统一能力清单：`?kind=skill|mcp|agent|tool`（未知值 → 40000 `invalid_param`）、
// `?q=`（id/name/description 大小写不敏感子串）。逐条回带**逐 runtime 的可达性**与人话原因，
// 刻意不给"整体可用吗"的合并结论。
func (h *libraryCtr) ListLibrary(c *gin.Context) {
	items, err := h.svc.ListLibrary(svclibrary.Query{Kind: c.Query("kind"), Q: c.Query("q")})
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}
