package router_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/dto/dtoautomation"
	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/internal/dto/dtoexport"
	"github.com/morehao/ark-mate/internal/dto/dtolibrary"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/dto/dtoruntime"
	"github.com/morehao/ark-mate/internal/dto/dtosearch"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/dto/dtousage"
	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/ark-mate/internal/service/svclibrary"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/gerror"
)

// ——— stub Service（handler.Service 的测试替身，记录调用） ———

type stubSvc struct {
	sessions []dtosession.SessionResp

	listSessionsErr  error
	listCwd          string // 最近一次 ListSessions 的工作空间过滤参数
	listWorkspaceID  string // 最近一次 ListSessions 的归属过滤参数（v14）
	listArchivedOnly bool   // 最近一次 ListSessions 的 archived 参数

	browsePath   string // 最近一次 BrowseWorkspace 的 path 参数
	browseHidden bool   // 最近一次 BrowseWorkspace 的 hidden 参数
	filesPath    string // 最近一次 ListWorkspaceFiles 的 path 参数
	filesHidden  bool   // 最近一次 ListWorkspaceFiles 的 hidden 参数
	filesResp    *dtoworkspace.WorkspaceFilesResp
	filesErr     error
	browseResp   *dtoworkspace.WorkspaceBrowseResp
	browseErr    error
	// v14.3 目录浏览面的写面（「新建文件夹」）。
	dirParent string // 最近一次 CreateWorkspaceDir 的 parent 参数
	dirName   string // 最近一次 CreateWorkspaceDir 的 name 参数
	dirResp   *dtoworkspace.WorkspaceDirCreateResp
	dirErr    error
	// v14.5 宿主原生目录选择器（桩只记参 + 透传；判定与 spawn 全在 manager）。
	pickerMode    string
	pickCalled    bool
	pickPath      string
	pickCancelled bool
	pickErr       error
	recentLimit   int // 最近一次 RecentWorkspaces 的 limit 参数
	recentResp    []dtoworkspace.WorkspaceRecentResp
	recentErr     error

	settingsResp   *dtoworkspace.WorkspaceSettingsResp
	settingsErr    error
	settingsCalled bool     // UpdateWorkspaceSettings 是否被调用（"传了空名单"与"没调用"必须能区分）
	settingsRoots  []string // 最近一次 UpdateWorkspaceSettings 收到的 roots

	// v14 工作空间登记表（桩只记参 + 透传；领域判断全在 manager）。
	registryList         *dtoworkspace.WorkspaceListResp
	registryOne          *dtoworkspace.WorkspaceResp
	registryDelete       *dtoworkspace.WorkspaceDeleteResp
	registryMove         *dtoworkspace.WorkspaceMoveResp
	registryErr          error
	registryListCalled   bool
	registryCreatePath   string
	registryCreateTitle  string
	registryCreateStrict bool
	registryRenameTitle  string
	registryID           string
	registryMoveBefore   string

	diagResp *dtoruntime.DiagnosticsResp
	diagErr  error

	searchMsgQuery string
	searchMsgLimit int
	searchMsgResp  *dtosearch.MessageSearchResp
	searchMsgErr   error

	exportReq  dtoexport.ExportReq
	exportResp *dtoexport.ExportResp
	exportErr  error

	// automation 面（M4-5）桩状态
	automations    []dtoautomation.AutomationResp
	autoErr        error
	autoAction     string // 最近一次写面动作（create/update/delete/run）
	autoID         string // 最近一次写面/取单条的 id
	autoGot        string // 最近一次 GetAutomation/ListAutomationRuns 的 id
	autoReq        dtoautomation.AutomationWriteReq
	autoPreviewReq dtoautomation.AutomationPreviewReq

	aclErr          error                              // 任意端点的裸 acl 错误注入（信封映射用）
	resolvePermErr  *gerror.Error                      // 权限决策桩错误注入
	resolvePermResp *dtosession.PermissionDecisionResp // 权限决策桩回执（nil = 默认构造）

	providerErr error // P2 provider 桩错误注入
	providers   []dtoprovider.ProviderResp

	createCalled bool
	createReq    dtosession.SessionCreateReq
	createResp   *dtosession.SessionCreateResp
	createErr    error

	promptCalled    bool
	promptID        string
	promptText      string
	promptDelivery  string
	inputDeleted    bool
	inputDeleteID   string
	inputDeleteConv string
	inputsListed    bool
	inputsListConv  string
	promptResp      *dtosession.SessionPromptResp
	promptErr       error

	cancelCalled bool
	cancelID     string
	cancelErr    error

	historyCalled bool
	historyID     string
	historyAfter  *int64
	historyBefore *int64
	historyLimit  int
	historyResp   *dtosession.SessionHistoryResp
	historyErr    error

	memList     []dtosession.MemoryResp
	revokeID    string
	revokeTitle string
	revokeKind  string
	revokeErr   error

	elicitations        []dtosession.ElicitationResp
	resolvedElicID      string
	resolvedElicAction  string
	resolvedElicContent map[string]any
	resolveElicErr      error

	skills []dtocapability.SkillResp // P1 能力数据面桩
	// §13.4 S4 启停桩
	matQueried         string
	skillToggled       bool
	skillToggleID      string
	skillToggleEnabled bool
	mcps               []dtocapability.McpServerResp
	tools              []dtocapability.ToolResp
	capErr             error // 能力列表桩错误注入

	// M2 写面桩
	skillWritten  string // create|update|delete
	skillWriteID  string
	skillWriteReq dtocapability.SkillWriteReq
	mcpWritten    string
	mcpWriteID    string
	mcpWriteReq   dtocapability.McpWriteReq

	// M2-4 Library 桩
	libraryQueried svclibrary.Query

	probeResp *dtocapability.McpProbeResp // P2 §5.2 MCP live 探测桩
	probeErr  error
	probeID   string

	callMcpResp *dtocapability.McpToolCallResp // B1 tools/call 桩
	callMcpErr  error
	callMcpID   string
	callMcpTool string
	callMcpReq  dtocapability.McpToolCallReq

	mcpLiveID   string // B2 资源/提示词桩
	mcpLiveURI  string
	mcpLiveName string
	mcpLiveArgs map[string]string
	mcpLiveErr  error

	usageResp  *dtousage.UsageSummaryResp // M3 GET /usage/summary 桩
	usageQuery dtousage.UsageQueryReq     // 捕获查询参数
	usageErr   error
}

// RestErr 桩：与 manager 一致——已映射错误透传，acl 错误按分类映射（测试只关心 acl→信封码）。
func (s *stubSvc) RestErr(err error) error {
	if err == nil {
		return nil
	}
	var ge gerror.Error
	if errors.As(err, &ge) {
		return err
	}
	var ae *acl.Error
	if errors.As(err, &ae) {
		code := 40001
		switch ae.Class {
		case acl.ClassUnsupported:
			code = 40501
		case acl.ClassLimit:
			code = 40901
		case acl.ClassService, acl.ClassConnection:
			code = 50201
		}
		if ae.Code == "agent_not_found" || ae.Code == "session_not_found" || ae.Code == "runtime_not_found" || ae.Code == "mcp_not_found" {
			code = 40401
		}
		return gerror.Error{Code: code, Msg: ae.Details}
	}
	return gerror.Error{Code: 50000, Msg: err.Error()}
}

func (s *stubSvc) ListProviders(context.Context) ([]dtoprovider.ProviderResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return s.providers, nil
}
func (s *stubSvc) CreateProvider(_ context.Context, req dtoprovider.ProviderCreateReq) (*dtoprovider.ProviderResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &dtoprovider.ProviderResp{ID: req.ID, Protocol: req.Protocol, HasKey: req.APIKey != nil && *req.APIKey != ""}, nil
}
func (s *stubSvc) UpdateProvider(_ context.Context, id string, patch dtoprovider.ProviderPatchReq) (*dtoprovider.ProviderResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &dtoprovider.ProviderResp{ID: id, HasKey: patch.APIKey != nil && *patch.APIKey != ""}, nil
}
func (s *stubSvc) DeleteProvider(_ context.Context, id string) (*dtoprovider.ProviderDeleteResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &dtoprovider.ProviderDeleteResp{Deleted: true}, nil
}
func (s *stubSvc) ProbeProvider(_ context.Context, id string, req dtoprovider.ProviderProbeReq) (*dtoprovider.ProviderProbeResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &dtoprovider.ProviderProbeResp{ID: id, Upstream: req.Upstream, OK: true}, nil
}
func (s *stubSvc) ListModels(_ context.Context) ([]dtomodel.ModelResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return nil, nil
}
func (s *stubSvc) CreateModel(_ context.Context, req dtomodel.ModelCreateReq) (*dtomodel.ModelResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &dtomodel.ModelResp{ID: "m1", ProviderID: req.ProviderID, Model: req.Model}, nil
}
func (s *stubSvc) UpdateModel(_ context.Context, id string, patch dtomodel.ModelPatchReq) (*dtomodel.ModelResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &dtomodel.ModelResp{ID: id}, nil
}
func (s *stubSvc) DeleteModel(_ context.Context, id string) (*dtomodel.ModelDeleteResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &dtomodel.ModelDeleteResp{Deleted: true}, nil
}
func (s *stubSvc) SyncModels(_ context.Context, runtimeID string) (*dtomodel.ModelSyncResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &dtomodel.ModelSyncResp{Applied: []dtomodel.RuntimeSyncResp{{Runtime: runtimeID, Pending: true}}}, nil
}
func (s *stubSvc) ModelsView(_ context.Context, runtimeID string, refresh bool) (*dtomodel.ModelsResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &dtomodel.ModelsResp{}, nil
}
func (s *stubSvc) ModelCatalog(_ context.Context) (*dtomodel.ModelCatalogResp, error) {
	if s.providerErr != nil {
		return nil, s.providerErr
	}
	return &dtomodel.ModelCatalogResp{Runtime: "stub", Source: "none"}, nil
}

// ——— P1 能力数据面桩（ListSkills/ListMcpServers/ListTools） ———
func (s *stubSvc) ListSkills() ([]dtocapability.SkillResp, error) {
	if s.capErr != nil {
		return nil, s.capErr
	}
	return s.skills, nil
}
func (s *stubSvc) SetSkillEnabled(_ context.Context, id string, enabled bool) (*dtocapability.SkillToggleResp, error) {
	s.skillToggled, s.skillToggleID, s.skillToggleEnabled = true, id, enabled
	if s.capErr != nil {
		return nil, s.capErr
	}
	return &dtocapability.SkillToggleResp{ID: id, Enabled: enabled, NextAction: "已生效"}, nil
}

// M2 写面桩：只记录被调用与入参，返回固定结果（真实的落盘/闸门/回滚在 manager 的单测里验）。
func (s *stubSvc) CreateSkill(_ context.Context, req dtocapability.SkillWriteReq) (*dtocapability.SkillWriteResp, error) {
	s.skillWritten, s.skillWriteReq = "create", req
	if s.capErr != nil {
		return nil, s.capErr
	}
	return &dtocapability.SkillWriteResp{ID: "s1", Path: "/tmp/skills/s1.json", Created: true, NextAction: "已新建"}, nil
}

