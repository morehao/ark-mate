// Package ctrsession 承载会话面控制器（列表/详情/新建/提问/取消/历史/权限/提问决策）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrsession

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/service/svcsession"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// SessionCtr 会话面控制器（列表/详情/新建/提问/取消/历史/权限/提问决策）。
type SessionCtr interface {
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
	QueuedInputs(c *gin.Context)
	DeleteQueuedInput(c *gin.Context)
	History(c *gin.Context)
	Runs(c *gin.Context)
}

var _ SessionCtr = (*sessionCtr)(nil)

// SessionHandler 处理会话相关 REST 请求（列表 + search + create/prompt/cancel/history + 元数据 + 权限）。
type sessionCtr struct {
	svc svcsession.SessionSvc
	// hist 会话历史**读面**（P4a 正文第十刀起在用例层，与 `svc` 分开按面持有）。
	hist svcsession.HistorySvc
	// detail 会话详情**读面**（P4a 正文第四十刀起在用例层）：单会话详情 + 三份交互快照。
	//
	// 与读模型（`plane` 那一族）的分界是"有没有请求校验与错误码出口"：这一面要回
	// `session_not_found`，而四条出口共用同一份投影（详情里的 pending/memories 就是那两条列表的
	// 结果），所以它们一起搬、一起持有。
	detail svcsession.DetailSvc
	// interaction 会话交互的**动作面**（P4a 正文第四十一刀起在用例层）：权限/提问决策 + 记忆撤销。
	//
	// 与 detail（只读）分开持有：这一族有副作用（把决定投给底座），两族吃的窄端口也因此不同
	// （`InteractionSource` 纯读 vs `InteractionDelivery` 投递）。
	interaction svcsession.InteractionSvc
	// queue 排队输入的**撤下**用例（P4a 正文第四十二刀起在用例层）：请求形状校验 + 幂等口径 +
	// 「真撤下必须发一条 `session.input_cancelled`」三条语义都在它里面。
	queue svcsession.QueueSvc
	// plane 会话域平面：**只读模型**直接由它提供（P4a 正文第十一刀）。
	//
	// 读模型（列表 / 单条 / 轮次 / 排队输入 / 检索 / 用量）没有请求校验、错误码出口与响应装配，
	// 它们的实现与域自己的锁和状态绑在一起（`ListSessions` 要持 `pl.mu` 遍历注册表、
	// 释放锁后再补齐归属展示字段）。给它们造一层用例只会得到**一行转发**——
	// 那正是 2026-10-16 被判定为"转发骚操作"的形状，所以这一面按 P4b 的原口径收口：
	// 调用点直接依赖域平面（`manager` 侧那份 TODO(P4b) 转发随之删除）。
	plane *session.Plane
}

// NewSessionCtr 构造 SessionCtr。
//
// 拿到的是**依赖事实**（会话面过渡期端口 + 库句柄 + 会话域平面 + 事件总线 + 交互事实来源）：
// 历史读面（第十刀）与详情读面（第四十刀）两个用例在这里造，其余方法的实现仍在 `manager`（逐刀搬）。
func NewSessionCtr(svc svcsession.SessionSvc, st *store.Store, plane *session.Plane, bus *eventbus.Bus,
	src svcsession.InteractionSource, deliver svcsession.InteractionDelivery) SessionCtr {
	return &sessionCtr{
		svc: svc, hist: svcsession.NewHistory(st, plane, bus), plane: plane,
		detail:      svcsession.NewDetail(plane, src),
		interaction: svcsession.NewInteraction(plane, deliver),
		queue:       svcsession.NewQueue(plane),
	}
}

// List 列出全部会话（query `archived` 控制是否包含已归档；默认仅未归档）。
//
// 两个工作空间过滤参数口径不同，都可选：
//   - `cwd`：工作空间**精确等于**该绝对路径的会话（历史行为）；
//   - `workspace_id`：归属该工作空间登记的会话（v14 左栏分组用它；按默认工作空间过滤时
//     同时包含 `workspace_id=”` 的历史会话——那些行读面归默认工作空间）。
func (h *sessionCtr) List(c *gin.Context) {
	archived := c.Query("archived") == "true"
	cwd := strings.TrimSpace(c.Query("cwd"))
	if cwd != "" && !filepath.IsAbs(cwd) {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: cwd 需为绝对路径"))
		return
	}
	workspaceID := strings.TrimSpace(c.Query("workspace_id"))
	sessions, err := h.plane.ListSessions(archived, cwd, workspaceID)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, sessions)
}

// Search 搜索会话（query `q` 必填；`limit` 默认 30 上限 50；含归档；updated 降序）。
func (h *sessionCtr) Search(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: q 必填"))
		return
	}
	limit := 30
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	hits, err := h.plane.SearchSessions(c.Request.Context(), q, limit)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, hits)
}

