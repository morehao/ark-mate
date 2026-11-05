package svcworkspace

import (
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
)

// ——— 工作空间域视图 → 对外 DTO 的投影（v3.2 刀 6）———
//
// 逐字段搬运、不重命名。没有 `omitempty` 的切片一律 `make(...,0,len)`
// （`entries` / `items` / `ids` 必须序列化成 `[]`）；有 `omitempty` 的保持 nil。

func workspaceResp(v workspace.WorkspaceItem) dtoworkspace.WorkspaceResp {
	return dtoworkspace.WorkspaceResp{
		ID:         v.ID,
		Path:       v.Path,
		Title:      v.Title,
		IsDefault:  v.IsDefault,
		ListOrder:  v.ListOrder,
		DirStatus:  v.DirStatus,
		Sessions:   v.Sessions,
		LastUsedAt: v.LastUsedAt,
		CreatedAt:  v.CreatedAt,
		UpdatedAt:  v.UpdatedAt,
	}
}

func workspaceResps(vs []workspace.WorkspaceItem) []dtoworkspace.WorkspaceResp {
	out := make([]dtoworkspace.WorkspaceResp, 0, len(vs))
	for _, v := range vs {
		out = append(out, workspaceResp(v))
	}
	return out
}

func workspaceListResp(v *workspace.WorkspaceList) *dtoworkspace.WorkspaceListResp {
	if v == nil {
		return nil
	}
	return &dtoworkspace.WorkspaceListResp{
		Items:        workspaceResps(v.Items),
		DefaultID:    v.DefaultID,
		Writable:     v.Writable,
		ProvisionDir: v.ProvisionDir,
		CreateRoot:   v.CreateRoot,
	}
}

func workspaceDeleteResp(v *workspace.WorkspaceDelete) *dtoworkspace.WorkspaceDeleteResp {
	if v == nil {
		return nil
	}
	return &dtoworkspace.WorkspaceDeleteResp{ID: v.ID, ReleasedSessions: v.ReleasedSessions}
}

func workspaceMoveResp(v *workspace.WorkspaceMove) *dtoworkspace.WorkspaceMoveResp {
	if v == nil {
		return nil
	}
	ids := make([]string, 0, len(v.IDs))
	ids = append(ids, v.IDs...)
	return &dtoworkspace.WorkspaceMoveResp{IDs: ids}
}

func workspaceDirCreateResp(v *workspace.WorkspaceDirCreate) *dtoworkspace.WorkspaceDirCreateResp {
	if v == nil {
		return nil
	}
	return &dtoworkspace.WorkspaceDirCreateResp{Path: v.Path}
}

func recentWorkspaceResps(vs []workspace.RecentWorkspace) []dtoworkspace.WorkspaceRecentResp {
	out := make([]dtoworkspace.WorkspaceRecentResp, 0, len(vs))
	for _, v := range vs {
		out = append(out, dtoworkspace.WorkspaceRecentResp{
			Path:       v.Path,
			Name:       v.Name,
			LastUsedAt: v.LastUsedAt,
			Sessions:   v.Sessions,
		})
	}
	return out
}

func browseViewResp(v *workspace.BrowseView) *dtoworkspace.WorkspaceBrowseResp {
	if v == nil {
		return nil
	}
	var crumbs []dtoworkspace.WorkspaceCrumbResp
	if len(v.Crumbs) > 0 {
		crumbs = make([]dtoworkspace.WorkspaceCrumbResp, 0, len(v.Crumbs))
		for _, c := range v.Crumbs {
			crumbs = append(crumbs, dtoworkspace.WorkspaceCrumbResp{Name: c.Name, Path: c.Path, Hidden: c.Hidden})
		}
	}
	entries := make([]dtoworkspace.WorkspaceEntryResp, 0, len(v.Entries))
	for _, e := range v.Entries {
		entries = append(entries, dtoworkspace.WorkspaceEntryResp{Name: e.Name, Path: e.Path, Hidden: e.Hidden})
	}
	return &dtoworkspace.WorkspaceBrowseResp{
		Path:      v.Path,
		Parent:    v.Parent,
		Home:      v.Home,
		Roots:     append([]string(nil), v.Roots...),
		Crumbs:    crumbs,
		Truncated: v.Truncated,
		Entries:   entries,
	}
}

func filesViewResp(v *workspace.FilesView) *dtoworkspace.WorkspaceFilesResp {
	if v == nil {
		return nil
	}
	entries := make([]dtoworkspace.WorkspaceFileEntryResp, 0, len(v.Entries))
	for _, e := range v.Entries {
		entries = append(entries, dtoworkspace.WorkspaceFileEntryResp{
			Name:       e.Name,
			Path:       e.Path,
			IsDir:      e.IsDir,
			Size:       e.Size,
			ModifiedAt: e.ModifiedAt,
		})
	}
	return &dtoworkspace.WorkspaceFilesResp{
		Path:    v.Path,
		Roots:   append([]string(nil), v.Roots...),
		Entries: entries,
	}
}

// createRequest 对外请求 → 领域入参。
func createRequest(req dtoworkspace.WorkspaceCreateReq) workspace.CreateRequest {
	return workspace.CreateRequest{Path: req.Path, Name: req.Name, Title: req.Title, Strict: req.Strict}
}

// patchRequest 对外请求 → 领域入参。
func patchRequest(req dtoworkspace.WorkspacePatchReq) workspace.PatchRequest {
	return workspace.PatchRequest{
		Title:                 req.Title,
		DefaultCapability:     req.DefaultCapability,
		DefaultPermissionMode: req.DefaultPermissionMode,
		Path:                  req.Path,
	}
}
