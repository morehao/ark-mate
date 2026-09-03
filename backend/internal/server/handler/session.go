package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// SessionHandler 处理会话相关 REST 请求（列表 + create/prompt/cancel）。
type SessionHandler struct {
	svc Service
}

// List 列出全部会话。
func (h *SessionHandler) List(c *gin.Context) {
	sessions, err := h.svc.ListSessions()
	if err != nil {
		gincontext.Fail(c, gerror.Error{Code: 1, Msg: err.Error()})
		return
	}
	gincontext.Success(c, sessions)
}

// Create 建立会话（agent_id + 可选首轮 prompt）。
// 返回 session id 与 event_seq 水位：前端 WS attach 传 since_seq=event_seq 即可
// 续传首轮增量事件（§4.5 attach/续传语义；EventSeq=0 表示 bus 未启用，attach since=0 走快照）。
func (h *SessionHandler) Create(c *gin.Context) {
	var req SessionCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.CreateSession(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// Prompt 向既有会话追加一轮 prompt（后台执行，事件经 WS 出流）。
func (h *SessionHandler) Prompt(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Prompt string `json:"prompt" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.PromptSession(c.Request.Context(), id, req.Prompt)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// Cancel 取消进行中的 turn（幂等）。
func (h *SessionHandler) Cancel(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.CancelTurn(c.Request.Context(), id); err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, gin.H{"id": id, "status": "cancelling"})
}
