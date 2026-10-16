// Package ctrworkspace 承载工作空间面控制器（目录枚举/建目录/最近使用/起始点偏好设置）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrworkspace

import (
	"context"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
	"github.com/morehao/ark-mate/internal/service/svcworkspace"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// WorkspaceCtr 工作空间面控制器（目录枚举/建目录/最近使用/起始点偏好设置）。
type WorkspaceCtr interface {
	Browse(c *gin.Context)
	CreateDir(c *gin.Context)
	PickDirectory(c *gin.Context)
	Recent(c *gin.Context)
	Files(c *gin.Context)
	Settings(c *gin.Context)
	UpdateSettings(c *gin.Context)

	// v14：工作空间登记表（有 id、可改写与删除的持久实体）。
	List(c *gin.Context)
	Create(c *gin.Context)
	Rename(c *gin.Context)
	Delete(c *gin.Context)
	Move(c *gin.Context)
}

var _ WorkspaceCtr = (*workspaceCtr)(nil)

// workspaceCtr 处理工作空间相关的只读 REST 请求（目录浏览 + 最近使用 + 目录内容）与目录创建。
// 存在理由：Web 端没有 Electron 那样的原生目录选择器（AionUi 用 webFsPicker 替代），
// 因此"选一个目录"必须由控制面提供枚举能力；且**只列目录**——工作空间必须是目录
// （见 workspace.ValidateWorkspace 的三条件）。
// DirectoryPicker 宿主目录选择器**窄端口**（控制器侧声明）。
//
// 生产传装配好的那个 `*workspace.Picker`（自持 bindAddr 事实）；测试传替身——"选定 / 取消"
// 两个结局要真的 spawn 宿主命令（osascript/zenity），路由级跑不出来。端口不放 svc 层：域里的
// `Picker` 已经把判定、配方、错误码全做完（P2a-16），用例层再包一层就只是一行转发（第十一刀口径）。
type DirectoryPicker interface {
	PickDirectoryHost(ctx context.Context) (string, bool, error)
}

type workspaceCtr struct {
	svc svcworkspace.WorkspaceSvc
	// picker 宿主目录选择器（事实来自 `router.Ports.Picker`）。
	picker DirectoryPicker
	// plane 工作空间域平面：**读模型**（登记表列表）直接经它取——域里已经折好 DTO，
	// 用例层再包一层就只是一行转发（第十一刀口径）。
	plane *workspace.Plane
}

// NewWorkspaceCtr 构造 WorkspaceCtr。
func NewWorkspaceCtr(svc svcworkspace.WorkspaceSvc, picker DirectoryPicker, plane *workspace.Plane) WorkspaceCtr {
	return &workspaceCtr{svc: svc, picker: picker, plane: plane}
}

// Browse 列出某绝对路径下的子目录（query `path` 缺省 = 首个工作区根，未配白名单时 = 服务进程家目录；
// `hidden=true` 含隐藏目录）。
//
// 错误一律经 svc.RestErr 映射：路径不可用必须回 `workspace_invalid`（40001），
// 而不是一个笼统的 code=1——面板要据此告诉用户"这个目录不存在/不是目录"，与"服务端坏了"是两种事。
// v14.5 起不再有"越界"这一类拒绝（名录已降级为浏览起点偏好，选工作区是全系统）。
func (h *workspaceCtr) Browse(c *gin.Context) {
	path := strings.TrimSpace(c.Query("path"))
	includeHidden := c.Query("hidden") == "true"
	resp, err := h.svc.BrowseWorkspace(path, includeHidden)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// CreateDir 在某个已存在的目录下建一个子目录（`POST /workspaces/dirs`，目录浏览面的「新建文件夹」）。
//
// 两种拒绝要能分辨：名字非法（含分隔符 / "." / ".."）→ acl.ClassRequest 的既有投影(40001)；
// 同名目录已存在 → workspace_exists(40901)。最后一种必须是 40901 而不是 40001：前者的处置是
// "换个名字"，后者是"改参数"，界面上的引导（改名重试 vs 报错）不同。
func (h *workspaceCtr) CreateDir(c *gin.Context) {
	var req dtoworkspace.WorkspaceDirCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateWorkspaceDir(req.Parent, req.Name)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// PickDirectory 抬一次**宿主原生**目录选择器（`POST /workspaces/pick-directory`）。
//
// 为什么这个端点是 POST 而不是 GET：它有副作用（在用户屏幕上开一个系统面板），而且**不可缓存**。
//
// 三种结局各自可判定：选定 → `{path}`；取消 → `{cancelled:true}`（**不是错误**，前端静默收手）；
// 宿主没有可用选择器 → `native_pick_unsupported`（前端回落应用内浏览）。
//
// 前端本不该在"能力声明说没有"时打到这里（`/capabilities` 的 `native-dialog` 就是干这个的），
// 所以这个错是"声明与实际不一致"的兜底——按可判定错误回，而不是笼统 50000。
func (h *workspaceCtr) PickDirectory(c *gin.Context) {
	path, cancelled, err := h.picker.PickDirectoryHost(c.Request.Context())
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, dtoworkspace.WorkspacePickResp{Path: path, Cancelled: cancelled})
}

// Recent 返回最近使用过的工作空间（query `limit` 默认 10、上限 50）。
func (h *workspaceCtr) Recent(c *gin.Context) {
	limit := 10
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: limit 需为正整数"))
			return
		}
		if n > 50 {
			n = 50
		}
		limit = n
	}
	list, err := h.svc.RecentWorkspaces(limit)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, list)
}

