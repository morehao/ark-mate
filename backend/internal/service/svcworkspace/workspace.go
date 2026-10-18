package svcworkspace

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
)

// WorkspaceSvc 工作空间面用例端口：登记表、派生视图（浏览/文件/最近）与设置面。
//
// 为什么这一层存在：v3.1 §3.2.1 规则 1 要求控制器不得 import `internal/domain`。
// 域平面已经把 DTO 与错误码折好（`*dtoworkspace.*`），所以这里只复述**窄接口**并把
// 宿主目录选择器收进来——控制器拿不到平面的其余方法（启动期的策略/白名单写入等）。
type WorkspaceSvc interface {
	// ——— 派生视图 ———
	Browse(path string, includeHidden bool) (*dtoworkspace.WorkspaceBrowseResp, error)
	CreateDir(parent, name string) (*dtoworkspace.WorkspaceDirCreateResp, error)
	Recent(limit int) ([]dtoworkspace.WorkspaceRecentResp, error)
	Files(path string, includeHidden bool) (*dtoworkspace.WorkspaceFilesResp, error)
	// PickDirectory 抬一次宿主原生目录选择器（实现见 `domain/workspace.Picker`）。
	PickDirectory(ctx context.Context) (path string, cancelled bool, err error)

	// ——— 设置面 ———
	Settings() *dtoworkspace.WorkspaceSettingsResp
	UpdateSettings(roots []string) (*dtoworkspace.WorkspaceSettingsResp, error)

	// ——— 登记表 ———
	Registry() (*dtoworkspace.WorkspaceListResp, error)
	Create(req dtoworkspace.WorkspaceCreateReq) (*dtoworkspace.WorkspaceResp, error)
	Patch(id string, req dtoworkspace.WorkspacePatchReq) (*dtoworkspace.WorkspaceResp, error)
	Delete(id string) (*dtoworkspace.WorkspaceDeleteResp, error)
	Move(id, beforeID string) (*dtoworkspace.WorkspaceMoveResp, error)
}

// HostPicker 宿主目录选择器的窄端口（由 `*workspace.Picker` 满足）。
type HostPicker interface {
	PickDirectoryHost(ctx context.Context) (string, bool, error)
}

type workspaceSvc struct {
	plane  *workspace.Plane
	picker HostPicker
}

// New 构造工作空间面用例。
func New(plane *workspace.Plane, picker HostPicker) WorkspaceSvc {
	return &workspaceSvc{plane: plane, picker: picker}
}

func (s *workspaceSvc) Browse(path string, includeHidden bool) (*dtoworkspace.WorkspaceBrowseResp, error) {
	return s.plane.BrowseWorkspace(path, includeHidden)
}

func (s *workspaceSvc) CreateDir(parent, name string) (*dtoworkspace.WorkspaceDirCreateResp, error) {
	return s.plane.CreateWorkspaceDir(parent, name)
}

func (s *workspaceSvc) Recent(limit int) ([]dtoworkspace.WorkspaceRecentResp, error) {
	return s.plane.RecentWorkspaces(limit)
}

func (s *workspaceSvc) Files(path string, includeHidden bool) (*dtoworkspace.WorkspaceFilesResp, error) {
	return s.plane.ListWorkspaceFiles(path, includeHidden)
}

func (s *workspaceSvc) PickDirectory(ctx context.Context) (string, bool, error) {
	return s.picker.PickDirectoryHost(ctx)
}

func (s *workspaceSvc) Settings() *dtoworkspace.WorkspaceSettingsResp { return s.plane.Settings() }

func (s *workspaceSvc) UpdateSettings(roots []string) (*dtoworkspace.WorkspaceSettingsResp, error) {
	return s.plane.UpdateSettings(roots)
}

func (s *workspaceSvc) Registry() (*dtoworkspace.WorkspaceListResp, error) {
	return s.plane.WorkspaceRegistry()
}

func (s *workspaceSvc) Create(req dtoworkspace.WorkspaceCreateReq) (*dtoworkspace.WorkspaceResp, error) {
	return s.plane.CreateWorkspace(req)
}

func (s *workspaceSvc) Patch(id string, req dtoworkspace.WorkspacePatchReq) (*dtoworkspace.WorkspaceResp, error) {
	return s.plane.PatchWorkspace(id, req)
}

func (s *workspaceSvc) Delete(id string) (*dtoworkspace.WorkspaceDeleteResp, error) {
	return s.plane.DeleteRegisteredWorkspace(id)
}

func (s *workspaceSvc) Move(id, beforeID string) (*dtoworkspace.WorkspaceMoveResp, error) {
	return s.plane.MoveRegisteredWorkspace(id, beforeID)
}
