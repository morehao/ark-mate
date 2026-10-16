// Package svcworkspace 定义工作空间面的服务端口：目录枚举与创建、
// 最近使用派生、以及**起始点偏好**的设置面。
package svcworkspace

import (
	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
)

// WorkspaceSvc 工作空间面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ WorkspaceSvc = (*T)(nil) 保证一致性。
type WorkspaceSvc interface {
	// BrowseWorkspace 浏览某绝对路径下的**子目录**（只读；path 空 = 服务进程家目录）。
	// Web 端无原生目录选择器，故由控制面提供枚举能力。
	BrowseWorkspace(path string, includeHidden bool) (*dtoworkspace.WorkspaceBrowseResp, error)

	// CreateWorkspaceDir 在某个已存在的目录下建一个子目录（目录浏览面的「新建文件夹」）。
	//
	// 父目录走与建会话同一条校验（ValidateWorkspace + 根白名单），名字限单段；
	// 同名目录已存在 → workspace_exists(40901)，绝不静默复用。
	CreateWorkspaceDir(parent, name string) (*dtoworkspace.WorkspaceDirCreateResp, error)

	// RecentWorkspaces 返回最近使用过的工作空间（**从既有会话派生**，不额外维护状态：
	// 排除平台代建的临时空间，按最近使用倒序去重，供选择器"最近"一栏）。
	RecentWorkspaces(limit int) ([]dtoworkspace.WorkspaceRecentResp, error)

	// ListWorkspaceFiles 列出某目录的**内容**（文件 + 子目录，只读；与 BrowseWorkspace 分工：
	// 那个用于"选工作空间"，这个用于"看资料"）。
	ListWorkspaceFiles(path string, includeHidden bool) (*dtoworkspace.WorkspaceFilesResp, error)

	// UpdateWorkspaceSettings 全量替换工作区根白名单（落盘 + 热更新；被更高优先级来源锁定时拒绝）。
	UpdateWorkspaceSettings(roots []string) (*dtoworkspace.WorkspaceSettingsResp, error)

	// ---- v14：工作空间**登记表**（用户产出的持久实体，与上面几个派生视图分工见 manager/workspace_registry.go）----

	// **写面的三条（Rename/Delete/Move）刻意不在这个接口里**（P4a 正文第二十七刀）：它们的
	// 投影（DTO 与三个错误码）都在域平面里，控制器直接经 `h.plane.*` 调用；接口里再留一份声明，
	// 只是给"一行转发"留位置。

	// CreateRegisteredWorkspace 登记一个目录（get-or-create；strict=true 时重复报 workspace_exists）。
	//
	// 路径校验与建会话**共用同一条实现**（ValidateWorkspace），所以不会出现"能登记、建不了会话"。
	CreateRegisteredWorkspace(path, title string, strict bool) (*dtoworkspace.WorkspaceResp, error)
}
