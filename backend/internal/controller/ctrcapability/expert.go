package ctrcapability

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ——— 专家定义面（P0）———
//
// 控制器只做传输层：绑定、调用用例、把错误经统一信封出。业务规则（id 词法、长度/危险标记校验、
// revision CAS）都在 `domain/expert` 里，与 skill/mcp 走同一条写面纪律。

// ListExperts 列出全部专家（含禁用；列表不返回 instructions 正文）。
func (h *capabilityCtr) ListExperts(c *gin.Context) {
	items, err := h.svc.ListExperts(c.Request.Context())
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}

// GetExpert 读一条专家详情（返回 instructions 全文，供管理页/预览）。
func (h *capabilityCtr) GetExpert(c *gin.Context) {
	resp, err := h.svc.GetExpert(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// CreateExpert 新建一个专家定义：body = 专家定义（`id` 必填）。id 已存在 → expert_exists(40901)；
// 校验不过 → definition_invalid(40001) 且零字节落库。
func (h *capabilityCtr) CreateExpert(c *gin.Context) {
	var req dtocapability.ExpertWriteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.WriteExpert(c.Request.Context(), "", req, true)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// UpdateExpert 覆盖写一个专家定义：**只改 body 里出现的键**（未出现的保留原值，null 删键）；
// 路径 id 与 body id 不一致 → 40000。
func (h *capabilityCtr) UpdateExpert(c *gin.Context) {
	var req dtocapability.ExpertWriteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.WriteExpert(c.Request.Context(), c.Param("id"), req, false)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// DeleteExpert 删除一个专家定义（幂等：不存在 → deleted:false 且 code=0）。会话快照不受影响。
func (h *capabilityCtr) DeleteExpert(c *gin.Context) {
	resp, err := h.svc.DeleteExpert(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// SetExpertEnabled 启停一个专家（禁用只影响新选择，不影响已建会话）。
func (h *capabilityCtr) SetExpertEnabled(c *gin.Context) {
	var req dtocapability.ExpertToggleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.SetExpertEnabled(c.Request.Context(), c.Param("id"), req.Enabled)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
