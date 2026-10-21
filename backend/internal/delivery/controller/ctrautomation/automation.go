// Package ctrautomation 承载 automation（定时任务）面的控制器。
//
// 与其余控制器同一条纪律：只依赖 internal/service/svcautomation 的窄端口，错误一律经
// gincontext 落到统一响应信封（`TestControllerErrorExitsCarryCode` 守卫：不得透传裸 err，
// 否则客户端拿到的是 code:-1）。
//
// P4a 正文第九刀起，本控制器拿到的**不是**用例实例而是依赖事实（`*automation.Plane`），
// 用例在构造函数里造（`svcautomation.New`）——装配点为此少持一格 svc 类型。
package ctrautomation

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/contract/dtoautomation"
	"github.com/morehao/ark-mate/internal/service/svcautomation"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// AutomationCtr automation 面控制器。
type AutomationCtr interface {
	ListAutomations(c *gin.Context)
	CreateAutomation(c *gin.Context)
	PreviewAutomation(c *gin.Context)
	GetAutomation(c *gin.Context)
	UpdateAutomation(c *gin.Context)
	DeleteAutomation(c *gin.Context)
	ListAutomationRuns(c *gin.Context)
	RunAutomationNow(c *gin.Context)
}

var _ AutomationCtr = (*automationCtr)(nil)

type automationCtr struct {
	svc svcautomation.AutomationSvc
}

// NewAutomationCtr 构造 AutomationCtr（只收**已构造的用例**）。
func NewAutomationCtr(svc svcautomation.AutomationSvc) AutomationCtr {
	return &automationCtr{svc: svc}
}

// ListAutomations 列出全部 automation（含定义装载失败的那些，带 `error` 字段）。
func (h *automationCtr) ListAutomations(c *gin.Context) {
	items, err := h.svc.ListAutomations()
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, items)
}

// CreateAutomation 新建（id 已存在 → 40901 automation_exists，绝不覆盖）。
func (h *automationCtr) CreateAutomation(c *gin.Context) {
	var req dtoautomation.AutomationWriteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateAutomation(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// PreviewAutomation 只算未来若干次到期时刻：**不落盘、不建会话**。
//
// 放在 `/automations/:id` 之前注册（否则 "preview" 会被当成一个 id）。
func (h *automationCtr) PreviewAutomation(c *gin.Context) {
	var req dtoautomation.AutomationPreviewReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.PreviewAutomation(req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// GetAutomation 取一条（不存在 → 40401）。
func (h *automationCtr) GetAutomation(c *gin.Context) {
	resp, err := h.svc.GetAutomation(c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// UpdateAutomation 覆盖写：只改 body 里出现过的键（未出现的保留原值），`null` 删键。
func (h *automationCtr) UpdateAutomation(c *gin.Context) {
	var req dtoautomation.AutomationWriteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.UpdateAutomation(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// DeleteAutomation 删除（幂等：不存在 → deleted:false 且无错）。
func (h *automationCtr) DeleteAutomation(c *gin.Context) {
	resp, err := h.svc.DeleteAutomation(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ListAutomationRuns 运行史（含 skipped/overlap/duplicate：**没跑成的那些也要看得见**）。
func (h *automationCtr) ListAutomationRuns(c *gin.Context) {
	resp, err := h.svc.ListAutomationRuns(c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// RunAutomationNow 手动触发一次（走同一认领判据；失败既报 4xx 又已留痕）。
func (h *automationCtr) RunAutomationNow(c *gin.Context) {
	resp, err := h.svc.RunAutomationNow(c.Request.Context(), c.Param("id"))
	if err != nil {
		// 失败也回带结果：面板要显示"我点过、没跑成、为什么"，而不是一个空信封。
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