func (s *stubSvc) UpdateSkill(_ context.Context, id string, req dtocapability.SkillWriteReq) (*dtocapability.SkillWriteResp, error) {
	s.skillWritten, s.skillWriteID, s.skillWriteReq = "update", id, req
	if s.capErr != nil {
		return nil, s.capErr
	}
	return &dtocapability.SkillWriteResp{ID: id, Path: "/tmp/skills/" + id + ".json", NextAction: "已覆盖写"}, nil
}

func (s *stubSvc) DeleteSkill(_ context.Context, id string) (*dtocapability.SkillDeleteResp, error) {
	s.skillWritten, s.skillWriteID = "delete", id
	if s.capErr != nil {
		return nil, s.capErr
	}
	return &dtocapability.SkillDeleteResp{ID: id, Deleted: true, Path: "/tmp/skills/" + id + ".json",
		NextAction: "已删除"}, nil
}

func (s *stubSvc) CreateMcpServer(_ context.Context, req dtocapability.McpWriteReq) (*dtocapability.McpWriteResp, error) {
	s.mcpWritten, s.mcpWriteReq = "create", req
	if s.capErr != nil {
		return nil, s.capErr
	}
	return &dtocapability.McpWriteResp{ID: "m1", Path: "/tmp/mcp/m1.json", Created: true, NextAction: "已新建"}, nil
}

func (s *stubSvc) UpdateMcpServer(_ context.Context, id string, req dtocapability.McpWriteReq) (*dtocapability.McpWriteResp, error) {
	s.mcpWritten, s.mcpWriteID, s.mcpWriteReq = "update", id, req
	if s.capErr != nil {
		return nil, s.capErr
	}
	return &dtocapability.McpWriteResp{ID: id, Path: "/tmp/mcp/" + id + ".json", NextAction: "已覆盖写"}, nil
}

func (s *stubSvc) DeleteMcpServer(_ context.Context, id string) (*dtocapability.McpDeleteResp, error) {
	s.mcpWritten, s.mcpWriteID = "delete", id
	if s.capErr != nil {
		return nil, s.capErr
	}
	return &dtocapability.McpDeleteResp{ID: id, Deleted: true, Path: "/tmp/mcp/" + id + ".json",
		ToolsRemoved: 2, NextAction: "已删除"}, nil
}

func (s *stubSvc) SkillMaterialization(id string) (*dtocapability.SkillMaterializationResp, error) {
	s.matQueried = id
	if s.capErr != nil {
		return nil, s.capErr
	}
	return &dtocapability.SkillMaterializationResp{
		ID: id, Enabled: true,
		States: []dtocapability.SkillMaterializationState{
			{Root: "/tmp/skills", Runtime: "rtx", Dir: "/tmp/skills/" + id,
				Status: "in_sync", Hash: "abc", ActualHash: "abc"},
		},
	}, nil
}

