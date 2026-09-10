package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/dto/dtoworkspace"
	"github.com/morehao/ark-forge/internal/service/svcerr"
	"github.com/morehao/ark-forge/internal/service/svcworkspace"
	"github.com/morehao/golib/biz/gcontext/gincontext"
	"github.com/morehao/golib/gerror"
)

// WorkspaceHandler 处理工作空间相关的只读 REST 请求（资料库 + 目录浏览 + 最近使用 + 目录内容）。
//
// 存在理由：Web 端没有 Electron 那样的原生目录选择器（AionUi 用 webFsPicker 替代），
// 因此"选一个目录"必须由控制面提供枚举能力；且**只列目录**——工作空间必须是目录
// （见 manager.ValidateWorkspace 的三条件）。
type WorkspaceHandler struct {
	svc    svcworkspace.WorkspaceSvc
	mapper svcerr.Mapper
}

// Browse 列出某绝对路径下的子目录（query `path` 缺省 = 首个工作区根，未配白名单时 = 服务进程家目录；
// `hidden=true` 含隐藏目录）。
//
// 错误一律经 svc.RestErr 映射：越界的 path 必须回 `workspace_outside_roots`（40001），
// 而不是一个笼统的 code=1——面板要据此告诉用户"这个目录不在允许范围内"，与"服务端坏了"是两种事。
func (h *WorkspaceHandler) Browse(c *gin.Context) {
	path := strings.TrimSpace(c.Query("path"))
	includeHidden := c.Query("hidden") == "true"
	resp, err := h.svc.BrowseWorkspace(path, includeHidden)
	if err != nil {
		gincontext.Fail(c, h.mapper.RestErr(err))
		return
	}
	gincontext.Success(c, resp)
}

// Recent 返回最近使用过的工作空间（query `limit` 默认 10、上限 50）。
func (h *WorkspaceHandler) Recent(c *gin.Context) {
	limit := 10
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: limit 需为正整数"})
			return
		}
		if n > 50 {
			n = 50
		}
		limit = n
	}
	list, err := h.svc.RecentWorkspaces(limit)
	if err != nil {
		gincontext.Fail(c, h.mapper.RestErr(err))
		return
	}
	gincontext.Success(c, list)
}

// Libraries 返回资料库列表（工作区根下的一级子目录，只读）。
//
// 无参数：资料库的集合完全由 FORGE_WORKSPACE_ROOTS + 磁盘决定，没有可调项——
// 少一个参数就少一种"面板看到的和运维配的不一致"的可能。
func (h *WorkspaceHandler) Libraries(c *gin.Context) {
	list, err := h.svc.WorkspaceLibraries()
	if err != nil {
		gincontext.Fail(c, h.mapper.RestErr(err))
		return
	}
	gincontext.Success(c, list)
}

// Files 列出某目录的内容（query `path` 必填、`hidden=true` 含隐藏项）。
//
// 与 Browse 的分工：Browse 选工作空间（只列目录，条目可直接当 cwd），
// Files 看资料（列文件 + 目录，带大小/修改时间，条目**不可**当 cwd）。
func (h *WorkspaceHandler) Files(c *gin.Context) {
	path := strings.TrimSpace(c.Query("path"))
	includeHidden := c.Query("hidden") == "true"
	resp, err := h.svc.ListWorkspaceFiles(path, includeHidden)
	if err != nil {
		gincontext.Fail(c, h.mapper.RestErr(err))
		return
	}
	gincontext.Success(c, resp)
}

// Settings 返回工作区根白名单的设置快照（只读；面板「设置」页据此渲染可编辑列表）。
//
// 与 `/workspaces/browse` 返回的 roots 同源但**不同义**：browse 给的是生效名单（含平台代建根，
// 供浏览面把根列成入口），这里额外区分"显式配置的"与"派生的"，否则面板无法表达"删掉这条"。
func (h *WorkspaceHandler) Settings(c *gin.Context) {
	resp, err := h.svc.WorkspaceSettings()
	if err != nil {
		gincontext.Fail(c, h.mapper.RestErr(err))
		return
	}
	gincontext.Success(c, resp)
}

// UpdateSettings 全量替换工作区根白名单（落盘 + 对新会话立即生效）。
//
// 三种拒绝要能分辨：`settings_locked`（被 env/flag 锁定 → 面板转只读）、`settings_readonly`
// （本进程无配置目录）、`workspace_invalid`（某条不是绝对路径 / 不存在 / 不是目录）。
func (h *WorkspaceHandler) UpdateSettings(c *gin.Context) {
	var req dtoworkspace.WorkspaceSettingsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, gerror.Error{Code: 40000, Msg: "参数错误: " + err.Error()})
		return
	}
	resp, err := h.svc.UpdateWorkspaceSettings(req.Roots)
	if err != nil {
		gincontext.Fail(c, h.mapper.RestErr(err))
		return
	}
	gincontext.Success(c, resp)
}
