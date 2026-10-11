// Package svcworkspace 定义工作空间面的服务端口：目录枚举与创建、
// 最近使用派生、以及**起始点偏好**的设置面。
package svcworkspace

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
)

// WorkspaceSvc 工作空间面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ WorkspaceSvc = (*T)(nil) 保证一致性。
type WorkspaceSvc interface {
	// BrowseWorkspace 浏览某绝对路径下的**子目录**（只读；path 空 = 服务进程家目录）。
	// Web 端无原生目录选择器，故由控制面提供枚举能力。
	BrowseWorkspace(path string, includeHidden bool) (*dtoworkspace.WorkspaceBrowseResp, error)

	// NativePickerMode 宿主该用哪一套选目录交互：native（能抬系统面板）/ browse（应用内浏览）。
	//
	// 判据是宿主事实（绑回环 / 非 SSH / 有显示会话），**与客户端形态无关**——这是"完全参照
	// 参照实现"的那条：本机浏览器打开控制台也能用上系统面板（见 internal/manager/workspace_picker.go）。
	NativePickerMode() string

	// PickDirectoryHost 抬一次**宿主原生**目录选择器（系统面板）。
	//
	// 返回 (path, false) = 选定；( "", true) = 取消（或超时收回，取消不是错误）；
	// 宿主没有可用选择器 → `native_pick_unsupported`（可判定，前端据此回落应用内浏览）。
	PickDirectoryHost(ctx context.Context) (string, bool, error)

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