func (s *stubSvc) ListMcpServers() ([]dtocapability.McpServerResp, error) {
	if s.capErr != nil {
		return nil, s.capErr
	}
	return s.mcps, nil
}
func (s *stubSvc) ListTools() ([]dtocapability.ToolResp, error) {
	if s.capErr != nil {
		return nil, s.capErr
	}
	return s.tools, nil
}
func (s *stubSvc) UsageSummary(_ context.Context, q dtousage.UsageQueryReq) (*dtousage.UsageSummaryResp, error) {
	s.usageQuery = q
	if s.usageErr != nil {
		return nil, s.usageErr
	}
	if s.usageResp != nil {
		return s.usageResp, nil
	}
	return &dtousage.UsageSummaryResp{GroupBy: "agent", Source: "sqlite", Buckets: []dtousage.UsageBucketResp{}}, nil
}
func (s *stubSvc) CallMcpTool(_ context.Context, id, tool string, req dtocapability.McpToolCallReq) (*dtocapability.McpToolCallResp, error) {
	s.callMcpID, s.callMcpTool, s.callMcpReq = id, tool, req
	if s.callMcpErr != nil {
		return nil, s.callMcpErr
	}
	if s.callMcpResp != nil {
		return s.callMcpResp, nil
	}
	return &dtocapability.McpToolCallResp{ServerID: id, Tool: tool, Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"ok"}`)}, Text: "ok"}, nil
}
func (s *stubSvc) ListMcpResources(_ context.Context, id string) (*dtocapability.McpResourcesResp, error) {
	s.mcpLiveID = id
	if s.mcpLiveErr != nil {
		return nil, s.mcpLiveErr
	}
	return &dtocapability.McpResourcesResp{ServerID: id, Resources: []dtocapability.McpResourceResp{{URI: "file:///tmp/a.txt", MimeType: "text/plain"}}}, nil
}
func (s *stubSvc) ReadMcpResource(_ context.Context, id, uri string) (*dtocapability.McpResourceReadResp, error) {
	s.mcpLiveID, s.mcpLiveURI = id, uri
	if s.mcpLiveErr != nil {
		return nil, s.mcpLiveErr
	}
	return &dtocapability.McpResourceReadResp{ServerID: id, URI: uri, Contents: []dtocapability.McpResourceContent{{URI: uri, Text: "hi"}}}, nil
}
func (s *stubSvc) ListMcpPrompts(_ context.Context, id string) (*dtocapability.McpPromptsResp, error) {
	s.mcpLiveID = id
	if s.mcpLiveErr != nil {
		return nil, s.mcpLiveErr
	}
	return &dtocapability.McpPromptsResp{ServerID: id, Prompts: []dtocapability.McpPromptResp{{Name: "greet"}}}, nil
}
func (s *stubSvc) GetMcpPrompt(_ context.Context, id, name string, req dtocapability.McpPromptGetReq) (*dtocapability.McpPromptGetResp, error) {
	s.mcpLiveID, s.mcpLiveName, s.mcpLiveArgs = id, name, req.Arguments
	if s.mcpLiveErr != nil {
		return nil, s.mcpLiveErr
	}
	return &dtocapability.McpPromptGetResp{ServerID: id, Name: name, Messages: []dtocapability.McpPromptMessage{{Role: "user", Content: json.RawMessage(`{"type":"text","text":"hi"}`)}}}, nil
}
func (s *stubSvc) ProbeMcpServer(_ context.Context, id string) (*dtocapability.McpProbeResp, error) {
	s.probeID = id
	if s.probeErr != nil {
		return nil, s.probeErr
	}
	if s.probeResp != nil {
		return s.probeResp, nil
	}
	return &dtocapability.McpProbeResp{ID: id, ProbedAt: "2026-09-09T00:00:00Z"}, nil
}
func (s *stubSvc) ListSessions(archived bool, cwd, workspaceID string) ([]dtosession.SessionResp, error) {
	s.listArchivedOnly, s.listCwd, s.listWorkspaceID = archived, cwd, workspaceID
	if s.listSessionsErr != nil {
		return nil, s.listSessionsErr
	}
	return s.sessions, nil
}
func (s *stubSvc) BrowseWorkspace(path string, includeHidden bool) (*dtoworkspace.WorkspaceBrowseResp, error) {
	s.browsePath, s.browseHidden = path, includeHidden
	if s.browseErr != nil {
		return nil, s.browseErr
	}
	return s.browseResp, nil
}

func (s *stubSvc) NativePickerMode() string {
	if s.pickerMode == "" {
		return "browse"
	}
	return s.pickerMode
}

func (s *stubSvc) PickDirectoryHost(_ context.Context) (string, bool, error) {
	s.pickCalled = true
	if s.pickErr != nil {
		return "", false, s.pickErr
	}
	return s.pickPath, s.pickCancelled, nil
}

func (s *stubSvc) CreateWorkspaceDir(parent, name string) (*dtoworkspace.WorkspaceDirCreateResp, error) {
	s.dirParent, s.dirName = parent, name
	if s.dirErr != nil {
		return nil, s.dirErr
	}
	return s.dirResp, nil
}

func (s *stubSvc) RecentWorkspaces(limit int) ([]dtoworkspace.WorkspaceRecentResp, error) {
	s.recentLimit = limit
	if s.recentErr != nil {
		return nil, s.recentErr
	}
	return s.recentResp, nil
}

func (s *stubSvc) ListWorkspaceFiles(path string, includeHidden bool) (*dtoworkspace.WorkspaceFilesResp, error) {
	s.filesPath, s.filesHidden = path, includeHidden
	if s.filesErr != nil {
		return nil, s.filesErr
	}
	return s.filesResp, nil
}

func (s *stubSvc) WorkspaceSettings() (*dtoworkspace.WorkspaceSettingsResp, error) {
	if s.settingsErr != nil {
		return nil, s.settingsErr
	}
	return s.settingsResp, nil
}

func (s *stubSvc) UpdateWorkspaceSettings(roots []string) (*dtoworkspace.WorkspaceSettingsResp, error) {
	s.settingsCalled = true
	s.settingsRoots = roots
	if s.settingsErr != nil {
		return nil, s.settingsErr
	}
	return s.settingsResp, nil
}

// ——— v14 工作空间登记表桩 ———
//
// 这一组桩只做**透传 + 记参**：控制器在本轮里没有任何领域判断（校验、默认工作空间、
// 归属解析全在 manager），所以路由测试要证明的也只是"路径与参数原样到了端口、
// 错误按带码信封出去"。领域语义由 manager 与 store 的测试覆盖。

func (s *stubSvc) WorkspaceRegistry() (*dtoworkspace.WorkspaceListResp, error) {
	s.registryListCalled = true
	if s.registryErr != nil {
		return nil, s.registryErr
	}
	return s.registryList, nil
}

func (s *stubSvc) CreateRegisteredWorkspace(path, title string, strict bool) (*dtoworkspace.WorkspaceResp, error) {
	s.registryCreatePath, s.registryCreateTitle, s.registryCreateStrict = path, title, strict
	if s.registryErr != nil {
		return nil, s.registryErr
	}
	return s.registryOne, nil
}

func (s *stubSvc) RenameRegisteredWorkspace(id, title string) (*dtoworkspace.WorkspaceResp, error) {
	s.registryID, s.registryRenameTitle = id, title
	if s.registryErr != nil {
		return nil, s.registryErr
	}
	return s.registryOne, nil
}

func (s *stubSvc) DeleteRegisteredWorkspace(id string) (*dtoworkspace.WorkspaceDeleteResp, error) {
	s.registryID = id
	if s.registryErr != nil {
		return nil, s.registryErr
	}
	return s.registryDelete, nil
}

func (s *stubSvc) MoveRegisteredWorkspace(id, beforeID string) (*dtoworkspace.WorkspaceMoveResp, error) {
	s.registryID, s.registryMoveBefore = id, beforeID
	if s.registryErr != nil {
		return nil, s.registryErr
	}
	return s.registryMove, nil
}

func (s *stubSvc) SearchSessions(_ context.Context, _ string, _ int) ([]dtosession.SessionSearchHit, error) {
	return nil, nil
}

// RuntimeIdentity 底座身份桩：`/meta` 的 runtime 段（id/name/version）由路由层从本方法取。
func (s *stubSvc) RuntimeIdentity(_ context.Context) dtoruntime.IdentityResp {
	return dtoruntime.IdentityResp{ID: "stub", Name: "Stub", Version: "0.0-stub"}
}

// RuntimeDiagnostics 底座诊断桩（v19：原 AgentDiagnostics，路径不带 id）。
func (s *stubSvc) RuntimeDiagnostics(_ context.Context) (*dtoruntime.DiagnosticsResp, error) {
	if s.diagErr != nil {
		return nil, s.diagErr
	}
	if s.diagResp != nil {
		return s.diagResp, nil
	}
	return &dtoruntime.DiagnosticsResp{State: "stopped", Runtime: "stub", StderrSource: "none",
		StderrTail: []string{}, CheckedAt: "2026-09-09T00:00:00Z"}, nil
}

// ——— M3 ② 消息检索 / 批量导出桩 ———
func (s *stubSvc) SearchMessages(_ context.Context, q string, limit int) (*dtosearch.MessageSearchResp, error) {
	s.searchMsgQuery, s.searchMsgLimit = q, limit
	if s.searchMsgErr != nil {
		return nil, s.searchMsgErr
	}
	if s.searchMsgResp != nil {
		return s.searchMsgResp, nil
	}
	return &dtosearch.MessageSearchResp{Query: q, Source: "sqlite", Hits: []dtosearch.MessageSearchHit{}}, nil
}

func (s *stubSvc) ExportSessions(_ context.Context, req dtoexport.ExportReq) (*dtoexport.ExportResp, error) {
	s.exportReq = req
	if s.exportErr != nil {
		return nil, s.exportErr
	}
	if s.exportResp != nil {
		return s.exportResp, nil
	}
	return &dtoexport.ExportResp{Format: "markdown", Files: []dtoexport.ExportFile{}}, nil
}
func (s *stubSvc) GetSession(_ context.Context, id string) (*dtosession.SessionDetailResp, error) {
	return &dtosession.SessionDetailResp{
		SessionResp: dtosession.SessionResp{ID: id},
		Memories:    s.memList,
	}, nil
}
func (s *stubSvc) UpdateSession(_ context.Context, id string, patch dtosession.SessionPatch) (*dtosession.SessionResp, error) {
	resp := dtosession.SessionResp{ID: id}
	if patch.Title != nil {
		resp.Title = *patch.Title
	}
	if patch.Pinned != nil {
		resp.Pinned = *patch.Pinned
	}
	if patch.Archived != nil {
		resp.Archived = *patch.Archived
	}
	return &resp, nil
}
func (s *stubSvc) ListPendingPermissions(_ context.Context, id string) ([]dtosession.PermissionResp, error) {
	return nil, nil
}
func (s *stubSvc) ResolvePermission(_ context.Context, id, callID, optionID string, always, allowed bool) (*dtosession.PermissionDecisionResp, error) {
	if s.resolvePermErr != nil {
		return nil, *s.resolvePermErr
	}
	return &dtosession.PermissionDecisionResp{
		CallID: callID, Allowed: allowed, Rejected: !allowed, Always: always,
		OptionID: optionID, Outcome: "selected", SelectedOptionID: optionID,
	}, nil
}
func (s *stubSvc) ListPermissionMemories(_ context.Context, id string) ([]dtosession.MemoryResp, error) {
	return s.memList, nil
}
func (s *stubSvc) RevokeMemory(_ context.Context, id, title, kind string) error {
	s.revokeID = id
	s.revokeTitle = title
	s.revokeKind = kind
	return s.revokeErr
}
func (s *stubSvc) ListPendingElicitations(_ context.Context, id string) ([]dtosession.ElicitationResp, error) {
	return s.elicitations, nil
}
func (s *stubSvc) ResolveElicitation(_ context.Context, id, elicitationID, action string, content map[string]any) error {
	s.resolvedElicID = elicitationID
	s.resolvedElicAction = action
	s.resolvedElicContent = content
	return s.resolveElicErr
}
func (s *stubSvc) CreateSession(_ context.Context, req dtosession.SessionCreateReq) (*dtosession.SessionCreateResp, error) {
	s.createCalled = true
	s.createReq = req
	if s.createErr != nil {
		return nil, s.createErr
	}
	if s.createResp != nil {
		return s.createResp, nil
	}
	return &dtosession.SessionCreateResp{ID: "s-new"}, nil
}
func (s *stubSvc) ListQueuedInputs(_ context.Context, id string) ([]dtosession.QueuedInputResp, error) {
	s.inputsListed, s.inputsListConv = true, id
	return []dtosession.QueuedInputResp{
		{ID: "in-8", Text: "排队一", Delivery: "after_run", TargetRunID: "t1", Generation: 3},
	}, nil
}

func (s *stubSvc) DeleteQueuedInput(_ context.Context, id, inputID string) (bool, error) {
	s.inputDeleted, s.inputDeleteID, s.inputDeleteConv = true, inputID, id
	return true, nil
}

func (s *stubSvc) PromptSession(_ context.Context, id string, req dtosession.PromptReq) (*dtosession.SessionPromptResp, error) {
	s.promptCalled = true
	s.promptID = id
	s.promptText = req.Prompt
	s.promptDelivery = req.Delivery
	if s.promptErr != nil {
		return nil, s.promptErr
	}
	if s.promptResp != nil {
		return s.promptResp, nil
	}
	return &dtosession.SessionPromptResp{ID: id, EventSeq: 5}, nil
}
func (s *stubSvc) CancelRun(_ context.Context, id string) error {
	s.cancelCalled = true
	s.cancelID = id
	return s.cancelErr
}
func (s *stubSvc) SessionHistory(_ context.Context, id string, q dtosession.HistoryQuery) (*dtosession.SessionHistoryResp, error) {
	s.historyCalled = true
	s.historyID = id
	s.historyAfter = q.After
	s.historyBefore = q.Before
	s.historyLimit = q.Limit
	if s.historyErr != nil {
		return nil, s.historyErr
	}
	if s.historyResp != nil {
		return s.historyResp, nil
	}
	return &dtosession.SessionHistoryResp{ID: id}, nil
}

// ListRuns 桩：默认空 runs（详情/统计路径不依赖其返回）。
func (s *stubSvc) ListRuns(_ context.Context, id string) ([]dtosession.RunResp, error) {
	return []dtosession.RunResp{}, nil
}

// ——— 测试基建 ———

type envelope struct {
	Code      int             `json:"code"`
	RequestID string          `json:"requestID"`
	Msg       string          `json:"msg"`
	Data      json.RawMessage `json:"data"`
}

// stubDeps 用同一个测试替身填满全部领域端口（stubSvc 实现了全部端口）。
func stubDeps(svc *stubSvc) router.Ports {
	return router.Ports{
		Session: svc, Workspace: svc, Runtime: svc,
		Capability: svc, Library: svc, Usage: svc, Search: svc, Export: svc,
	}
}

// newRestEngine 走**真实路由装配**（router.New）：路径、鉴权与中间件形态都与线上一致。
//
// 历史版本在这里手工镜像了一份路由表（g.GET("/agents", ...) 逐条重敲），
// 结果是路由一旦改名测试仍全绿——镜像表与真实表各说各话。现在直接构造真实引擎，
// 路由表只有 router 包这一个事实源。
func newRestEngine(deps router.Ports) *gin.Engine {
	return router.New(deps, eventbus.New())
}

// TestWorkspaceSettingsCtrPassThrough 设置面的控制器职责：GET 透出快照、PUT 把 roots 原样交给
// 领域层（语义判断全在 manager，handler 不做第二套）、body 非法回 40000、领域错误经 RestErr 映射。
func TestWorkspaceSettingsCtrPassThrough(t *testing.T) {
	snap := &dtoworkspace.WorkspaceSettingsResp{
		Configured: []string{"/srv/docs"}, Roots: []string{"/srv/docs", "/home/u/.ark-mate/workspaces"},
		PreferRoots: true, ProvisionDir: "/home/u/.ark-mate/workspaces", ProvisionIncluded: true,
		Source: "config.json", Writable: true,
	}
	svc := &stubSvc{settingsResp: snap}
	r := newRestEngine(stubDeps(svc))

	env, _ := do(t, r, "GET", "/v1/mate/settings/workspace", "")
	if env.Code != 0 {
		t.Fatalf("GET 应成功: %+v", env)
	}
	var got dtoworkspace.WorkspaceSettingsResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	if len(got.Roots) != 2 || !got.ProvisionIncluded || !got.Writable {
		t.Fatalf("快照应原样透出: %+v", got)
	}

	// PUT：roots 原样下发（含空数组 = 恢复不限制），响应回更新后的快照。
	env, _ = do(t, r, "PUT", "/v1/mate/settings/workspace", `{"roots":["/srv/docs"]}`)
	if env.Code != 0 {
		t.Fatalf("PUT 应成功: %+v", env)
	}
	if !svc.settingsCalled || len(svc.settingsRoots) != 1 || svc.settingsRoots[0] != "/srv/docs" {
		t.Fatalf("roots 应原样下发: %+v", svc.settingsRoots)
	}
	svc.settingsCalled = false
	if _, _ = do(t, r, "PUT", "/v1/mate/settings/workspace", `{"roots":[]}`); !svc.settingsCalled || len(svc.settingsRoots) != 0 {
		t.Fatalf("空数组应下发空名单（= 不限制）: called=%v roots=%+v", svc.settingsCalled, svc.settingsRoots)
	}
	// 字段缺省与空数组同义：都是"取消限制"（白名单只有"画边界/不画边界"两种状态）。
	svc.settingsCalled = false
	if _, _ = do(t, r, "PUT", "/v1/mate/settings/workspace", `{}`); !svc.settingsCalled || len(svc.settingsRoots) != 0 {
		t.Fatalf("缺省 roots 应下发空名单: called=%v roots=%+v", svc.settingsCalled, svc.settingsRoots)
	}

	// body 非法 → 参数错误码（不是 1：面板要能区分"我传错了"与"服务端坏了"）。
	if env, _ = do(t, r, "PUT", "/v1/mate/settings/workspace", `{`); env.Code != 40000 {
		t.Fatalf("非法 body 应 40000: %+v", env)
	}

	// 领域拒绝（锁定 / 只读 / 非法目录）经 RestErr 映射为业务码，且不吞掉语义码。
	svc.settingsErr = gerror.Error{Code: 40001, Msg: "[settings_locked] 被 MATE_WORKSPACE_ROOTS 锁定"}
	env, _ = do(t, r, "PUT", "/v1/mate/settings/workspace", `{"roots":["/srv/docs"]}`)
	if env.Code != 40001 || !strings.Contains(env.Msg, "settings_locked") {
		t.Fatalf("领域错误应经 RestErr 透出: %+v", env)
	}
}

// TestWorkspaceRegistryCtrPassThrough v14 登记表的控制器职责：路径参数与 body 原样到端口、
// 领域错误经带码信封出去。
//
// 控制器在本轮里没有任何领域判断（校验、默认工作空间、归属解析全在 manager），所以这里
// 要证明的就是这条透传链——尤其是**路径参数别被静态路由抢走**（`/workspaces/browse` 之类
// 是与 `/workspaces/:id` 共存的静态段，顺序错了会把 `browse` 当成 id 打过来）。
func TestWorkspaceRegistryCtrPassThrough(t *testing.T) {
	one := &dtoworkspace.WorkspaceResp{
		ID: "wsp-1", Path: "/Users/me/work/ark-mate", Title: "ark-mate",
		DirStatus: dtoworkspace.DirStatusOK, Sessions: 12, CreatedAt: "2026-10-11T00:00:00Z",
	}
	svc := &stubSvc{
		registryOne:    one,
		registryList:   &dtoworkspace.WorkspaceListResp{Items: []dtoworkspace.WorkspaceResp{*one}, DefaultID: "wsp-default", Writable: true},
		registryDelete: &dtoworkspace.WorkspaceDeleteResp{ID: "wsp-1", ReleasedSessions: 12},
		registryMove:   &dtoworkspace.WorkspaceMoveResp{IDs: []string{"wsp-2", "wsp-1"}},
	}
	r := newRestEngine(stubDeps(svc))

	// GET /workspaces：列表原样透出（含 default_id 与 writable——前端靠它们决定"未指定"归哪、
	// 以及要不要渲染创建入口）。
	env, _ := do(t, r, "GET", "/v1/mate/workspaces", "")
	if env.Code != 0 {
		t.Fatalf("GET /workspaces 应成功: %+v", env)
	}
	var list dtoworkspace.WorkspaceListResp
	if err := json.Unmarshal(env.Data, &list); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	if len(list.Items) != 1 || list.DefaultID != "wsp-default" || !list.Writable {
		t.Fatalf("登记表应原样透出: %+v", list)
	}

	// POST /workspaces：path/title/strict 三个参数都要到端口（strict 决定重复登记是
	// 返回既有行还是报错，漏传会让"必须新建"的调用方静默得到旧行）。
	env, _ = do(t, r, "POST", "/v1/mate/workspaces", `{"path":"/Users/me/work/ark-mate","title":"昵称","strict":true}`)
	if env.Code != 0 {
		t.Fatalf("POST /workspaces 应成功: %+v", env)
	}
	if svc.registryCreatePath != "/Users/me/work/ark-mate" || svc.registryCreateTitle != "昵称" || !svc.registryCreateStrict {
		t.Fatalf("创建参数未原样下发: path=%q title=%q strict=%v",
			svc.registryCreatePath, svc.registryCreateTitle, svc.registryCreateStrict)
	}

	// PATCH /workspaces/:id：路径参数与 body 都要到端口。
	env, _ = do(t, r, "PATCH", "/v1/mate/workspaces/wsp-1", `{"title":"新名字"}`)
	if env.Code != 0 || svc.registryID != "wsp-1" || svc.registryRenameTitle != "新名字" {
		t.Fatalf("改名参数未原样下发: code=%d id=%q title=%q", env.Code, svc.registryID, svc.registryRenameTitle)
	}

	// DELETE /workspaces/:id：released_sessions 必须回出来（界面那句"这 N 个会话回到默认
	// 工作空间"要有事实支撑，不是前端自己猜的）。
	env, _ = do(t, r, "DELETE", "/v1/mate/workspaces/wsp-1", "")
	if env.Code != 0 || svc.registryID != "wsp-1" {
		t.Fatalf("删除参数未原样下发: code=%d id=%q", env.Code, svc.registryID)
	}
	var del dtoworkspace.WorkspaceDeleteResp
	if err := json.Unmarshal(env.Data, &del); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	if del.ReleasedSessions != 12 {
		t.Fatalf("released_sessions 应原样透出: %+v", del)
	}

	// POST /workspaces/:id/move：锚点原样下发；响应是**完整**顺序。
	env, _ = do(t, r, "POST", "/v1/mate/workspaces/wsp-1/move", `{"before_id":"wsp-2"}`)
	if env.Code != 0 || svc.registryID != "wsp-1" || svc.registryMoveBefore != "wsp-2" {
		t.Fatalf("重排参数未原样下发: code=%d id=%q before=%q", env.Code, svc.registryID, svc.registryMoveBefore)
	}
	var mv dtoworkspace.WorkspaceMoveResp
	if err := json.Unmarshal(env.Data, &mv); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	if len(mv.IDs) != 2 || mv.IDs[0] != "wsp-2" {
		t.Fatalf("重排应返回完整顺序: %+v", mv)
	}

	// **空 body 的 move 不是错误**（追加到尾部），所以缺省 body 也必须走通。
	svc.registryMoveBefore = "sentinel"
	if env, _ = do(t, r, "POST", "/v1/mate/workspaces/wsp-1/move", ""); env.Code != 0 {
		t.Fatalf("空 body 的 move 应成功（= 追加到尾部）: %+v", env)
	}
	if svc.registryMoveBefore != "" {
		t.Fatalf("空 body 时锚点应为空串（追加），实际 %q", svc.registryMoveBefore)
	}

	// 非法 body → 参数错误码（不是 1：面板要能区分"我传错了"与"服务端坏了"）。
	if env, _ = do(t, r, "POST", "/v1/mate/workspaces", `{`); env.Code != 40000 {
		t.Fatalf("非法 body 应 40000: %+v", env)
	}

	// 领域错误经带码信封出去（未知 id / 默认不可删 / memory 只读都要能被前端分辨）。
	for _, c := range []struct {
		code int
		msg  string
	}{
		{40401, "[workspace_not_found] 未知工作空间"},
		{40001, "[workspace_default_immutable] 默认工作空间不可删除"},
		{40501, "[workspace_schema_read_only] 当前持久层为 memory"},
	} {
		svc.registryErr = gerror.Error{Code: c.code, Msg: c.msg}
		env, _ = do(t, r, "DELETE", "/v1/mate/workspaces/wsp-x", "")
		if env.Code != c.code || !strings.Contains(env.Msg, c.msg[:len("[")+len(strings.Fields(c.msg)[0])-1]) {
			t.Fatalf("领域错误 %s 应经带码信封透出: %+v", c.msg, env)
		}
	}
}

// TestWorkspaceStaticRoutesBeatParamRoute `/workspaces/browse` 这类**静态段**不能被
// `/workspaces/:id` 抢走。
//
// 为什么专门守：本组同时注册了 `GET /workspaces` 与 `GET /workspaces/browse`、以及
// `PATCH/DELETE /workspaces/:id`。gin 的 radix 树对静态段优先，但这条性质**不是我们能控制的**
// ——一旦它变了（或有人把路由挪到另一个 group 里重建），`browse` 会被当成 id 打进改名/删除。
// 那种坏法是静默的：浏览目录突然报"未知工作空间"。
func TestWorkspaceStaticRoutesBeatParamRoute(t *testing.T) {
	svc := &stubSvc{browseResp: &dtoworkspace.WorkspaceBrowseResp{Path: "/Users/me"}}
	r := newRestEngine(stubDeps(svc))

	env, _ := do(t, r, "GET", "/v1/mate/workspaces/browse?path=/Users/me", "")
	if env.Code != 0 {
		t.Fatalf("GET /workspaces/browse 应打到 Browse 而不是被 :id 抢走: %+v", env)
	}
	if svc.browsePath != "/Users/me" {
		t.Fatalf("Browse 未收到 path（说明被当成 id 打到了别的 handler）: %q", svc.browsePath)
	}
	if svc.registryID != "" {
		t.Fatalf("Browse 不该走到带参 handler: registryID=%q", svc.registryID)
	}
}

// TestWorkspaceCreateDirRoute 目录浏览面的写面（`POST /workspaces/dirs`，v14.3）。
//
// 两层守卫，缺一不可：
//   - **路径不能被 `/workspaces/:id` 的写路由抢走**：本组同时有 `POST /workspaces/:id/move`，
//     而 `dirs` 是静态段。抢走的坏法是静默的——「新建文件夹」突然报"未知工作空间"；
//   - **参数原样到端口**：控制器在本轮没有任何领域判断（单段名 / 越界 / 已存在全在 manager），
//     所以这里要证的只是透传；领域语义由 manager 的用例覆盖。
func TestWorkspaceCreateDirRoute(t *testing.T) {
	svc := &stubSvc{dirResp: &dtoworkspace.WorkspaceDirCreateResp{Path: "/Users/me/proj/新项目"}}
	r := newRestEngine(stubDeps(svc))

	env, _ := do(t, r, "POST", "/v1/mate/workspaces/dirs", `{"parent":"/Users/me/proj","name":"新项目"}`)
	if env.Code != 0 {
		t.Fatalf("POST /workspaces/dirs 应成功: %+v", env)
	}
	if svc.dirParent != "/Users/me/proj" || svc.dirName != "新项目" {
		t.Fatalf("CreateWorkspaceDir 未收到原样参数: parent=%q name=%q", svc.dirParent, svc.dirName)
	}
	if svc.registryID != "" {
		t.Fatalf("不该走到带参 handler: registryID=%q", svc.registryID)
	}
}

// TestWorkspaceCreateDirRejections 三种拒绝必须是三个不同的码：
// 越界 40001 / 名字非法 40000 / 已存在 40901——它们的界面引导不同（改路径 vs 改名）。
func TestWorkspaceCreateDirRejections(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
	}{
		{"名字非法", acl.NewError(acl.ClassRequest, "workspace_invalid", "目录名不能包含路径分隔符", nil), code.InvalidRequest},
		{"名字非法", acl.NewError(acl.ClassRequest, "workspace_invalid", "名字非法", nil), code.InvalidRequest},
		{"已存在", acl.NewError(acl.ClassLimit, "workspace_exists", "目录已存在", nil), code.Conflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubSvc{dirErr: tc.err}
			r := newRestEngine(stubDeps(svc))
			env, _ := do(t, r, "POST", "/v1/mate/workspaces/dirs", `{"parent":"/p","name":"x"}`)
			if env.Code != tc.code {
				t.Fatalf("期望 code=%d，得到 %+v", tc.code, env)
			}
		})
	}
}

func do(t *testing.T, r *gin.Engine, method, path, body string) (*envelope, int) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("content-type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应非信封 JSON: %v body=%s", err, w.Body.String())
	}
	return &env, w.Code
}

// ——— 用例 ———

// TestListEndpoints GET /sessions 返回 code=0 + 真实列表载荷。
//
// v19：agent 列表那一半随 `/agents` 路由整体删除——会话身份只剩 `runtime_id`（构建期固定）。
func TestListEndpoints(t *testing.T) {
	svc := &stubSvc{
		sessions: []dtosession.SessionResp{{ID: "s1", RuntimeID: "rtx", Status: "completed"}},
	}
	r := newRestEngine(stubDeps(svc))

	env, _ := do(t, r, "GET", "/v1/mate/sessions", "")
	if env.Code != 0 || env.Msg != "success" {
		t.Fatalf("sessions 信封不符: %+v", env)
	}
	var sess []dtosession.SessionResp
	if err := json.Unmarshal(env.Data, &sess); err != nil || len(sess) != 1 || sess[0].Status != "completed" {
		t.Fatalf("sessions data 不符: %s", env.Data)
	}
	if sess[0].RuntimeID != "rtx" || !strings.Contains(string(env.Data), `"runtime_id":"rtx"`) {
		t.Fatalf("会话身份应只有 runtime_id: %s", env.Data)
	}
}

// TestListSessionsWorkspaceQuery GET /sessions 的 `cwd` 过滤：
// 绝对路径透传给 service（含 workspace_temporary 回读），相对路径属参数错误直接 40000。
func TestListSessionsWorkspaceQuery(t *testing.T) {
	svc := &stubSvc{sessions: []dtosession.SessionResp{{
		ID: "c-1", RuntimeID: "rtx", Status: "completed",
		Cwd: "/work/alpha", WorkspaceTemporary: true,
	}}}
	r := newRestEngine(stubDeps(svc))

	env, code := do(t, r, "GET", "/v1/mate/sessions?cwd=/work/alpha", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d env=%+v", code, env)
	}
	if svc.listCwd != "/work/alpha" || svc.listArchivedOnly {
		t.Fatalf("cwd 未透传: cwd=%q archived=%v", svc.listCwd, svc.listArchivedOnly)
	}
	var list []dtosession.SessionResp
	if err := json.Unmarshal(env.Data, &list); err != nil || len(list) != 1 {
		t.Fatalf("sessions data 不符: %s", env.Data)
	}
	if list[0].Cwd != "/work/alpha" || !list[0].WorkspaceTemporary {
		t.Fatalf("工作空间字段未回读: %+v", list[0])
	}

	// 未指定 cwd → 不过滤（空串透传）。
	svc.listCwd = "sentinel"
	if _, code := do(t, r, "GET", "/v1/mate/sessions", ""); code != 200 {
		t.Fatalf("无 cwd 应 200，got %d", code)
	}
	if svc.listCwd != "" {
		t.Fatalf("无 cwd 时应透传空串，got %q", svc.listCwd)
	}

	// 相对路径 → 40000（在 handler 层拦截，不进入 service）。
	env, _ = do(t, r, "GET", "/v1/mate/sessions?cwd=relative/dir", "")
	if env.Code != 40000 {
		t.Fatalf("相对 cwd 应 40000，got %+v", env)
	}
}

// TestUsageSummaryRoute M3：GET /usage/summary 参数绑定/透传 + 信封形态 + 业务错误透传。
func TestUsageSummaryRoute(t *testing.T) {
	svc := &stubSvc{usageResp: &dtousage.UsageSummaryResp{
		From: "2026-09-01T00:00:00Z", To: "2026-09-09T00:00:00Z",
		GroupBy: "day", Source: "sqlite",
		Totals:  dtousage.UsageBucketResp{Key: "total", Runs: 3, Completed: 2, Failed: 1, DurationMS: 1200},
		Buckets: []dtousage.UsageBucketResp{{Key: "2026-09-08", Runs: 3}},
	}}
	r := newRestEngine(stubDeps(svc))

	env, code := do(t, r, "GET", "/v1/mate/usage/summary?from=2026-09-01&to=2026-09-09&group_by=day", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d env=%+v", code, env)
	}
	if svc.usageQuery.From != "2026-09-01" || svc.usageQuery.To != "2026-09-09" ||
		svc.usageQuery.GroupBy != "day" {
		t.Fatalf("查询参数未透传: %+v", svc.usageQuery)
	}
	var resp dtousage.UsageSummaryResp
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	if resp.GroupBy != "day" || resp.Source != "sqlite" || resp.Totals.Runs != 3 ||
		len(resp.Buckets) != 1 || resp.Buckets[0].Key != "2026-09-08" {
		t.Fatalf("响应载荷不符: %+v", resp)
	}

	// 业务错误（40001/40501）→ HTTP 200 + code≠0 透传。
	svc.usageErr = gerror.Error{Code: 40001, Msg: `[invalid_usage_group_by] group_by 仅支持 day："tool"`}
	if env, code := do(t, r, "GET", "/v1/mate/usage/summary?group_by=tool", ""); code != 200 || env.Code != 40001 {
		t.Fatalf("非法 group_by 应 40001: http=%d env=%+v", code, env)
	}
	svc.usageErr = gerror.Error{Code: 40501, Msg: "[usage_stats_unsupported] 内存模式无统计"}
	if env, _ := do(t, r, "GET", "/v1/mate/usage/summary", ""); env.Code != 40501 {
		t.Fatalf("内存模式应 40501: %+v", env)
	}
}

// TestListFailure 列表失败 → code=50000（兜底内部错误）。
//
// 历史形态是 code=1：那时控制器各处手写 gerror.Error{Code: 1, Msg: err.Error()}，
// 与领域层的 50000 并存，同一个"未归类内部错误"有两个码。P5 收敛到
// code.Internal(50000) 一个兜底码，且 msg 仍原样带出（不失诊断信息）。
func TestListFailure(t *testing.T) {
	svc := &stubSvc{listSessionsErr: errors.New("boom")}
	r := newRestEngine(stubDeps(svc))
	env, _ := do(t, r, "GET", "/v1/mate/sessions", "")
	if env.Code != code.Internal {
		t.Fatalf("期望 code=%d（Internal），got %+v", code.Internal, env)
	}
	if env.Msg != "boom" {
		t.Fatalf("兜底码仍须带出原始 msg，got %q", env.Msg)
	}
}

// TestCreateSession 正常创建：解析 body → 透传 svc → 信封含 id/event_seq。
func TestCreateSession(t *testing.T) {
	svc := &stubSvc{createResp: &dtosession.SessionCreateResp{ID: "s-abc", RuntimeID: "rtx", Status: "in_progress", EventSeq: 9}}
	r := newRestEngine(stubDeps(svc))
	env, _ := do(t, r, "POST", "/v1/mate/sessions", `{"prompt":"hi","cwd":"/tmp"}`)
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var resp struct {
		ID       string `json:"id"`
		EventSeq int64  `json:"event_seq"`
	}
	if err := json.Unmarshal(env.Data, &resp); err != nil || resp.ID != "s-abc" || resp.EventSeq != 9 {
		t.Fatalf("create data 不符: %s", env.Data)
	}
	if !svc.createCalled || svc.createReq.Prompt != "hi" || svc.createReq.Cwd != "/tmp" {
		t.Fatalf("create 未正确透传: %+v", svc.createReq)
	}
}

// TestCreateSessionBadBody 非 JSON 请求体 → code 40000。
func TestCreateSessionBadBody(t *testing.T) {
	r := newRestEngine(stubDeps(&stubSvc{}))
	env, _ := do(t, r, "POST", "/v1/mate/sessions", `not-json{`)
	if env.Code != 40000 {
		t.Fatalf("非 JSON 期望 40000，got %+v", env)
	}
}

// TestCreateSessionServiceError svc 业务错误原样透出（40901 忙）。
func TestCreateSessionServiceError(t *testing.T) {
	svc := &stubSvc{createErr: gerror.Error{Code: 40901, Msg: "run in progress"}}
	r := newRestEngine(stubDeps(svc))
	env, _ := do(t, r, "POST", "/v1/mate/sessions", `{"prompt":"hi"}`)
	if env.Code != 40901 || !strings.Contains(env.Msg, "run in progress") {
		t.Fatalf("服务错误未透出: %+v", env)
	}
}

// TestPromptAndCancel prompt（校验必填）/cancel 调用透传。
func TestPromptAndCancel(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))

	// prompt 缺 prompt → 40000。
	env, _ := do(t, r, "POST", "/v1/mate/sessions/s1/prompt", `{}`)
	if env.Code != 40000 {
		t.Fatalf("prompt 缺参期望 40000，got %+v", env)
	}
	// 正常 prompt。
	env, _ = do(t, r, "POST", "/v1/mate/sessions/s1/prompt", `{"prompt":"再来一轮"}`)
	if env.Code != 0 || !svc.promptCalled || svc.promptID != "s1" || svc.promptText != "再来一轮" {
		t.Fatalf("prompt 调用不符: called=%v id=%s env=%+v", svc.promptCalled, svc.promptID, env)
	}
	var presp struct {
		ID       string `json:"id"`
		EventSeq int64  `json:"event_seq"`
	}
	if err := json.Unmarshal(env.Data, &presp); err != nil || presp.EventSeq != 5 {
		t.Fatalf("prompt data 不符: %s", env.Data)
	}

	// §6 投递模式透传到 service（delivery 不是路由层的事，路由只负责别把它吃掉）。
	env, _ = do(t, r, "POST", "/v1/mate/sessions/s1/prompt", `{"prompt":"排队","delivery":"after_run"}`)
	if env.Code != 0 || svc.promptDelivery != "after_run" {
		t.Fatalf("delivery 未透传: env=%+v got=%q", env, svc.promptDelivery)
	}
	// §6 列出待投递输入（冷挂载重建队列的当前态入口）：载荷形状按前端所需字段断言。
	env, _ = do(t, r, "GET", "/v1/mate/sessions/s1/inputs", "")
	if env.Code != 0 || !svc.inputsListed || svc.inputsListConv != "s1" {
		t.Fatalf("列出排队输入不符: env=%+v listed=%v conv=%q", env, svc.inputsListed, svc.inputsListConv)
	}
	var qdata []struct {
		InputID     string `json:"input_id"`
		Text        string `json:"text"`
		Delivery    string `json:"delivery"`
		TargetRunID string `json:"target_run_id"`
		Generation  int64  `json:"generation"`
	}
	if err := json.Unmarshal(env.Data, &qdata); err != nil || len(qdata) != 1 {
		t.Fatalf("排队列表 data 不符: %s", env.Data)
	}
	if qdata[0].InputID != "in-8" || qdata[0].Delivery != "after_run" || qdata[0].Generation != 3 {
		t.Fatalf("排队列表字段不符: %+v", qdata[0])
	}

	// §6 撤下排队输入：路径参数 :input_id 正确接线（拼错即 404，路由守卫与用例双保险）。
	env, _ = do(t, r, "DELETE", "/v1/mate/sessions/s1/inputs/in-9", "")
	if env.Code != 0 || !svc.inputDeleted || svc.inputDeleteID != "in-9" {
		t.Fatalf("撤下排队输入不符: env=%+v deleted=%v id=%q", env, svc.inputDeleted, svc.inputDeleteID)
	}

	// cancel。
	env, _ = do(t, r, "POST", "/v1/mate/sessions/s1/cancel", "")
	if env.Code != 0 || !svc.cancelCalled || svc.cancelID != "s1" {
		t.Fatalf("cancel 调用不符: env=%+v called=%v", env, svc.cancelCalled)
	}
	var cdata struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(env.Data, &cdata); err != nil || cdata.Status != "cancelling" {
		t.Fatalf("cancel data 不符: %s", env.Data)
	}
}

// TestHistoryParamParsing history 分页参数解析与夹取：默认/显式/非法。
func TestHistoryParamParsing(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))

	// 默认：两个游标都不给（nil = 该方向没有游标）、limit=200。
	do(t, r, "GET", "/v1/mate/sessions/s1/history", "")
	if !svc.historyCalled || svc.historyID != "s1" || svc.historyAfter != nil || svc.historyBefore != nil || svc.historyLimit != 200 {
		t.Fatalf("默认参数不符: %+v %v/%v %d", svc.historyID, svc.historyAfter, svc.historyBefore, svc.historyLimit)
	}

	// 显式 after/limit。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/mate/sessions/s1/history?after=7&limit=50", "")
	if svc.historyAfter == nil || *svc.historyAfter != 7 || svc.historyLimit != 50 {
		t.Fatalf("显式参数不符: after=%v limit=%d", svc.historyAfter, svc.historyLimit)
	}

	// before 游标（C2：向前翻页）；after/before 同时给 → 40001（互斥）。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/mate/sessions/s1/history?before=42&limit=5", "")
	if svc.historyBefore == nil || *svc.historyBefore != 42 || svc.historyAfter != nil || svc.historyLimit != 5 {
		t.Fatalf("before 参数不符: before=%v after=%v limit=%d", svc.historyBefore, svc.historyAfter, svc.historyLimit)
	}
	// before=0 显式给出 = "第 0 条之前"（空页）：**给了游标**与"没给"是两件事。
	// v21 之前这两件事靠 HasBefore 标志区分，现在靠指针——`0` 本身已是合法游标。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/mate/sessions/s1/history?before=0", "")
	if svc.historyBefore == nil || *svc.historyBefore != 0 {
		t.Fatalf("before=0 应携带游标 0: %+v", svc)
	}
	// after=0 同理：它是"第 0 条之后"，也必须有游标（不是"没给"）。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/mate/sessions/s1/history?after=0", "")
	if svc.historyAfter == nil || *svc.historyAfter != 0 {
		t.Fatalf("after=0 应携带游标 0: %+v", svc)
	}
	svc.historyCalled = false
	envBoth, _ := do(t, r, "GET", "/v1/mate/sessions/s1/history?after=1&before=2", "")
	if envBoth.Code != 40001 {
		t.Fatalf("after+before 期望 40001，got %+v", envBoth)
	}
	envBadBefore, _ := do(t, r, "GET", "/v1/mate/sessions/s1/history?before=-3", "")
	if envBadBefore.Code != 40000 {
		t.Fatalf("负 before 期望 40000，got %+v", envBadBefore)
	}

	// limit 上限 1000 夹取。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/mate/sessions/s1/history?limit=999999", "")
	if svc.historyLimit != 1000 {
		t.Fatalf("limit 应夹到 1000，got %d", svc.historyLimit)
	}

	// limit=0 → 1。
	svc.historyCalled = false
	do(t, r, "GET", "/v1/mate/sessions/s1/history?limit=0", "")
	if svc.historyLimit != 1 {
		t.Fatalf("limit=0 应夹到 1，got %d", svc.historyLimit)
	}

	// 非法 after → 40000。
	svc.historyCalled = false
	env, _ := do(t, r, "GET", "/v1/mate/sessions/s1/history?after=-1", "")
	if env.Code != 40000 {
		t.Fatalf("负 after 期望 40000，got %+v", env)
	}
	env, _ = do(t, r, "GET", "/v1/mate/sessions/s1/history?after=abc", "")
	if env.Code != 40000 {
		t.Fatalf("非数字 after 期望 40000，got %+v", env)
	}
	if svc.historyCalled {
		t.Fatal("非法参数不应触达 svc")
	}
}

// TestHistorySuccess 正常 history 返回事件数组（含 has_more）。
func TestHistorySuccess(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))
	env, _ := do(t, r, "GET", "/v1/mate/sessions/s1/history?after=1&limit=10", "")
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var out struct {
		ID      string `json:"id"`
		HasMore bool   `json:"has_more"`
	}
	if err := json.Unmarshal(env.Data, &out); err != nil || out.ID != "s1" {
		t.Fatalf("history data 不符: %s", env.Data)
	}
}

// TestPermissionMemoriesList GET /sessions/:id/permissions/memories 返回会话级权限记忆（含 decision 侧）。
func TestPermissionMemoriesList(t *testing.T) {
	svc := &stubSvc{memList: []dtosession.MemoryResp{
		{Title: "运行演示命令 echo hello", Kind: "execute", Decision: "allow", Since: "2026-01-02T03:04:05Z"},
		{Title: "删除文件", Kind: "execute", Decision: "reject", Since: "2026-01-03T03:04:05Z"},
	}}
	r := newRestEngine(stubDeps(svc))
	env, _ := do(t, r, "GET", "/v1/mate/sessions/s1/permissions/memories", "")
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var out []dtosession.MemoryResp
	if err := json.Unmarshal(env.Data, &out); err != nil || len(out) != 2 ||
		out[0].Title != "运行演示命令 echo hello" || out[0].Kind != "execute" || out[0].Decision != "allow" ||
		out[1].Decision != "reject" {
		t.Fatalf("memories data 不符: %s", env.Data)
	}
}

// TestRevokeMemory 单条撤销（title+kind）与一键撤销（均空）都正确透传。
func TestRevokeMemory(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))

	env, _ := do(t, r, "POST", "/v1/mate/sessions/s1/permissions/memories/revoke",
		`{"title":"运行演示命令 echo hello","kind":"execute"}`)
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	if svc.revokeID != "s1" || svc.revokeTitle != "运行演示命令 echo hello" || svc.revokeKind != "execute" {
		t.Fatalf("撤销单条透传不符: %+v", svc)
	}

	env, _ = do(t, r, "POST", "/v1/mate/sessions/s1/permissions/memories/revoke", `{}`)
	if env.Code != 0 {
		t.Fatalf("一键撤销信封不符: %+v", env)
	}
	if svc.revokeTitle != "" || svc.revokeKind != "" {
		t.Fatalf("一键撤销应传空 title/kind: %+v", svc)
	}
}

// TestElicitationList GET /sessions/:id/elicitations 返回挂起结构化提问（表单 schema 透传）。
func TestElicitationList(t *testing.T) {
	svc := &stubSvc{elicitations: []dtosession.ElicitationResp{{
		ElicitationID: "el-abc", SessionID: "s1", Mode: "form",
		Message: "请选择一个环境", TimeoutAt: "2026-01-02T03:04:05Z",
		Schema: map[string]any{"type": "object", "properties": map[string]any{"env": map[string]any{"type": "string", "enum": []any{"dev", "prod"}}}},
	}}}
	r := newRestEngine(stubDeps(svc))
	env, _ := do(t, r, "GET", "/v1/mate/sessions/s1/elicitations", "")
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var out []dtosession.ElicitationResp
	if err := json.Unmarshal(env.Data, &out); err != nil || len(out) != 1 || out[0].ElicitationID != "el-abc" || out[0].Mode != "form" {
		t.Fatalf("elicitations data 不符: %s", env.Data)
	}
}

// TestResolveElicitation POST /sessions/:id/elicitation 决策透传（accept + content）。
func TestResolveElicitation(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))
	env, _ := do(t, r, "POST", "/v1/mate/sessions/s1/elicitation",
		`{"elicitation_id":"el-abc","action":"accept","content":{"env":"prod"}}`)
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	if svc.resolvedElicID != "el-abc" || svc.resolvedElicAction != "accept" || svc.resolvedElicContent["env"] != "prod" {
		t.Fatalf("决策透传不符: %+v", svc)
	}
}

// TestResolveElicitationBadBody 缺 elicitation_id/action → code 40000。
func TestResolveElicitationBadBody(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))
	env, _ := do(t, r, "POST", "/v1/mate/sessions/s1/elicitation", `{"action":"accept"}`)
	if env.Code != 40000 {
		t.Fatalf("缺 elicitation_id 应 40000: %+v", env)
	}
	env, _ = do(t, r, "POST", "/v1/mate/sessions/s1/elicitation", `{"elicitation_id":"el-abc"}`)
	if env.Code != 40000 {
		t.Fatalf("缺 action 应 40000: %+v", env)
	}
}

// TestProviderRoutes provider 注入面路由：GET 列表 + PATCH upsert（密钥只写不读回）。
func TestProviderRoutes(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))
	env, code := do(t, r, "PATCH", "/v1/mate/providers/default", `{"type":"openai","model":"gpt-4o","key":"sk-x"}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("PATCH provider 失败 code=%d env=%d", code, env.Code)
	}
	env, code = do(t, r, "GET", "/v1/mate/providers", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET providers 失败 code=%d env=%d", code, env.Code)
	}
}

// TestCapabilityRoutes P1 能力数据面路由：GET /skills /mcps /tools 列出目录（信封 data 数组）。
func TestCapabilityRoutes(t *testing.T) {
	svc := &stubSvc{
		skills: []dtocapability.SkillResp{{ID: "plan", Name: "plan", Description: "先规划再动手"}},
		mcps:   []dtocapability.McpServerResp{{ID: "fs", Transport: "stdio", Command: "npx"}},
		tools:  []dtocapability.ToolResp{{Name: "web_search", Source: "builtin", Description: "联网搜索"}},
	}
	r := newRestEngine(stubDeps(svc))

	env, code := do(t, r, "GET", "/v1/mate/skills", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET skills 失败 code=%d env=%d", code, env.Code)
	}
	var skills []dtocapability.SkillResp
	if err := json.Unmarshal(env.Data, &skills); err != nil || len(skills) != 1 || skills[0].ID != "plan" {
		t.Fatalf("skills data 不符: %s", env.Data)
	}

	env, code = do(t, r, "GET", "/v1/mate/mcps", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET mcps 失败 code=%d env=%d", code, env.Code)
	}
	var mcps []dtocapability.McpServerResp
	if err := json.Unmarshal(env.Data, &mcps); err != nil || len(mcps) != 1 || mcps[0].ID != "fs" {
		t.Fatalf("mcps data 不符: %s", env.Data)
	}

	env, code = do(t, r, "GET", "/v1/mate/tools", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET tools 失败 code=%d env=%d", code, env.Code)
	}
	var tools []dtocapability.ToolResp
	if err := json.Unmarshal(env.Data, &tools); err != nil || len(tools) != 1 || tools[0].Name != "web_search" {
		t.Fatalf("tools data 不符: %s", env.Data)
	}
}

// TestSkillToggleRoute PATCH /skills/:id（§13.4 S4）：入参 {enabled} 透传到服务端口，
// 响应回带写回路径与物化计数；非法 JSON → 40000（控制器绑定错误带码）。
func TestSkillToggleRoute(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))

	env, code := do(t, r, "PATCH", "/v1/mate/skills/plan", `{"enabled":false}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("PATCH skill 失败 code=%d env=%d msg=%s", code, env.Code, env.Msg)
	}
	if !svc.skillToggled || svc.skillToggleID != "plan" || svc.skillToggleEnabled {
		t.Fatalf("未透传到服务端口: %+v", svc)
	}
	var resp dtocapability.SkillToggleResp
	if err := json.Unmarshal(env.Data, &resp); err != nil || resp.ID != "plan" || resp.Enabled {
		t.Fatalf("toggle data 不符: %s", env.Data)
	}

	// 启用方向也要通（同一个 handler，方向由入参决定）。
	env, code = do(t, r, "PATCH", "/v1/mate/skills/plan", `{"enabled":true}`)
	if code != 200 || env.Code != 0 || !svc.skillToggleEnabled {
		t.Fatalf("PATCH enable 失败 code=%d env=%d", code, env.Code)
	}

	// 坏入参：控制器绑定失败 → 40000。
	env, code = do(t, r, "PATCH", "/v1/mate/skills/plan", `{"enabled":`)
	if code != 200 || env.Code != 40000 {
		t.Fatalf("坏入参应 40000，code=%d env=%d", code, env.Code)
	}
}

// TestSkillMaterializationRoute GET /skills/:id/materialization（§13.4 S2 审计面）：只读，
// 透传 id 到服务端口，信封 data 带每处物化目标的状态与摘要。
func TestSkillMaterializationRoute(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))

	env, code := do(t, r, "GET", "/v1/mate/skills/plan/materialization", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET materialization 失败 code=%d env=%d", code, env.Code)
	}
	if svc.matQueried != "plan" {
		t.Fatalf("id 未透传: %q", svc.matQueried)
	}
	var resp dtocapability.SkillMaterializationResp
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("data 解析失败: %s", env.Data)
	}
	if resp.ID != "plan" || !resp.Enabled || len(resp.States) != 1 || resp.States[0].Status != "in_sync" {
		t.Fatalf("materialization data 不符: %+v", resp)
	}
	if resp.States[0].Hash != resp.States[0].ActualHash {
		t.Fatalf("in_sync 的期望/实测摘要应一致: %+v", resp.States[0])
	}
}

// TestProbeMcpServerRoute P2 §5.2 MCP live 探测路由：POST /mcps/:id/probe 返回探测结果；
// 未知 server 透传 40401；未知传输透传 40501。
func TestProbeMcpServerRoute(t *testing.T) {
	svc := &stubSvc{
		probeResp: &dtocapability.McpProbeResp{
			ID: "fs", Transport: "stdio", ServerName: "helper", ProbedAt: "2026-09-09T10:00:00Z",
			Added: 2, Removed: 1, ToolCount: 2,
			Tools: []dtocapability.ToolResp{{Name: "read_file", Source: "mcp", McpServer: "fs"}},
		},
	}
	r := newRestEngine(stubDeps(svc))

	env, code := do(t, r, "POST", "/v1/mate/mcps/fs/probe", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("POST probe 失败 code=%d env=%d", code, env.Code)
	}
	var got dtocapability.McpProbeResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("data 反序列化失败: %v (%s)", err, env.Data)
	}
	if got.ID != "fs" || svc.probeID != "fs" || got.Added != 2 || got.ToolCount != 2 {
		t.Fatalf("探测结果不符: %+v stub=%q", got, svc.probeID)
	}
	if len(got.Tools) != 1 || got.Tools[0].McpServer != "fs" {
		t.Fatalf("tools 不符: %+v", got.Tools)
	}

	svc.probeErr = gerror.Error{Code: 40401, Msg: `未知 MCP server "nope"`}
	if env, code := do(t, r, "POST", "/v1/mate/mcps/nope/probe", ""); code != 200 || env.Code != 40401 {
		t.Fatalf("未知 server 应 40401，got http=%d code=%d", code, env.Code)
	}
	svc.probeErr = gerror.Error{Code: 40501, Msg: "MCP 未知传输（不臆造）"}
	if env, code := do(t, r, "POST", "/v1/mate/mcps/weird1/probe", ""); code != 200 || env.Code != 40501 {
		t.Fatalf("未知传输应 40501，got http=%d code=%d", code, env.Code)
	}
}

// TestErrorEnvelopeMapping 端点错误必须经信封映射（修复：此前裸 acl 错误漏成 code=-1，
// 与 README「非 0 为业务错误（40001/40401/…）」契约不符）。
//
// v19：这些用例原挂在 agent 端点上（已随 agent 面删除）；映射表本身与端点无关，
// 现在用**供应商面**（唯一还带完整 acl 分类的写面）钉住四条分类 → 四个码。
func TestErrorEnvelopeMapping(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		err    error
		code   int
	}{
		{"not_found", "GET", "/v1/mate/providers", "",
			acl.NewError(acl.ClassRequest, "provider_not_found", "未知供应商", nil), 40401},
		{"request", "POST", "/v1/mate/providers", `{"id":"p1","protocol":"openai"}`,
			acl.NewError(acl.ClassRequest, "invalid_provider_id", "id 非法", nil), 40001},
		{"conflict", "POST", "/v1/mate/providers", `{"id":"p1","protocol":"openai"}`,
			acl.NewError(acl.ClassLimit, "provider_exists", "已存在", nil), 40901},
		{"unsupported", "POST", "/v1/mate/providers", `{"id":"p1","protocol":"openai","api_key":"sk-x"}`,
			acl.NewError(acl.ClassUnsupported, "credentials_unsupported", "这套底座不支持托管凭据", nil), 40501},
		{"upstream", "POST", "/v1/mate/providers", `{"id":"p1","protocol":"openai"}`,
			acl.NewError(acl.ClassService, "provider_write_failed", "载体提交失败", nil), 50201},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubSvc{providerErr: tc.err}
			r := newRestEngine(stubDeps(svc))
			env, http := do(t, r, tc.method, tc.path, tc.body)
			if http != 200 || env.Code != tc.code {
				t.Fatalf("期望 HTTP 200 code=%d，got http=%d code=%d msg=%q", tc.code, http, env.Code, env.Msg)
			}
			if env.Msg == "" || strings.HasPrefix(env.Msg, "acl error") {
				t.Fatalf("msg 应为可读详情，got %q", env.Msg)
			}
		})
	}
	// 已映射错误不得被二次映射（restErr 幂等）。
	svc := &stubSvc{providerErr: gerror.Error{Code: 40001, Msg: "已映射"}}
	r := newRestEngine(stubDeps(svc))
	env, _ := do(t, r, "GET", "/v1/mate/providers", "")
	if env.Code != 40001 || env.Msg != "已映射" {
		t.Fatalf("已映射错误应透传: %+v", env)
	}
}

// TestMessageSearchRoute GET /search/messages：参数校验（q 必填、limit 正整数）、
// 查询透传与信封形状（M3 ② 锚点跳转数据面）。
func TestMessageSearchRoute(t *testing.T) {
	svc := &stubSvc{searchMsgResp: &dtosearch.MessageSearchResp{
		Query: "502", Source: "sqlite", Total: 1, HasMore: false,
		Hits: []dtosearch.MessageSearchHit{{SessionID: "c-1", Title: "部署", Role: "user", Seq: 7, Snippet: "网关 502"}},
	}}
	r := newRestEngine(stubDeps(svc))

	env, http := do(t, r, "GET", "/v1/mate/search/messages?q=502&limit=5", "")
	if http != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d %+v", http, env)
	}
	if svc.searchMsgQuery != "502" || svc.searchMsgLimit != 5 {
		t.Fatalf("参数透传不符: q=%q limit=%d", svc.searchMsgQuery, svc.searchMsgLimit)
	}
	if !strings.Contains(string(env.Data), `"seq":7`) || !strings.Contains(string(env.Data), `"source":"sqlite"`) {
		t.Fatalf("响应载荷不符: %s", env.Data)
	}

	// 缺 q → 40001（HTTP 200 + 信封非 0）。
	env, http = do(t, r, "GET", "/v1/mate/search/messages", "")
	if http != 200 || env.Code != 40001 || !strings.Contains(env.Msg, "invalid_search_query") {
		t.Fatalf("缺 q 校验不符: http=%d %+v", http, env)
	}
	// 非法 limit → 40001。
	env, http = do(t, r, "GET", "/v1/mate/search/messages?q=a&limit=abc", "")
	if http != 200 || env.Code != 40001 || !strings.Contains(env.Msg, "invalid_search_limit") {
		t.Fatalf("limit 校验不符: http=%d %+v", http, env)
	}
	// 默认 limit=30。
	_, _ = do(t, r, "GET", "/v1/mate/search/messages?q=a", "")
	if svc.searchMsgLimit != 30 {
		t.Fatalf("默认 limit 应为 30: %d", svc.searchMsgLimit)
	}
}

// TestExportRoute POST /sessions/export：请求体校验 + 响应形状 + 错误映射。
func TestExportRoute(t *testing.T) {
	svc := &stubSvc{exportResp: &dtoexport.ExportResp{
		GeneratedAt: "2026-09-09T00:00:00Z", Format: "markdown", Exported: 1, TotalBytes: 12,
		Files: []dtoexport.ExportFile{{SessionID: "c-1", Filename: "部署-c-1.md", Format: "markdown", Bytes: 12, Content: "# 部署\n\nhi"}},
	}}
	r := newRestEngine(stubDeps(svc))

	env, http := do(t, r, "POST", "/v1/mate/sessions/export", `{"ids":["c-1"],"format":"markdown"}`)
	if http != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d %+v", http, env)
	}
	if svc.exportReq.Format != "markdown" || len(svc.exportReq.IDs) != 1 {
		t.Fatalf("请求透传不符: %+v", svc.exportReq)
	}
	if !strings.Contains(string(env.Data), `"filename":"部署-c-1.md"`) || !strings.Contains(string(env.Data), `"total_bytes":12`) {
		t.Fatalf("响应载荷不符: %s", env.Data)
	}

	// 缺 ids → 40000（参数绑定失败）。
	env, http = do(t, r, "POST", "/v1/mate/sessions/export", `{"format":"text"}`)
	if http != 200 || env.Code != 40000 {
		t.Fatalf("缺 ids 校验不符: http=%d %+v", http, env)
	}
	// 服务错误映射（40001 非法格式）。
	svc2 := &stubSvc{exportErr: gerror.Error{Code: 40001, Msg: "不支持的导出格式 [invalid_export_format]"}}
	r2 := newRestEngine(stubDeps(svc2))
	env, http = do(t, r2, "POST", "/v1/mate/sessions/export", `{"ids":["c-1"],"format":"pdf"}`)
	if http != 200 || env.Code != 40001 || !strings.Contains(env.Msg, "invalid_export_format") {
		t.Fatalf("错误映射不符: http=%d %+v", http, env)
	}
}

// TestRuntimeDiagnosticsRoute GET /runtime/diagnostics：载荷形状（stderr 尾巴 + 最近失败）+ 错误映射。
//
// v19：路径不带 id（原 `GET /agents/:id/diagnostics`）——底座由构建期固定，身份由响应里的
// `runtime` 回答，不再有"哪个 agent 的诊断"这一问。
func TestRuntimeDiagnosticsRoute(t *testing.T) {
	svc := &stubSvc{diagResp: &dtoruntime.DiagnosticsResp{
		State: "stopped", Runtime: "stub", RuntimeName: "Stub", Command: "sh",
		Connected: false, StderrSource: "last_error", CheckedAt: "2026-09-09T00:00:00Z",
		StderrTail: []string{"[warn] boom"},
		LastError:  &dtoruntime.LastErrorResp{At: "2026-09-09T00:00:00Z", Code: "timeout", Class: "connection", Details: "握手超时"},
	}}
	r := newRestEngine(stubDeps(svc))

	env, http := do(t, r, "GET", "/v1/mate/runtime/diagnostics", "")
	if http != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d %+v", http, env)
	}
	body := string(env.Data)
	for _, want := range []string{`"runtime":"stub"`, `"runtime_name":"Stub"`, `"stderr_source":"last_error"`, `"[warn] boom"`, `"code":"timeout"`, `"connected":false`} {
		if !strings.Contains(body, want) {
			t.Fatalf("响应缺 %s: %s", want, body)
		}
	}
	// 身份面不许回带 agent 相关的选择面字段（被撤掉的概念不许从别的字段缝回来）——
	// 判据落在响应形状上（`rest_removed_agent_key_test.go` 有逐字的那一条）。
	if strings.Contains(body, `"runtimes"`) {
		t.Fatalf("诊断面不该出现 runtimes: %s", body)
	}

	// 底层错误 → 信封映射（handler 经 code.FromError）。
	svc2 := &stubSvc{diagErr: acl.NewError(acl.ClassRequest, "runtime_not_found", "底座未装配", nil)}
	r2 := newRestEngine(stubDeps(svc2))
	env, http = do(t, r2, "GET", "/v1/mate/runtime/diagnostics", "")
	if http != 200 || env.Code != 40401 || !strings.Contains(env.Msg, "底座未装配") {
		t.Fatalf("错误映射不符: http=%d %+v", http, env)
	}
}

// TestCallMcpToolRoute B1 MCP 工具调用路由：参数透传（server/tool/arguments/timeout_ms）、
// 工具级失败（is_error=true）仍是 code=0，传输/协议错误透传信封码。
func TestCallMcpToolRoute(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))

	body := `{"arguments":{"path":"/tmp/a"},"timeout_ms":1500}`
	env, code := do(t, r, "POST", "/v1/mate/mcps/fs/tools/read_file/call", body)
	if code != 200 || env.Code != 0 {
		t.Fatalf("POST call 失败 http=%d code=%d body=%s", code, env.Code, env.Data)
	}
	if svc.callMcpID != "fs" || svc.callMcpTool != "read_file" {
		t.Fatalf("路径参数未透传: id=%q tool=%q", svc.callMcpID, svc.callMcpTool)
	}
	if string(svc.callMcpReq.Arguments) != `{"path":"/tmp/a"}` || svc.callMcpReq.TimeoutMS != 1500 {
		t.Fatalf("请求体未透传: %+v", svc.callMcpReq)
	}
	var got dtocapability.McpToolCallResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("data 反序列化失败: %v", err)
	}
	if got.Text != "ok" || len(got.Content) != 1 {
		t.Fatalf("结果不符: %+v", got)
	}

	// 空 body = 无参数调用（不应报参数错误）。
	svc.callMcpReq = dtocapability.McpToolCallReq{}
	if env, code := do(t, r, "POST", "/v1/mate/mcps/fs/tools/t/call", ""); code != 200 || env.Code != 0 {
		t.Fatalf("空 body 应放行，got http=%d code=%d", code, env.Code)
	}
	if len(svc.callMcpReq.Arguments) != 0 || svc.callMcpReq.TimeoutMS != 0 {
		t.Fatalf("空 body 应零值: %+v", svc.callMcpReq)
	}

	// 非法 JSON body → 40000（参数错误）。
	if env, code := do(t, r, "POST", "/v1/mate/mcps/fs/tools/t/call", "{oops"); code != 200 || env.Code != 40000 {
		t.Fatalf("非法 body 应 40000，got http=%d code=%d", code, env.Code)
	}

	// 工具级失败：仍 code=0，is_error 表达。
	svc.callMcpResp = &dtocapability.McpToolCallResp{
		ServerID: "fs", Tool: "read_file", IsError: true,
		Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"权限不足"}`)}, Text: "权限不足",
	}
	env, _ = do(t, r, "POST", "/v1/mate/mcps/fs/tools/read_file/call", "")
	var failed dtocapability.McpToolCallResp
	_ = json.Unmarshal(env.Data, &failed)
	if env.Code != 0 || !failed.IsError {
		t.Fatalf("工具级失败应 code=0 + is_error=true，got code=%d %+v", env.Code, failed)
	}

	// 未知 server → 40401；传输不支持 → 40501；超时 → 40901。
	svc.callMcpResp = nil
	for _, tc := range []struct {
		err  error
		want int
	}{
		{gerror.Error{Code: 40401, Msg: `未知 MCP server "nope"`}, 40401},
		{gerror.Error{Code: 40501, Msg: "MCP sse 传输暂不支持"}, 40501},
		{gerror.Error{Code: 40901, Msg: "[mcp_timeout] tools/call 失败"}, 40901},
	} {
		svc.callMcpErr = tc.err
		if env, code := do(t, r, "POST", "/v1/mate/mcps/x/tools/t/call", ""); code != 200 || env.Code != tc.want {
			t.Fatalf("错误码透传不符 want=%d got http=%d code=%d", tc.want, code, env.Code)
		}
	}
}