// Get 返回单会话详情（元数据 + 挂起权限）。
func (h *sessionCtr) Get(c *gin.Context) {
	resp, err := h.detail.GetSession(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Update 更新会话元数据（title/pinned/archived 子集）。
func (h *sessionCtr) Update(c *gin.Context) {
	var req dtosession.SessionPatch
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.UpdateSession(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Permissions 列出会话的挂起权限请求。
func (h *sessionCtr) Permissions(c *gin.Context) {
	resp, err := h.detail.ListPendingPermissions(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// PermissionMemories 列出会话级权限记忆（allow/reject 双侧，详情/撤销 UI 数据源）。
func (h *sessionCtr) PermissionMemories(c *gin.Context) {
	resp, err := h.detail.ListPermissionMemories(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// RevokeMemory 撤销会话级权限记忆（body title+kind 均空 = 一键撤销全部）。
func (h *sessionCtr) RevokeMemory(c *gin.Context) {
	var req dtosession.MemoryRevokeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	if err := h.interaction.RevokeMemory(c.Request.Context(), c.Param("id"), req.Title, req.Kind); err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, gin.H{"title": req.Title, "kind": req.Kind})
}

// ResolvePermission 对挂起权限请求做出决策（allow/reject × once/always）。
func (h *sessionCtr) ResolvePermission(c *gin.Context) {
	var req dtosession.PermissionResolveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.interaction.ResolvePermission(c.Request.Context(), c.Param("id"), req.CallID, req.OptionID, req.Always, req.Allowed)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Elicitations 列出会话的挂起结构化提问（elicitation/create 表单/URL 卡）。
func (h *sessionCtr) Elicitations(c *gin.Context) {
	resp, err := h.detail.ListPendingElicitations(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ResolveElicitation 对挂起结构化提问做出决策（accept/decline/cancel；accept 可带 content）。
func (h *sessionCtr) ResolveElicitation(c *gin.Context) {
	var req dtosession.ElicitationResolveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	if err := h.interaction.ResolveElicitation(c.Request.Context(), c.Param("id"), req.ElicitationID, req.Action, req.Content); err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, gin.H{"elicitation_id": req.ElicitationID, "action": req.Action})
}

// Create 建立会话（agent_id + 可选首轮 prompt）。
// 返回 session id 与 event_seq 水位：前端 WS attach 传 since_seq=event_seq 即可
// 续传首轮增量事件（§4.5 attach/续传语义；EventSeq=0 表示 bus 未启用，attach since=0 走快照）。
func (h *sessionCtr) Create(c *gin.Context) {
	var req dtosession.SessionCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateSession(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Prompt 向既有会话追加一轮 prompt（后台执行，事件经 WS 出流）。
// 会话正忙时按 body 的 delivery 决定：报错（缺省）/ 排队 / 取消当前轮后投递（§6）。
func (h *sessionCtr) Prompt(c *gin.Context) {
	id := c.Param("id")
	var req dtosession.PromptReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.PromptSession(c.Request.Context(), id, req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// QueuedInputs 列出待投递的排队输入（§6 当前态；前端冷挂载/刷新后重建队列视图）。
func (h *sessionCtr) QueuedInputs(c *gin.Context) {
	resp, err := h.plane.ListQueuedInputs(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// DeleteQueuedInput 撤下一条尚未投递的排队输入（§6；已投递/已作废不可撤，幂等）。
func (h *sessionCtr) DeleteQueuedInput(c *gin.Context) {
	id, inputID := c.Param("id"), c.Param("input_id")
	ok, err := h.queue.DeleteQueuedInput(c.Request.Context(), id, inputID)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, gin.H{"id": id, "input_id": inputID, "deleted": ok})
}

// Cancel 取消进行中的 run（幂等）。
func (h *sessionCtr) Cancel(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.CancelRun(c.Request.Context(), id); err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, gin.H{"id": id, "status": "cancelling"})
}

// History 返回会话事件历史（分页）：query `after`（起点 seq，开区间）、
// `before`（反向起点 seq，开区间，用于「加载更早」）、`limit`（默认 200，上限 1000）。
// 事件按 seq 升序；翻页用末条 seq 作下页 after。
//
// **游标口径（v21）**：seq 是**会话内**位次、0 是合法值（`session.created`），所以缺省与
// 显式 0 是两件事：
//   - `after` 缺省 = 从该会话第一条起；显式 `after=0` = 第 0 条之后（跳过它）；
//   - `before` 缺省 = 从最新往回（末尾一页）；显式 `before=0` = 第 0 条之前（空页）。
//
// 两件事由**指针的有无**承载（`nil` = 没给），不是由某个哨兵值承载——`0` 已经是合法游标了。
// 两者都只接受非负整数（负数一律 40000，服务端内部才用 -1 表示"没有游标"）。
func (h *sessionCtr) History(c *gin.Context) {
	id := c.Param("id")
	q := dtosession.HistoryQuery{Limit: 200}
	if v := c.Query("after"); v != "" {
		n, err := parseNonNeg(v)
		if err != nil {
			gincontext.Fail(c, code.Error(code.InvalidParam, "after 需为非负整数"))
			return
		}
		q.After = &n
	}
	if v := c.Query("before"); v != "" {
		n, err := parseNonNeg(v)
		if err != nil {
			gincontext.Fail(c, code.Error(code.InvalidParam, "before 需为非负整数"))
			return
		}
		q.Before = &n
	}
	if q.After != nil && q.Before != nil {
		gincontext.Fail(c, code.Error(code.InvalidRequest, "after 与 before 互斥（一个向后、一个向前）"))
		return
	}
	if v := c.Query("limit"); v != "" {
		n, err := parseNonNeg(v)
		if err != nil {
			gincontext.Fail(c, code.Error(code.InvalidParam, "limit 需为非负整数"))
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
	resp, err := h.hist.SessionHistory(c.Request.Context(), id, q)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Runs 列出某会话的轮次明细（runs 表读模型；内存模式返回空数组）。
func (h *sessionCtr) Runs(c *gin.Context) {
	id := c.Param("id")
	runs, err := h.plane.ListRuns(c.Request.Context(), id)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
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
