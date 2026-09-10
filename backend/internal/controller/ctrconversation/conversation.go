// Package ctrconversation 承载会话面控制器（列表/详情/新建/提问/取消/历史/权限/提问决策）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrconversation

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/dto/dtoconversation"
	"github.com/morehao/ark-forge/internal/service/svcconversation"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// ConversationCtr 会话面控制器（列表/详情/新建/提问/取消/历史/权限/提问决策）。
type ConversationCtr interface {
	List(c *gin.Context)
	Search(c *gin.Context)
	Get(c *gin.Context)
	Update(c *gin.Context)
	Permissions(c *gin.Context)
	PermissionMemories(c *gin.Context)
	RevokeMemory(c *gin.Context)
	ResolvePermission(c *gin.Context)
	Elicitations(c *gin.Context)
	ResolveElicitation(c *gin.Context)
	Create(c *gin.Context)
	Prompt(c *gin.Context)
	Cancel(c *gin.Context)
	History(c *gin.Context)
	Runs(c *gin.Context)
}

var _ ConversationCtr = (*conversationCtr)(nil)

// ConversationHandler 处理会话相关 REST 请求（列表 + search + create/prompt/cancel/history + 元数据 + 权限）。
type conversationCtr struct {
	svc svcconversation.ConversationSvc
}

// NewConversationCtr 构造 ConversationCtr。
func NewConversationCtr(svc svcconversation.ConversationSvc) ConversationCtr {
	return &conversationCtr{svc: svc}
}

// List 列出全部会话（query `archived` 控制是否包含已归档；默认仅未归档；
// `cwd` 可选，非空时只返回工作空间精确等于该绝对路径的会话）。
func (h *conversationCtr) List(c *gin.Context) {
	archived := c.Query("archived") == "true"
	cwd := strings.TrimSpace(c.Query("cwd"))
	if cwd != "" && !filepath.IsAbs(cwd) {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: cwd 需为绝对路径"})
		return
	}
	conversations, err := h.svc.ListConversations(archived, cwd)
	if err != nil {
		gincontext.Fail(c, gerror.Error{Code: 1, Msg: err.Error()})
		return
	}
	gincontext.Success(c, conversations)
}

// Search 搜索会话（query `q` 必填；`limit` 默认 30 上限 50；含归档；updated 降序）。
func (h *conversationCtr) Search(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: q 必填"})
		return
	}
	limit := 30
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	hits, err := h.svc.SearchConversations(c.Request.Context(), q, limit)
	if err != nil {
		gincontext.Fail(c, gerror.Error{Code: 1, Msg: err.Error()})
		return
	}
	gincontext.Success(c, hits)
}

// Get 返回单会话详情（元数据 + 挂起权限）。
func (h *conversationCtr) Get(c *gin.Context) {
	resp, err := h.svc.GetConversation(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// Update 更新会话元数据（title/pinned/archived 子集）。
func (h *conversationCtr) Update(c *gin.Context) {
	var req dtoconversation.ConversationPatch
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.UpdateConversation(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// Permissions 列出会话的挂起权限请求。
func (h *conversationCtr) Permissions(c *gin.Context) {
	resp, err := h.svc.ListPendingPermissions(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// PermissionMemories 列出会话级权限记忆（allow/reject 双侧，详情/撤销 UI 数据源）。
func (h *conversationCtr) PermissionMemories(c *gin.Context) {
	resp, err := h.svc.ListPermissionMemories(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// RevokeMemory 撤销会话级权限记忆（body title+kind 均空 = 一键撤销全部）。
func (h *conversationCtr) RevokeMemory(c *gin.Context) {
	var req dtoconversation.MemoryRevokeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	if err := h.svc.RevokeMemory(c.Request.Context(), c.Param("id"), req.Title, req.Kind); err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, gin.H{"title": req.Title, "kind": req.Kind})
}

// ResolvePermission 对挂起权限请求做出决策（allow/reject × once/always）。
func (h *conversationCtr) ResolvePermission(c *gin.Context) {
	var req dtoconversation.PermissionResolveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.ResolvePermission(c.Request.Context(), c.Param("id"), req.CallID, req.OptionID, req.Always, req.Allowed)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// Elicitations 列出会话的挂起结构化提问（elicitation/create 表单/URL 卡）。
func (h *conversationCtr) Elicitations(c *gin.Context) {
	resp, err := h.svc.ListPendingElicitations(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// ResolveElicitation 对挂起结构化提问做出决策（accept/decline/cancel；accept 可带 content）。
func (h *conversationCtr) ResolveElicitation(c *gin.Context) {
	var req dtoconversation.ElicitationResolveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	if err := h.svc.ResolveElicitation(c.Request.Context(), c.Param("id"), req.ElicitationID, req.Action, req.Content); err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, gin.H{"elicitation_id": req.ElicitationID, "action": req.Action})
}

// Create 建立会话（agent_id + 可选首轮 prompt）。
// 返回 conversation id 与 event_seq 水位：前端 WS attach 传 since_seq=event_seq 即可
// 续传首轮增量事件（§4.5 attach/续传语义；EventSeq=0 表示 bus 未启用，attach since=0 走快照）。
func (h *conversationCtr) Create(c *gin.Context) {
	var req dtoconversation.ConversationCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.CreateConversation(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// Prompt 向既有会话追加一轮 prompt（后台执行，事件经 WS 出流）。
func (h *conversationCtr) Prompt(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Prompt string `json:"prompt" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.PromptConversation(c.Request.Context(), id, req.Prompt)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// Cancel 取消进行中的 turn（幂等）。
func (h *conversationCtr) Cancel(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.CancelTurn(c.Request.Context(), id); err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, gin.H{"id": id, "status": "cancelling"})
}

// History 返回会话事件历史（分页）：query `after`（起点 seq，含之后，默认 0）、
// `limit`（默认 200，上限 1000）。事件按 seq 升序；翻页用末条 seq 作下页 after。
func (h *conversationCtr) History(c *gin.Context) {
	id := c.Param("id")
	q := dtoconversation.HistoryQuery{Limit: 200}
	if v := c.Query("after"); v != "" {
		n, err := parseNonNeg(v)
		if err != nil {
			gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "after 需为非负整数"})
			return
		}
		q.After = n
	}
	if v := c.Query("before"); v != "" {
		n, err := parseNonNeg(v)
		if err != nil {
			gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "before 需为非负整数"})
			return
		}
		q.Before = n
		q.HasBefore = true
	}
	if q.After > 0 && q.HasBefore {
		gincontext.Fail(c, gerror.Error{Code: 40001, Msg: "after 与 before 互斥（一个向后、一个向前）"})
		return
	}
	if v := c.Query("limit"); v != "" {
		n, err := parseNonNeg(v)
		if err != nil {
			gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "limit 需为非负整数"})
			return
		}
		q.Limit = int(n)
	}
	if q.Limit < 1 {
		q.Limit = 1
	}
	if q.Limit > 1000 {
		q.Limit = 1000
	}
	resp, err := h.svc.ConversationHistory(c.Request.Context(), id, q)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, resp)
}

// Runs 列出某会话的轮次明细（runs 表读模型；内存模式返回空数组）。
func (h *conversationCtr) Runs(c *gin.Context) {
	id := c.Param("id")
	runs, err := h.svc.ListRuns(c.Request.Context(), id)
	if err != nil {
		gincontext.Fail(c, err)
		return
	}
	gincontext.Success(c, gin.H{"id": id, "runs": runs})
}

// parseNonNeg 解析非负整数查询参数。
func parseNonNeg(v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("bad non-negative int")
	}
	return n, nil
}
