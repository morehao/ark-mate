// Package svcworkspace 定义工作空间面的服务端口：目录枚举、
// 最近使用派生、资料库与工作区根白名单的设置面。
package svcworkspace

import "github.com/morehao/ark-mate/internal/dto/dtoworkspace"

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

	// ---- v14：工作空间**登记表**（用户产出的持久实体，与上面几个派生视图分工见 manager/workspace_registry.go）----

	// WorkspaceRegistry 列出全部工作空间登记（按展示顺序；目录状态/会话数/最近使用读时现算）。
	//
	// memory 持久层返回**空列表 + Writable=false**（不是错误）：没有登记是合法状态，
	// 而"不可写"这件事必须让前端看见，否则它会渲染一个必然失败的创建入口（设计 ADR-11）。
	WorkspaceRegistry() (*dtoworkspace.WorkspaceListResp, error)

	// CreateRegisteredWorkspace 登记一个目录（get-or-create；strict=true 时重复报 workspace_exists）。
	//
	// 路径校验与建会话**共用同一条实现**（ValidateWorkspace），所以不会出现"能登记、建不了会话"。
	CreateRegisteredWorkspace(path, title string, strict bool) (*dtoworkspace.WorkspaceResp, error)

	// RenameRegisteredWorkspace 改写展示名（不改 id、不改 path）。
	RenameRegisteredWorkspace(id, title string) (*dtoworkspace.WorkspaceResp, error)

	// DeleteRegisteredWorkspace 删除一条登记（**不删会话、不删目录**；默认工作空间拒绝）。
	DeleteRegisteredWorkspace(id string) (*dtoworkspace.WorkspaceDeleteResp, error)

	// MoveRegisteredWorkspace 重排（beforeID 为空 = 追加到尾部），返回重排后的完整顺序。
	MoveRegisteredWorkspace(id, beforeID string) (*dtoworkspace.WorkspaceMoveResp, error)
}