// TestMcpResourcesPromptsRoutes B2：资源/提示词四条路由（路径与 body 参数透传、错误码透传）。
func TestMcpResourcesPromptsRoutes(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))

	env, code := do(t, r, "GET", "/v1/mate/mcps/fs/resources", "")
	if code != 200 || env.Code != 0 || svc.mcpLiveID != "fs" {
		t.Fatalf("GET resources 失败 http=%d code=%d id=%q", code, env.Code, svc.mcpLiveID)
	}
	var res dtocapability.McpResourcesResp
	_ = json.Unmarshal(env.Data, &res)
	if len(res.Resources) != 1 || res.Resources[0].URI != "file:///tmp/a.txt" {
		t.Fatalf("资源结果不符: %+v", res)
	}

	env, code = do(t, r, "POST", "/v1/mate/mcps/fs/resources/read", `{"uri":"file:///tmp/a.txt"}`)
	if code != 200 || env.Code != 0 || svc.mcpLiveURI != "file:///tmp/a.txt" {
		t.Fatalf("POST resources/read 失败 http=%d code=%d uri=%q", code, env.Code, svc.mcpLiveURI)
	}
	var rd dtocapability.McpResourceReadResp
	_ = json.Unmarshal(env.Data, &rd)
	if len(rd.Contents) != 1 || rd.Contents[0].Text != "hi" {
		t.Fatalf("读取结果不符: %+v", rd)
	}
	// 缺 uri → 桩返回空（真实实现报 40001）；非法 JSON → 40000。
	if env, code := do(t, r, "POST", "/v1/mate/mcps/fs/resources/read", "{oops"); code != 200 || env.Code != 40000 {
		t.Fatalf("非法 body 应 40000，got http=%d code=%d", code, env.Code)
	}

	env, code = do(t, r, "GET", "/v1/mate/mcps/fs/prompts", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET prompts 失败 http=%d code=%d", code, env.Code)
	}
	var ps dtocapability.McpPromptsResp
	_ = json.Unmarshal(env.Data, &ps)
	if len(ps.Prompts) != 1 || ps.Prompts[0].Name != "greet" {
		t.Fatalf("提示词列表不符: %+v", ps)
	}

	env, code = do(t, r, "POST", "/v1/mate/mcps/fs/prompts/greet/get", `{"arguments":{"who":"世界"}}`)
	if code != 200 || env.Code != 0 || svc.mcpLiveName != "greet" || svc.mcpLiveArgs["who"] != "世界" {
		t.Fatalf("POST prompts/get 失败 http=%d code=%d name=%q args=%v", code, env.Code, svc.mcpLiveName, svc.mcpLiveArgs)
	}
	var gp dtocapability.McpPromptGetResp
	_ = json.Unmarshal(env.Data, &gp)
	if len(gp.Messages) != 1 || gp.Messages[0].Role != "user" {
		t.Fatalf("提示词结果不符: %+v", gp)
	}
	// 空 body = 无参数取用。
	svc.mcpLiveArgs = map[string]string{"x": "y"}
	if env, code := do(t, r, "POST", "/v1/mate/mcps/fs/prompts/greet/get", ""); code != 200 || env.Code != 0 || len(svc.mcpLiveArgs) != 0 {
		t.Fatalf("空 body 应零值，got http=%d code=%d args=%v", code, env.Code, svc.mcpLiveArgs)
	}

	// 错误码透传：40401 / 40501 / 50201。
	for _, tc := range []struct {
		err  error
		want int
		path string
	}{
		{gerror.Error{Code: 40401, Msg: "未知 MCP server"}, 40401, "/v1/mate/mcps/nope/resources"},
		{gerror.Error{Code: 40501, Msg: "未知传输"}, 40501, "/v1/mate/mcps/s/prompts"},
		{gerror.Error{Code: 50201, Msg: "[mcp_resource_read_failed] x"}, 50201, "/v1/mate/mcps/fs/resources"},
	} {
		svc.mcpLiveErr = tc.err
		if env, code := do(t, r, "GET", tc.path, ""); code != 200 || env.Code != tc.want {
			t.Fatalf("错误码透传不符 want=%d got http=%d code=%d", tc.want, code, env.Code)
		}
	}
}

