package handler

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// SessionHandler 处理会话相关 REST 请求（列表 + create/prompt/cancel/history）。
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

// History 返回会话事件历史（分页）：query `after`（起点 seq，含之后，默认 0）、
// `limit`（默认 200，上限 1000）。事件按 seq 升序；翻页用末条 seq 作下页 after。
func (h *SessionHandler) History(c *gin.Context) {
	id := c.Param("id")
	after, limit := int64(0), 200
	if v := c.Query("after"); v != "" {
		n, err := parseNonNeg(v)
		if err != nil {
			gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "after 需为非负整数"})
			return
		}
		after = n
	}
	if v := c.Query("limit"); v != "" {
		n, err := parseNonNeg(v)
		if err != nil {
			gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "limit 需为非负整数"})
			return
		}
		limit = int(n)
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	resp, err := h.svc.SessionHistory(c.Request.Context(), id, after, limit)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// parseNonNeg 解析非负整数查询参数。
func parseNonNeg(v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("bad non-negative int")
	}
	return n, nil
}