// Files 列出某目录的内容（query `path` 必填、`hidden=true` 含隐藏项）。
//
// 与 Browse 的分工：Browse 选工作空间（只列目录，条目可直接当 cwd），
// Files 看资料（列文件 + 目录，带大小/修改时间，条目**不可**当 cwd）。
func (h *workspaceCtr) Files(c *gin.Context) {
	path := strings.TrimSpace(c.Query("path"))
	includeHidden := c.Query("hidden") == "true"
	resp, err := h.svc.ListWorkspaceFiles(path, includeHidden)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Settings 返回工作区根白名单的设置快照（只读；面板「设置」页据此渲染可编辑列表）。
//
// 与 `/workspaces/browse` 返回的 roots 同源但**不同义**：browse 给的是生效名单（含平台代建根，
// 供浏览面把它们列成快捷入口），这里额外区分"显式配置的"与"派生的"，否则面板无法表达"删掉这条"。
func (h *workspaceCtr) Settings(c *gin.Context) {
	// 快照从**域平面**取（P4a 正文第十九刀）：域里已折好 DTO（含 configured / prefer_roots /
	// source / locked_by），用例层再包一层就只是一行转发；它没有错误出口（读自己的状态）。
	gincontext.Success(c, h.plane.Settings())
}

// UpdateSettings 全量替换工作区根白名单（落盘 + 对新会话立即生效）。
//
// 三种拒绝要能分辨：`settings_locked`（被 env/flag 锁定 → 面板转只读）、`settings_readonly`
// （本进程无配置目录）、`workspace_invalid`（某条不是绝对路径 / 不存在 / 不是目录）。
func (h *workspaceCtr) UpdateSettings(c *gin.Context) {
	var req dtoworkspace.WorkspaceSettingsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.UpdateWorkspaceSettings(req.Roots)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// ---- v14：工作空间登记表（用户产出的持久实体）----
//
// 与上面几个只读面的分工：browse/files/libraries/recent 是**派生视图**（从磁盘或从既有会话
// 算出来的），下面这组是**登记项**本身——有 id、能被改写与删除。选择器的主列表取 List。

// List 列出全部工作空间登记（`GET /workspaces`）。
//
// 无参数：顺序与集合都由库里的登记决定，没有可调项——少一个参数就少一种"面板看到的和库里
// 不一致"的可能（与 Libraries 同一条理由）。
func (h *workspaceCtr) List(c *gin.Context) {
	resp, err := h.plane.WorkspaceRegistry()
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Create 登记一个目录为工作空间（`POST /workspaces`）。
//
// 默认 get-or-create（重复登记返回既有那一行）；`strict=true` 时才报 workspace_exists。
// 目录不存在 / 不是目录 / 落在白名单外 → 40000（三种 code 名可分辨，见 workspace.ValidateWorkspace）。
func (h *workspaceCtr) Create(c *gin.Context) {
	var req dtoworkspace.WorkspaceCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateRegisteredWorkspace(req.Path, req.Title, req.Strict)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Rename 改写展示名（`PATCH /workspaces/:id`）。
func (h *workspaceCtr) Rename(c *gin.Context) {
	var req dtoworkspace.WorkspacePatchReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.plane.RenameRegisteredWorkspace(c.Param("id"), req.Title)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Delete 删除一条登记（`DELETE /workspaces/:id`）。
//
// **不删会话、不删目录**：目录与文件一概不动，其会话读面归到默认工作空间。响应回
// released_sessions 让界面能把这件事说清楚（"这 12 个会话回到了默认工作空间"）。
func (h *workspaceCtr) Delete(c *gin.Context) {
	resp, err := h.plane.DeleteRegisteredWorkspace(c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Move 重排（`POST /workspaces/:id/move`，body `{before_id?}`；空 = 追加到尾部）。
func (h *workspaceCtr) Move(c *gin.Context) {
	var req dtoworkspace.WorkspaceMoveReq
	// body 可缺省（追加到尾部），所以空 body 不是错误：`{}` 与"没传 body"同义。
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
			return
		}
	}
	resp, err := h.plane.MoveRegisteredWorkspace(c.Param("id"), req.BeforeID)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