// TestResolvePermissionReceiptShape C5：POST /sessions/:id/permission 回执描述
// **实际投递结果**（allowed/rejected/always/请求 option_id/outcome/selected_option_id），
// 不再是 `{call_id, allowed}` 的请求参数回显。
func TestResolvePermissionReceiptShape(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))
	env, code := do(t, r, "POST", "/v1/mate/sessions/s1/permission",
		`{"call_id":"tc1","option_id":"allow_always","always":true,"allowed":true}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("决策信封不符: code=%d env=%+v", code, env)
	}
	var resp dtosession.PermissionDecisionResp
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("回执解析失败: %v data=%s", err, env.Data)
	}
	if resp.CallID != "tc1" || !resp.Allowed || resp.Rejected || !resp.Always ||
		resp.OptionID != "allow_always" || resp.Outcome != "selected" || resp.SelectedOptionID != "allow_always" {
		t.Fatalf("回执字段不符: %+v", resp)
	}
	for _, key := range []string{`"rejected"`, `"always"`, `"outcome"`, `"selected_option_id"`} {
		if !strings.Contains(string(env.Data), key) {
			t.Fatalf("回执缺字段 %s: %s", key, env.Data)
		}
	}
	// 拒绝决策：rejected=true、allowed=false（不再出现「allowed:false 但语义是拒绝」的含糊回执）。
	env, _ = do(t, r, "POST", "/v1/mate/sessions/s1/permission",
		`{"call_id":"tc2","option_id":"reject_once","allowed":false}`)
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("拒绝回执解析失败: %v", err)
	}
	if resp.Allowed || !resp.Rejected || resp.OptionID != "reject_once" {
		t.Fatalf("拒绝回执字段不符: %+v", resp)
	}
}

// stubLibrary 让 stubSvc 满足 Library 端口（Library 读模型在 manager 侧有真装配测试）。
func (s *stubSvc) ListLibrary(q svclibrary.Query) ([]dtolibrary.LibraryItem, error) {
	s.libraryQueried = q
	if s.capErr != nil {
		return nil, s.capErr
	}
	return []dtolibrary.LibraryItem{{
		ID: "demo", Kind: dtolibrary.KindSkill, Name: "演示", Origin: dtolibrary.OriginLocal,
		Scope: "global", Enabled: true, SourceFile: "/tmp/skills/demo.json",
		Applicability: []dtolibrary.Applicability{
			{Runtime: "rtx", OK: true, Status: dtolibrary.StatusProjected, Reason: "投放到 /tmp/skills"},
		},
	}}, nil
}

// TestLibraryRouteShape GET /library：过滤参数透传领域层 + 逐 runtime 可达性原样透出 + 领域错误映射。
func TestLibraryRouteShape(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(svc))

	env, httpCode := do(t, r, "GET", "/v1/mate/library?kind=skill&q=dem", "")
	if httpCode != 200 || env.Code != 0 {
		t.Fatalf("GET /library 应成功: http=%d env=%+v", httpCode, env)
	}
	if svc.libraryQueried.Kind != "skill" || svc.libraryQueried.Q != "dem" {
		t.Fatalf("过滤参数应透传到领域层: %+v", svc.libraryQueried)
	}
	var items []dtolibrary.LibraryItem
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("data 应是 LibraryItem 数组: %v（%s）", err, env.Data)
	}
	if len(items) != 1 || len(items[0].Applicability) != 1 || !items[0].Applicability[0].OK {
		t.Fatalf("逐 runtime 可达性应原样透出: %+v", items)
	}
	if items[0].Origin != dtolibrary.OriginLocal || items[0].SourceFile == "" {
		t.Fatalf("origin/source_file 应透出: %+v", items[0])
	}

	// 未知 kind 的**校验在领域层**（控制器只透传 query），这里验证领域错误经信封落成 40000。
	svc.capErr = code.Error(code.InvalidParam, "未知 kind \"command\"")
	env, httpCode = do(t, r, "GET", "/v1/mate/library?kind=command", "")
	if httpCode != 200 || env.Code != code.InvalidParam {
		t.Fatalf("领域层拒绝应落成 40000: http=%d env=%+v", httpCode, env)
	}
}
