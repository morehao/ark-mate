// Package svcworkspace 定义工作空间面的服务端口：目录枚举、
// 最近使用派生、资料库与工作区根白名单的设置面。
package svcworkspace

import "github.com/morehao/ark-forge/internal/dto/dtoworkspace"

// WorkspaceSvc 工作空间面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ WorkspaceSvc = (*T)(nil) 保证一致性。
type WorkspaceSvc interface {
	// BrowseWorkspace 浏览某绝对路径下的**子目录**（只读；path 空 = 服务进程家目录）。
	// Web 端无原生目录选择器，故由控制面提供枚举能力。
	BrowseWorkspace(path string, includeHidden bool) (*dtoworkspace.WorkspaceBrowseResp, error)

	// RecentWorkspaces 返回最近使用过的工作空间（**从既有会话派生**，不额外维护状态：
	// 排除平台代建的临时空间，按最近使用倒序去重，供选择器"最近"一栏）。
	RecentWorkspaces(limit int) ([]dtoworkspace.WorkspaceRecentResp, error)

	// WorkspaceLibraries 返回资料库列表（工作区根下的一级子目录，零新状态：
	// 目录名即资料库名，让资料/文档场景的用户不必面对绝对路径）。
	WorkspaceLibraries() ([]dtoworkspace.WorkspaceLibraryResp, error)

	// ListWorkspaceFiles 列出某目录的**内容**（文件 + 子目录，只读；与 BrowseWorkspace 分工：
	// 那个用于"选工作空间"，这个用于"看资料"）。
	ListWorkspaceFiles(path string, includeHidden bool) (*dtoworkspace.WorkspaceFilesResp, error)

	// WorkspaceSettings 返回工作区根白名单的设置快照（生效名单 + 显式配置名单 + 来源/锁定态）。
	WorkspaceSettings() (*dtoworkspace.WorkspaceSettingsResp, error)

	// UpdateWorkspaceSettings 全量替换工作区根白名单（落盘 + 热更新；被更高优先级来源锁定时拒绝）。
	UpdateWorkspaceSettings(roots []string) (*dtoworkspace.WorkspaceSettingsResp, error)
}
