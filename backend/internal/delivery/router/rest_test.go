package router_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/morehao/ark-mate/internal/domain/session/sessionrow"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/infra/dsh"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/bootstrap/assembly"
	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/delivery/router"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/domain/mcp"
	dommcp "github.com/morehao/ark-mate/internal/domain/mcp"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/drivers/mechanism/credential"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/dto/dtosearch"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/dto/dtousage"
	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/persistence/store"
	goruntime "github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/service/svcautomation"
	"github.com/morehao/ark-mate/internal/service/svccapability"
	"github.com/morehao/ark-mate/internal/service/svccommandpolicy"
	"github.com/morehao/ark-mate/internal/service/svclibrary"
	"github.com/morehao/ark-mate/internal/service/svcmodel"
	"github.com/morehao/ark-mate/internal/service/svcruntime"
	"github.com/morehao/ark-mate/internal/service/svcsearch"
	"github.com/morehao/ark-mate/internal/service/svcsession"
	"github.com/morehao/ark-mate/internal/service/svcstream"
	"github.com/morehao/ark-mate/internal/service/svcsystem"
	"github.com/morehao/ark-mate/internal/service/svcusage"
	"github.com/morehao/ark-mate/internal/service/svcworkspace"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/golib/gerror"
)

// ——— stub Service（handler.Service 的测试替身，记录调用） ———

type stubSvc struct {
	listWorkspaceID string // 最近一次 ListSessions 的归属过滤参数（v14）

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
	pickerMode  string
	recentLimit int // 最近一次 RecentWorkspaces 的 limit 参数
	recentResp  []dtoworkspace.WorkspaceRecentResp
	recentErr   error

	settingsErr    error
	settingsCalled bool     // UpdateWorkspaceSettings 是否被调用（"传了空名单"与"没调用"必须能区分）
	settingsRoots  []string // 最近一次 UpdateWorkspaceSettings 收到的 roots

	// v14 工作空间登记表（桩只记参 + 透传；领域判断全在 manager）。
	registryOne          *dtoworkspace.WorkspaceResp
	registryDelete       *dtoworkspace.WorkspaceDeleteResp
	registryMove         *dtoworkspace.WorkspaceMoveResp
	registryErr          error
	registryCreatePath   string
	registryCreateTitle  string
	registryCreateStrict bool
	registryRenameTitle  string
	registryID           string
	registryMoveBefore   string

	// automation 面（M4-5）桩状态

	acpErr          error                              // 任意端点的裸 acp 错误注入（信封映射用）
	resolvePermErr  *gerror.Error                      // 权限决策桩错误注入
	resolvePermResp *dtosession.PermissionDecisionResp // 权限决策桩回执（nil = 默认构造）

	modelErr error // 模型面桩错误注入（v13 起 provider 档案与自检走真域平面，不再经替身）

	createCalled bool
	createReq    dtosession.SessionCreateReq
	createResp   *dtosession.SessionCreateResp
	createErr    error

	promptCalled   bool
	promptID       string
	promptText     string
	promptDelivery string
	promptResp     *dtosession.SessionPromptResp
	promptErr      error

	cancelCalled    bool
	deliverQueuedID string
	cancelID        string
	cancelErr       error

	memList     []interaction.MemoryItem
	revokeID    string
	revokeTitle string
	revokeKind  string
	revokeErr   error

	pendingPerms         []interaction.PermissionItem
	elicitations         []interaction.ElicitationItem
	resolvedElicID       string
	resolvedElicAction   string
	resolvedElicContent  map[string]any
	resolvedElicLate     bool
	cancelledElicSession string
	resolveElicErr       error

	skills []dtocapability.SkillResp // P1 能力数据面桩
	// §13.4 S4 启停桩
	matQueried         string
	skillToggled       bool
	skillToggleID      string
	skillToggleEnabled bool
	capErr             error // 能力列表桩错误注入

	// M2 写面桩
	skillWritten  string // create|update|delete
	skillWriteID  string
	skillWriteReq skill.WriteRequest
	mcpWritten    string
	mcpWriteID    string
	mcpWriteReq   dommcp.WriteRequest

	// 会话元数据写面的三份依赖（P4a 正文第四十四刀）。
	resolvedModelRuntime string
	resolvedModelID      string
	resolveModelErr      error
	appliedModelSession  string
	appliedModelCfg      goruntime.ModelConfig
	applyModelErr        error
	applyModelInactive   bool // ApplyModel 回 false（会话不活跃）
	metaPatchedID        string
	metaPatch            model.SessionMetaPatch
}

// RestErr 桩：与 manager 一致——已映射错误透传，acp 错误按分类映射（测试只关心 acp→信封码）。
func (s *stubSvc) RestErr(err error) error {
	if err == nil {
		return nil
	}
	var ge gerror.Error
	if errors.As(err, &ge) {
		return err
	}
	var ae *acp.Error
	if errors.As(err, &ae) {
		code := 40001
		switch ae.Class {
		case acp.ClassUnsupported:
			code = 40501
		case acp.ClassLimit:
			code = 40901
		case acp.ClassService, acp.ClassConnection:
			code = 50201
		}
		if ae.Code == "agent_not_found" || ae.Code == "session_not_found" || ae.Code == "runtime_not_found" || ae.Code == "mcp_not_found" {
			code = 40401
		}
		return gerror.Error{Code: code, Msg: ae.Details}
	}
	return gerror.Error{Code: 50000, Msg: err.Error()}
}

func (s *stubSvc) ListModels(_ context.Context) ([]dtomodel.ModelResp, error) {
	if s.modelErr != nil {
		return nil, s.modelErr
	}
	return nil, nil
}
func (s *stubSvc) CreateModel(_ context.Context, req dtomodel.ModelCreateReq) (*dtomodel.ModelResp, error) {
	if s.modelErr != nil {
		return nil, s.modelErr
	}
	return &dtomodel.ModelResp{ID: "m1", ProviderID: req.ProviderID, Model: req.Model}, nil
}
func (s *stubSvc) UpdateModel(_ context.Context, id string, patch dtomodel.ModelPatchReq) (*dtomodel.ModelResp, error) {
	if s.modelErr != nil {
		return nil, s.modelErr
	}
	return &dtomodel.ModelResp{ID: id}, nil
}
func (s *stubSvc) DeleteModel(_ context.Context, id string) (*dtomodel.ModelDeleteResp, error) {
	if s.modelErr != nil {
		return nil, s.modelErr
	}
	return &dtomodel.ModelDeleteResp{Deleted: true}, nil
}
func (s *stubSvc) SyncModels(_ context.Context, runtimeID string) (*dtomodel.ModelSyncResp, error) {
	if s.modelErr != nil {
		return nil, s.modelErr
	}
	return &dtomodel.ModelSyncResp{Applied: []dtomodel.RuntimeSyncResp{{Runtime: runtimeID, Pending: true}}}, nil
}
func (s *stubSvc) ModelsView(_ context.Context, runtimeID string, refresh bool) (*dtomodel.ModelsResp, error) {
	if s.modelErr != nil {
		return nil, s.modelErr
	}
	return &dtomodel.ModelsResp{}, nil
}

// stubCatalog 把 `stubSvc` 的 DTO 目录面收窄成 `svcmodel.Catalog` 要的领域形状。
//
// 同一个 stub 不能同时满足两个接口的同名方法（返回类型不同），而 `stubSvc` 还要供
// `svcmodel.ModelSvc` 用——所以这里显式包一层，而不是把两个接口的签名再拉平一次。
type stubCatalog struct{ s *stubSvc }

func (c stubCatalog) ModelCatalog(ctx context.Context) (*provider.CatalogView, error) {
	v, err := c.s.ModelCatalog(ctx)
	if err != nil || v == nil {
		return nil, err
	}
	return &provider.CatalogView{Runtime: v.Runtime, Source: v.Source, Options: nil}, nil
}

func (s *stubSvc) ModelCatalog(_ context.Context) (*dtomodel.ModelCatalogResp, error) {
	if s.modelErr != nil {
		return nil, s.modelErr
	}
	return &dtomodel.ModelCatalogResp{Runtime: "stub", Source: "none"}, nil
}

// ——— P1 能力数据面桩（ListSkills/ListMcpServers/ListTools） ———
func (s *stubSvc) SetSkillEnabled(_ context.Context, id string, enabled bool) (*dtocapability.SkillToggleResp, error) {
	s.skillToggled, s.skillToggleID, s.skillToggleEnabled = true, id, enabled
	if s.capErr != nil {
		return nil, s.capErr
	}
	return &dtocapability.SkillToggleResp{ID: id, Enabled: enabled, NextAction: "已生效"}, nil
}

// M2 写面桩：只记录被调用与入参，返回固定结果（真实的落盘/闸门/回滚在 manager 的单测里验）。
func (s *stubSvc) CreateSkill(_ context.Context, req skill.WriteRequest) (*dtocapability.SkillWriteResp, error) {
	s.skillWritten, s.skillWriteReq = "create", req
	if s.capErr != nil {
		return nil, s.capErr
	}
	return &dtocapability.SkillWriteResp{ID: "s1", Path: "/tmp/skills/s1.json", Created: true, NextAction: "已新建"}, nil
}

func (s *stubSvc) UpdateSkill(_ context.Context, id string, req skill.WriteRequest) (*dtocapability.SkillWriteResp, error) {
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

func (s *stubSvc) CreateMcpServer(_ context.Context, req dommcp.WriteRequest) (*dtocapability.McpWriteResp, error) {
	s.mcpWritten, s.mcpWriteReq = "create", req
	if s.capErr != nil {
		return nil, s.capErr
	}
	return &dtocapability.McpWriteResp{ID: "m1", Path: "/tmp/mcp/m1.json", Created: true, NextAction: "已新建"}, nil
}

func (s *stubSvc) UpdateMcpServer(_ context.Context, id string, req dommcp.WriteRequest) (*dtocapability.McpWriteResp, error) {
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

func (s *stubSvc) UpdateWorkspaceSettings(roots []string) (*dtoworkspace.WorkspaceSettingsResp, error) {
	s.settingsCalled = true
	s.settingsRoots = roots
	if s.settingsErr != nil {
		return nil, s.settingsErr
	}
	return &dtoworkspace.WorkspaceSettingsResp{}, nil
}

// ——— v14 工作空间登记表桩 ———
//
// 这一组桩只做**透传 + 记参**：控制器在本轮里没有任何领域判断（校验、默认工作空间、
// 归属解析全在 manager），所以路由测试要证明的也只是"路径与参数原样到了端口、
// 错误按带码信封出去"。领域语义由 manager 与 store 的测试覆盖。

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

// ——— M3 ② 消息检索 / 批量导出桩 ———
// ——— P4a：自检用例的**供应商域读面**替身（同上：只交事实） ———

// ——— 会话元数据写面的三份依赖（P4a 正文第四十四刀）：端口只吃域/pkg 类型 ———
//
// 替身不再提供"折好的 SessionResp"——那一层现在真的在用例里跑（子集语义、模型解析失败即上抛、
// 落库口径），替身只交这三份事实。
func (s *stubSvc) ResolveModel(_ context.Context, runtimeID, modelID string) (string, goruntime.ModelConfig, error) {
	s.resolvedModelRuntime, s.resolvedModelID = runtimeID, modelID
	if s.resolveModelErr != nil {
		return "", goruntime.ModelConfig{}, s.resolveModelErr
	}
	return modelID, goruntime.ModelConfig{Model: modelID}, nil
}
func (s *stubSvc) ApplyModel(_ context.Context, sessionID string, cfg goruntime.ModelConfig) (bool, error) {
	s.appliedModelSession, s.appliedModelCfg = sessionID, cfg
	return !s.applyModelInactive, s.applyModelErr
}
func (s *stubSvc) UpdateMeta(_ context.Context, id string, meta model.SessionMetaPatch) {
	s.metaPatchedID, s.metaPatch = id, meta
}

// ——— 会话交互面的三份事实（P4a 正文第四十刀）：端口吃**域值对象**，投影在用例层 ———
//
// 替身只提供事实、不再提供"已经折好的响应"——后者会让"投影真的发生了吗"无从验证
// （用例层把 Kind 丢掉、把 schema/url 弄反，替身时代都照样绿）。
func (s *stubSvc) PendingPermissions(string) []interaction.PermissionItem { return s.pendingPerms }
func (s *stubSvc) PermissionMemories(string) []interaction.MemoryItem     { return s.memList }
func (s *stubSvc) PendingElicitations(string) []interaction.ElicitationItem {
	return s.elicitations
}

// ResolvePermission 投递口替身：只回**域回执**（Kind/OptionID），DTO 由用例层折。
func (s *stubSvc) ResolvePermission(sessionID, callID, optionID string, always, allowed bool) (interaction.DecisionOutcome, bool, error) {
	if s.resolvePermErr != nil {
		return interaction.DecisionOutcome{}, false, *s.resolvePermErr
	}
	return interaction.DecisionOutcome{Kind: "selected", OptionID: optionID}, true, nil
}
func (s *stubSvc) RevokeMemory(sessionID, title, kind string) (bool, error) {
	s.revokeID = sessionID
	s.revokeTitle = title
	s.revokeKind = kind
	return true, s.revokeErr
}
func (s *stubSvc) ResolveElicitation(sessionID, elicitationID, action string, content map[string]any, late bool) (bool, error) {
	s.resolvedElicID = elicitationID
	s.resolvedElicAction = action
	s.resolvedElicContent = content
	s.resolvedElicLate = late
	return true, s.resolveElicErr
}

// CancelElicitations 归档时作废挂起提问（ADR-11）：替身只记账。
func (s *stubSvc) CancelElicitations(sessionID string) {
	s.cancelledElicSession = sessionID
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

// DeliverQueuedNow 「立即发送」在空闲态的落点（ADR-10）：替身只记账，不真的起一轮。
func (s *stubSvc) DeliverQueuedNow(_ context.Context, id string) error {
	s.deliverQueuedID = id
	return nil
}

// DeliverQueued 执行面的投递能力（`svcsession.QueuedDelivery`）。
func (s *stubSvc) DeliverQueued(id string) { s.deliverQueuedID = id }

// ListRuns 桩：默认空 runs（详情/统计路径不依赖其返回）。

// ——— 测试基建 ———

type envelope struct {
	Code      int             `json:"code"`
	RequestID string          `json:"requestID"`
	Msg       string          `json:"msg"`
	Data      json.RawMessage `json:"data"`
}

// sessionPlaneForRouter 造一个可用的会话域平面：只接**归属展示**这一个端口，
// 因为 `ListSessions` 释放锁后要解析 workspace 展示字段，端口为空会 nil panic（真实现见 manager）。
func sessionPlaneForRouter() *session.Plane {
	pl := session.NewPlane()
	pl.SetWorkspaceDisplay(func(id string) (string, string) { return id, "" })
	return pl
}

// restFixture 路由级测试的**事实集合**：控制器在 `newRestEngine` 里按当前字段现造。
//
// 为什么不在 `stubDeps` 里就构造好控制器：多个用例先改事实（挂 store / 换 automation 平面 /
// 注册会话）再起引擎；提前构造会让"改了事实、控制器还拿旧的那份"变成静默漂移。
type restFixture struct {
	svc             *stubSvc
	plane           *session.Plane
	bus             *eventbus.Bus
	store           *store.Store
	caps            *capability.Registry
	providerPlane   *provider.Plane
	automationPlane *automation.Plane
	execution       *runtimeconn.Plane
	registry        *goruntime.Registry
	mcpPlane        *mcp.Plane
	mcpSecrets      *mcp.Secrets
	skills          *skill.Plane
	wsPlane         *workspace.Plane
}

// stubDeps 用同一个测试替身填满会话/交互/模型那一批用例端口；域平面用**真对象**。
//
// v3.1 起控制器只收已构造的用例接口：这里保留"事实集合"，`newRestEngine` 用与生产同一条
// `assembly.Services.BuildControllers` 现造控制器——路由测试既保留替身行为断言，又不与真实装配漂移。
func stubDeps(t *testing.T, svc *stubSvc) restFixture {
	t.Helper()
	reg := dsh.Registry()
	caps := capability.NewRegistry()
	skills := skill.NewPlane(caps, nil)
	mcpSecrets := mcp.NewSecrets(credential.New(reg))
	return restFixture{
		svc:             svc,
		plane:           sessionPlaneForRouter(),
		bus:             eventbus.New(),
		caps:            caps,
		providerPlane:   testProviderPlane(t),
		automationPlane: newAutomationPlaneForTest(),
		execution:       runtimeconn.NewPlaneFor(reg),
		registry:        reg,
		mcpPlane:        mcp.NewPlane(caps, nil, mcpSecrets),
		mcpSecrets:      mcpSecrets,
		skills:          skills,
		wsPlane:         workspace.NewPlane(),
	}
}

// services 按**当前**事实构造用例集合。
func (f restFixture) services() assembly.Services {
	bus := f.bus
	if bus == nil {
		bus = eventbus.New()
	}
	return assembly.Services{
		Session:            f.svc,
		SessionHistory:     svcsession.NewHistory(nilSafeEventReader(f.store), f.plane, bus),
		SessionDetail:      svcsession.NewDetail(f.plane, f.svc),
		SessionInteraction: svcsession.NewInteraction(f.plane, f.svc),
		SessionQueue:       svcsession.NewQueue(f.plane, f.svc),
		SessionMeta:        svcsession.NewMeta(f.plane, f.svc),
		SessionRead:        svcsession.NewRead(f.plane),
		Search:             svcsearch.New(f.plane),
		Usage:              svcusage.New(f.plane),
		Export:             svcsystem.New(svcsystem.NewSource(nilSafeSourceFacts(f.store), f.plane, bus)),

		Workspace:     svcworkspace.New(f.wsPlane, workspace.NewPicker()),
		CommandPolicy: svccommandpolicy.New("", func([]interaction.PrefixRule) {}),

		Runtime:       svcruntime.New(f.execution),
		Model:         f.svc,
		ModelDeclared: svcmodel.NewDeclared(nilSafeModelStore(f.store), f.providerPlane, nil),
		Provider:      svcmodel.NewProvider(f.providerPlane),
		ProviderProbe: svcmodel.NewProbe(f.providerPlane, stubCatalog{f.svc}),

		Capability: svccapability.NewCapability(f.skills, f.mcpPlane,
			svccapability.NewTools(f.caps), svccapability.NewServers(f.caps, f.mcpPlane, f.mcpSecrets)),
		Automation: svcautomation.New(f.automationPlane),
		Library:    svclibrary.NewLibrary(f.caps, f.skills, f.registry, f.mcpSecrets, func() bool { return true }),

		Meta:         svcsystem.NewMeta(func() svcsystem.MetaSource { return svcsystem.MetaSource{} }),
		Capabilities: svcsystem.NewCapabilities(),
		Stream:       svcstream.New(bus),
	}
}

// deps 现造 router.Deps（控制器在此时按当前事实构造）。
func (f restFixture) deps() router.Deps {
	return router.Deps{Controllers: f.services().BuildControllers(nil, nil, nil)}
}

// newRestEngine 走**真实路由装配**（router.New）：路径、鉴权与中间件形态都与线上一致。
func newRestEngine(deps restFixture) *gin.Engine {
	return router.New(deps.deps())
}

// TestWorkspaceSettingsCtrPassThrough 设置面的控制器职责：GET 透出快照、PUT 把 roots 原样交给
// 领域层（语义判断全在 manager，handler 不做第二套）、body 非法回 40000、领域错误经 RestErr 映射。
func TestWorkspaceSettingsCtrPassThrough(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(t, svc))

	env, _ := do(t, r, "GET", "/v1/mate/settings/workspace", "")
	if env.Code != 0 {
		t.Fatalf("GET 应成功: %+v", env)
	}
	var got dtoworkspace.WorkspaceSettingsResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	// 快照这一格自 P4a 正文第十九刀起走**真域平面**（读模型由域折好 DTO）：这里只证
	// "信封形状对 + 空平面如实自报不可写、来源 unavailable"，"配置过的名单原样透出"由
	// `internal/manager` 的设置面测试用真策略/真端口钉（那边才是判定的本体）。
	// 空名单在 JSON 上被 omitempty 抹成缺省（前端按"没有"渲染），这里只钉状态两格。
	if got.Writable || got.Source != "unavailable" {
		t.Fatalf("空平面快照形状不符: %+v", got)
	}

	// PUT 自 P4a 正文第三十一刀起也走**真域平面**（`h.plane.UpdateSettings`）：这一格能证的是
	// "领域错误经带码信封原样出去"——本用例的平面没有配置目录，写面给的就是 `settings_readonly`
	// （code 40001）。roots 是否原样下发（含空数组 / 字段缺省 = 取消限制）由
	// `internal/manager` 的设置面测试用**真策略 + 假端口**钉（那边才是判定的本体）。
	for _, body := range []string{`{"roots":["/srv/docs"]}`, `{"roots":[]}`, `{}`} {
		env, _ = do(t, r, "PUT", "/v1/mate/settings/workspace", body)
		if env.Code != 40001 || !strings.Contains(env.Msg, "未启用设置持久化") {
			t.Fatalf("PUT %s 应把域平面的 `settings_readonly` 原样透出: %+v", body, env)
		}
	}

	// body 非法 → 参数错误码（不是 1：面板要能区分"我传错了"与"服务端坏了"）。
	if env, _ = do(t, r, "PUT", "/v1/mate/settings/workspace", `{`); env.Code != 40000 {
		t.Fatalf("非法 body 应 40000: %+v", env)
	}

	// 领域拒绝的三种语义码（锁定 / 只读 / 非法目录）不再由替身伪造：`settings_readonly` 已由上面
	// 那一组用**真平面**钉住，`settings_locked` 与 `workspace_invalid` 由 `internal/manager` 的
	// 设置面测试用真策略（env 锁 / 非法目录）钉——判定的本体在域里，替身只会重复它自己的假设。
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
		registryDelete: &dtoworkspace.WorkspaceDeleteResp{ID: "wsp-1", ReleasedSessions: 12},
		registryMove:   &dtoworkspace.WorkspaceMoveResp{IDs: []string{"wsp-2", "wsp-1"}},
	}
	r := newRestEngine(stubDeps(t, svc))

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
	// 列表这一格自 P4a 正文第十八刀起走**真域平面**（读模型由域折好 DTO），所以这里只证
	// "信封形状对、真的问到了平面"；"登记过的行原样透出"由 `TestWorkspaceRegistryHTTPFlow`
	// 用真登记流程钉（那条测试建表、建默认工作空间、创建、列出、重排、改名、删除全走真对象）。
	if list.Writable {
		t.Fatalf("空平面不该自称可写（memory 模式且未接库）: %+v", list)
	}
	if list.Items == nil {
		t.Fatalf("列表必须是空数组而不是 null（前端按数组渲染）: %+v", list)
	}

	// 写面（POST/PATCH/DELETE/move）自 P4a 正文第二十七刀起走**真域平面**（投影与错误码都在域里，
	// 控制器直接 `h.plane.*`），所以这一格能证的只有"领域错误经带码信封原样出去"这一条：
	// memory 无库平面（本用例的 plane 没接持久层）对写面给的就是 `workspace_schema_read_only`。
	// 参数是否原样到端口（path/title/strict/before_id）由 `TestWorkspaceRegistryHTTPFlow` 用
	// **真对象**钉（它建表、建默认工作空间、创建、重排、改名、删除全走真库），不再是替身自报。
	// `POST /workspaces` 自第二十九刀起也走真域平面，同样并入下面这一组。
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/v1/mate/workspaces", `{"path":"/Users/me/work/ark-mate","title":"昵称","strict":true}`},
		{"PATCH", "/v1/mate/workspaces/wsp-1", `{"title":"新名字"}`},
		{"DELETE", "/v1/mate/workspaces/wsp-1", ""},
		{"POST", "/v1/mate/workspaces/wsp-1/move", `{"before_id":"wsp-2"}`},
	} {
		env, _ = do(t, r, c.method, c.path, c.body)
		if env.Code != 40501 || !strings.Contains(env.Msg, "workspace_schema_read_only") {
			t.Fatalf("%s %s 应把域平面的 `workspace_schema_read_only` 原样透出: %+v", c.method, c.path, env)
		}
	}

	// 非法 body → 参数错误码（不是 1：面板要能区分"我传错了"与"服务端坏了"）。
	if env, _ = do(t, r, "POST", "/v1/mate/workspaces", `{`); env.Code != 40000 {
		t.Fatalf("非法 body 应 40000: %+v", env)
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
	// 自 P4a 正文第三十二刀起浏览面走**真域平面**（投影 + 路径校验都在域里），所以这里不再造
	// 替身响应，而是给它一个**真存在的临时目录**：能证明的比原来更强——静态段没被 `:id` 抢走，
	// 且域平面真的读到了盘上那一份（`browse` 若被当成 id，会走登记表读面而报 40001）。
	dir := t.TempDir()
	r := newRestEngine(stubDeps(t, &stubSvc{}))

	env, _ := do(t, r, "GET", "/v1/mate/workspaces/browse?path="+dir, "")
	if env.Code != 0 {
		t.Fatalf("GET /workspaces/browse 应打到 Browse 而不是被 :id 抢走: %+v", env)
	}
	var got dtoworkspace.WorkspaceBrowseResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	// 域里做了符号链接规范化（macOS 的 `/var` → `/private/var`），所以按**规范化后**的期望值比。
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got.Path != resolved {
		t.Fatalf("浏览结果应指向请求的那条路径（被当成 id 打歪了）: got=%q want=%q", got.Path, resolved)
	}
	// 静态段与带参段的**形状不同**：带参 GET 走登记表读面（返回 `items` 列表），浏览面回的是
	// 单目录的形状（`path` + `entries`/`crumbs`）。这里断言浏览面特有的那两格在。
	if got.Crumbs == nil {
		t.Fatalf("浏览面必须回 crumbs 祖先链（被 :id 抢走时不会有它）: %+v", got)
	}
}

// TestWorkspaceCreateDirRoute 目录浏览面的写面（`POST /workspaces/dirs`，v14.3）。
//
// 两层守卫，缺一不可：
//   - **路径不能被 `/workspaces/:id` 的写路由抢走**：本组同时有 `POST /workspaces/:id/move`，
//     而 `dirs` 是静态段。抢走的坏法是静默的——「新建文件夹」突然报"未知工作空间"；
//   - **参数原样到端口**：这一半自第三十二刀起由**真域平面**证明——给它一个真存在的父目录，
//     建出来的目录必须在盘上真的出现（替身自报做不到这一点）。
func TestWorkspaceCreateDirRoute(t *testing.T) {
	parent := t.TempDir()
	r := newRestEngine(stubDeps(t, &stubSvc{}))

	env, _ := do(t, r, "POST", "/v1/mate/workspaces/dirs", `{"parent":"`+parent+`","name":"新项目"}`)
	if env.Code != 0 {
		t.Fatalf("POST /workspaces/dirs 应成功: %+v", env)
	}
	var got dtoworkspace.WorkspaceDirCreateResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if want := filepath.Join(resolved, "新项目"); got.Path != want {
		t.Fatalf("建目录应回新建的那条路径: got=%q want=%q", got.Path, want)
	}
	if st, err := os.Stat(got.Path); err != nil || !st.IsDir() {
		t.Fatalf("目录必须真的建出来（真平面 + 真盘）: err=%v path=%q", err, got.Path)
	}
}

// TestWorkspaceCreateDirRejections 三种拒绝必须是三个不同的码：
// 越界 40001 / 名字非法 40000 / 已存在 40901——它们的界面引导不同（改路径 vs 改名）。
//
// 自第三十二刀起这一组用**真平面 + 真盘**造三种情形（替身伪造错误码只能证明"控制器会转发"，
// 证明不了"域真的这么判"）：越界 = 白名单外的父目录、名字非法 = 带分隔符、已存在 = 同名的先建一次。
func TestWorkspaceCreateDirRejections(t *testing.T) {
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "已有"), 0o755); err != nil {
		t.Fatalf("夹具建目录失败: %v", err)
	}
	cases := []struct {
		name string
		body string
		code int
	}{
		{"父目录不存在（= 越界/不可访问）", `{"parent":"/nope/not-here","name":"x"}`, 40001},
		{"名字非法（含分隔符）", `{"parent":"` + parent + `","name":"a/b"}`, 40001},
		{"已存在", `{"parent":"` + parent + `","name":"已有"}`, 40901},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRestEngine(stubDeps(t, &stubSvc{}))
			env, _ := do(t, r, "POST", "/v1/mate/workspaces/dirs", tc.body)
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
// P4a 正文第十一刀起这一格走**真域平面**（读模型由域直接提供，`manager` 侧转发已删），
// 所以夹具是"注册进平面的会话"而不是替身返回值。
func TestListEndpoints(t *testing.T) {
	deps := stubDeps(t, &stubSvc{})
	deps.plane.RegisterSession(&session.SessionRec{
		ID: "s1", RuntimeID: "rtx", Status: "completed", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	r := newRestEngine(deps)

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

// TestListSessionsWorkspaceQuery GET /sessions 的 `cwd` 过滤与排序。
//
// 原来这里验的是"cwd 透传给 service"（替身标志位）。换成真平面之后透传不再可观测，
// 于是改成验**过滤结果本身**：两个不同 cwd 的会话，带 cwd 只回一个、不带 cwd 回两个；
// 相对路径属参数错误直接 40000（仍在 handler 层拦，两个会话一个都不该出现）。
func TestListSessionsWorkspaceQuery(t *testing.T) {
	deps := stubDeps(t, &stubSvc{})
	now := time.Now()
	deps.plane.RegisterSession(&session.SessionRec{
		ID: "c-1", RuntimeID: "rtx", Status: "completed", Cwd: "/work/alpha",
		WorkspaceTemporary: true, CreatedAt: now, UpdatedAt: now,
	})
	deps.plane.RegisterSession(&session.SessionRec{
		ID: "c-2", RuntimeID: "rtx", Status: "completed", Cwd: "/work/beta",
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	})
	r := newRestEngine(deps)

	env, code := do(t, r, "GET", "/v1/mate/sessions?cwd=/work/alpha", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d env=%+v", code, env)
	}
	var list []dtosession.SessionResp
	if err := json.Unmarshal(env.Data, &list); err != nil || len(list) != 1 {
		t.Fatalf("按 cwd 过滤应只剩一个: %s", env.Data)
	}
	if list[0].Cwd != "/work/alpha" || !list[0].WorkspaceTemporary {
		t.Fatalf("工作空间字段未回读: %+v", list[0])
	}

	// 未指定 cwd → 不过滤（两个都在，且按创建时间倒序：c-1 更新 → 在前）。
	env, code = do(t, r, "GET", "/v1/mate/sessions", "")
	if code != 200 {
		t.Fatalf("无 cwd 应 200，got %d", code)
	}
	if err := json.Unmarshal(env.Data, &list); err != nil || len(list) != 2 {
		t.Fatalf("无 cwd 应回全部: %s", env.Data)
	}
	if list[0].ID != "c-1" || list[1].ID != "c-2" {
		t.Fatalf("应按创建时间倒序: %s", env.Data)
	}

	// 相对路径 → 40000（在 handler 层拦截，不进入域）。
	env, _ = do(t, r, "GET", "/v1/mate/sessions?cwd=relative/dir", "")
	if env.Code != 40000 {
		t.Fatalf("相对 cwd 应 40000，got %+v", env)
	}
}

// TestUsageSummaryRoute M3：GET /usage/summary **读模型**（P4a 正文第四十三刀起由会话域平面直接提供）。
//
// 三种自然答复都在这里验，替身（`usageResp`/`usageQuery`/`usageErr`）随之退场：
//
//	内存模式（无 store）→ 40501 `usage_stats_unsupported`（明确降级，不返回假零值）；
//	store 模式 → 真聚合，且 `from`/`to` 由域**解析后原样回带**——参数透传不再是"替身记下了什么"；
//	非法 group_by / 反向区间 → 40001（两条判据都在域里，与 `agent_id` 的拒绝同一处）。
func TestUsageSummaryRoute(t *testing.T) {
	// ① 内存模式：明确 40501。
	rMem := newRestEngine(stubDeps(t, &stubSvc{}))
	if env, code := do(t, rMem, "GET", "/v1/mate/usage/summary", ""); code != 200 || env.Code != 40501 {
		t.Fatalf("内存模式应 40501: http=%d env=%+v", code, env)
	}

	// ② store 模式：真平面 + 真库。
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	t.Cleanup(func() { _ = st.Close() })
	deps := stubDeps(t, &stubSvc{})
	deps.plane.SetStore(st)
	r := newRestEngine(deps)

	env, code := do(t, r, "GET", "/v1/mate/usage/summary?from=2026-09-01&to=2026-09-09&group_by=day", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d env=%+v", code, env)
	}
	var resp dtousage.UsageSummaryResp
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	// 日期形式按"该日 00:00 UTC"解析后**原样回带**（半开区间语义靠它成立）——这就是参数透传的证据。
	if resp.GroupBy != "day" || resp.Source != "sqlite" || resp.TZOffset != 0 ||
		resp.From != "2026-09-01T00:00:00Z" || resp.To != "2026-09-09T00:00:00Z" {
		t.Fatalf("响应载荷不符: %+v", resp)
	}
	// 空 runs 表 → 零值合计 + 桶是 `[]`（不是 null；前端按数组渲染）。
	if resp.Totals.Runs != 0 || !strings.Contains(string(env.Data), `"buckets":[]`) {
		t.Fatalf("空库应回零值合计与空桶数组: %s", env.Data)
	}

	// ②b 客户端时区：日期参数按 tz_offset 解释（UTC+8 的 9 月 1 日 = 8 月 31 日 16:00Z），
	// 响应回显生效偏移。不带它 = UTC（上一段已验），所以这条边只会被"传了但没生效"打破。
	env, code = do(t, r, "GET", "/v1/mate/usage/summary?from=2026-09-01&to=2026-09-09&tz_offset=480", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("tz_offset 信封不符: http=%d env=%+v", code, env)
	}
	resp = dtousage.UsageSummaryResp{}
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("tz_offset data 解析: %v", err)
	}
	if resp.TZOffset != 480 || resp.From != "2026-08-31T16:00:00Z" || resp.To != "2026-09-08T16:00:00Z" {
		t.Fatalf("tz_offset=480 应把日期解释成 +08:00 的当日零点: %+v", resp)
	}

	// ③ 非法 group_by / 反向区间 / 越界时区 → 40001（域里判，控制器不代判）。
	if env, _ := do(t, r, "GET", "/v1/mate/usage/summary?group_by=tool", ""); env.Code != 40001 {
		t.Fatalf("非法 group_by 应 40001: %+v", env)
	}
	if env, _ := do(t, r, "GET", "/v1/mate/usage/summary?tz_offset=999", ""); env.Code != 40001 {
		t.Fatalf("越界 tz_offset 应 40001: %+v", env)
	}
	if env, _ := do(t, r, "GET", "/v1/mate/usage/summary?from=2026-09-09&to=2026-09-01", ""); env.Code != 40001 {
		t.Fatalf("反向区间应 40001: %+v", env)
	}
}

// TestUnclassifiedErrorFallsBackToInternal 裸错误（不带码）→ code=50000，msg 原样带出。
//
// 历史形态是 code=1：那时控制器各处手写 gerror.Error{Code: 1, Msg: err.Error()}，
// 与领域层的 50000 并存，同一个"未归类内部错误"有两个码。P5 收敛到
// code.Internal(50000) 一个兜底码，且 msg 仍原样带出（不失诊断信息）。
//
// P4a 正文第十一/四十三刀把会话读面（列表、用量）都交给真域平面之后，这一格改挂**会话写面**
// （`createErr`）——它要验的是**控制器的错误出口**，与哪个端点无关。
func TestUnclassifiedErrorFallsBackToInternal(t *testing.T) {
	svc := &stubSvc{createErr: errors.New("boom")}
	r := newRestEngine(stubDeps(t, svc))
	env, _ := do(t, r, "POST", "/v1/mate/sessions", "{}")
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
	r := newRestEngine(stubDeps(t, svc))
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
	r := newRestEngine(stubDeps(t, &stubSvc{}))
	env, _ := do(t, r, "POST", "/v1/mate/sessions", `not-json{`)
	if env.Code != 40000 {
		t.Fatalf("非 JSON 期望 40000，got %+v", env)
	}
}

// TestCreateSessionServiceError svc 业务错误原样透出（40901 忙）。
func TestCreateSessionServiceError(t *testing.T) {
	svc := &stubSvc{createErr: gerror.Error{Code: 40901, Msg: "run in progress"}}
	r := newRestEngine(stubDeps(t, svc))
	env, _ := do(t, r, "POST", "/v1/mate/sessions", `{"prompt":"hi"}`)
	if env.Code != 40901 || !strings.Contains(env.Msg, "run in progress") {
		t.Fatalf("服务错误未透出: %+v", env)
	}
}

// TestUpdateSessionRoute PATCH /sessions/:id：会话**元数据写面**（P4a 正文第四十四刀起在用例层）。
//
// 这一面此前在路由层**一条用例都没有**（旧替身折一个 SessionResp 就完事）。现在替身只交三份依赖，
// 判据因此落在用例真的跑出来的东西上：nil 字段不更新、模型变更把**解析出的身份**写进会话记录、
// 以及"模型解析失败时**一个字节都不落库**"（落库那一格是替身记的，正是要看它有没有被调到）。
func TestUpdateSessionRoute(t *testing.T) {
	svc := &stubSvc{}
	deps := stubDeps(t, svc)
	deps.plane.RegisterSession(&session.SessionRec{ID: "s1", Title: "旧标题", RuntimeID: "rtx"})
	r := newRestEngine(deps)

	// ① 子集更新：只改出现过的键，回读投影与域里的记录一致。
	env, code := do(t, r, "PATCH", "/v1/mate/sessions/s1", `{"title":"新标题","pinned":true}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d env=%+v", code, env)
	}
	var resp dtosession.SessionResp
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	if resp.ID != "s1" || resp.Title != "新标题" || !resp.Pinned {
		t.Fatalf("回执不符: %+v", resp)
	}
	rec := deps.plane.Rec("s1")
	if rec == nil || rec.Title != "新标题" || !rec.Pinned || rec.Archived {
		t.Fatalf("域里的记录不符（`archived` 没传就不该动）: %+v", rec)
	}
	if svc.metaPatchedID != "s1" || svc.metaPatch.Title == nil || *svc.metaPatch.Title != "新标题" {
		t.Fatalf("元数据应落库一次: id=%q patch=%+v", svc.metaPatchedID, svc.metaPatch)
	}

	// ② 会话内改模型：**落进记录的是解析出的平台行身份**（不是请求原文，也不是运行时值），
	//    运行时值经投递面送进执行面。
	mdlID := objid.New(objid.Model)
	env, code = do(t, r, "PATCH", "/v1/mate/sessions/s1", `{"model":"`+mdlID+`"}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("改模型信封不符: http=%d env=%+v", code, env)
	}
	if rec := deps.plane.Rec("s1"); rec == nil || rec.ModelID != mdlID {
		t.Fatalf("会话记录里应是平台行身份: %+v", rec)
	}
	if svc.resolvedModelRuntime != "rtx" || svc.resolvedModelID != mdlID {
		t.Fatalf("解析应带上会话的 runtime 身份: runtime=%q id=%q", svc.resolvedModelRuntime, svc.resolvedModelID)
	}
	if svc.appliedModelSession != "s1" || svc.appliedModelCfg.Model != mdlID {
		t.Fatalf("投递面没收到解析出的运行时值: %+v", svc.appliedModelCfg)
	}

	// ③ 模型解析失败 → 上抛，且**落库那一格一次都不该被调到**（"一个字节都不落库"）。
	svc.metaPatchedID = ""
	svc.resolveModelErr = gerror.Error{Code: 40401, Msg: "模型行不存在"}
	env, _ = do(t, r, "PATCH", "/v1/mate/sessions/s1", `{"model":"`+mdlID+`"}`)
	if env.Code != 40401 || svc.metaPatchedID != "" {
		t.Fatalf("解析失败应上抛且不落库: env=%+v metaPatchedID=%q", env, svc.metaPatchedID)
	}

	// ④ 未知会话 → 40401（域的存在性判据）。
	if env, _ := do(t, r, "PATCH", "/v1/mate/sessions/nope", `{"title":"x"}`); env.Code != 40401 {
		t.Fatalf("未知会话应 40401: %+v", env)
	}
}

// TestPromptAndCancel prompt（校验必填）/cancel 调用透传。
func TestPromptAndCancel(t *testing.T) {
	svc := &stubSvc{}
	deps := stubDeps(t, svc)
	// 排队输入的**读模型**归域平面（P4a 正文第十一刀），所以夹具要有真会话与真入队。
	deps.plane.RegisterSession(&session.SessionRec{
		ID: "s1", RuntimeID: "rtx", Status: "completed", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	// 事件出口也要接上：撤下排队输入时那一条 `session.input_cancelled` 要能被观察
	// （域平面未注入总线时事件静默丢弃，只在纯列表/单测场景出现）。
	deps.plane.SetBus(deps.bus)
	queued, err := deps.plane.EnqueueInput(context.Background(), "s1", "排队一", "after_run", "t1", 3)
	if err != nil {
		t.Fatalf("入队夹具失败: %v", err)
	}
	r := newRestEngine(deps)

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
	// §6 列出待投递输入（冷挂载重建队列的当前态入口）：走**真域平面**，载荷形状按前端所需字段断言。
	//
	// 原来这里只断言"替身收到了 id"（`inputsListed`/`inputsListConv` 两个标志位）。那判据
	// 无法区分「id 真的传下去了」与「列表恒空」——换成真会话 + 真入队之后，回来的就是
	// 刚入队那一行（id 由平台铸造），顺带把未知会话必须 40401 这条也验进去。
	env, _ = do(t, r, "GET", "/v1/mate/sessions/s1/inputs", "")
	if env.Code != 0 {
		t.Fatalf("列出排队输入不符: env=%+v", env)
	}
	if env, _ = do(t, r, "GET", "/v1/mate/sessions/nope/inputs", ""); env.Code != 40401 {
		t.Fatalf("未知会话应 40401，got %+v", env)
	}
	env, _ = do(t, r, "GET", "/v1/mate/sessions/s1/inputs", "")
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
	if qdata[0].InputID != queued.ID || qdata[0].Text != "排队一" ||
		qdata[0].Delivery != "after_run" || qdata[0].TargetRunID != "t1" || qdata[0].Generation != 3 {
		t.Fatalf("排队列表字段不符: %+v（期望来自域入队的那一行 %s）", qdata[0], queued.ID)
	}

	// §6 撤下排队输入（P4a 正文第四十二刀起走用例层）：撤的是**刚入队那一行**，并且必须真的
	// 发出一条 `session.input_cancelled`——前端队列只认事件、不做本地推断，漏发它的表现是
	// 「REST 回 ok、界面那一行永远挂着」。替身时代这两件事都验不到（标志位只证"替身被调用了"）。
	env, _ = do(t, r, "DELETE", "/v1/mate/sessions/s1/inputs/"+queued.ID, "")
	if env.Code != 0 || !strings.Contains(string(env.Data), `"deleted":true`) {
		t.Fatalf("撤下排队输入不符: env=%+v", env)
	}
	cancelled := false
	for _, ev := range deps.bus.EventsAfter("s1", -1) {
		if ev.Type != session.BusInputCancelled {
			continue
		}
		if p, _ := ev.Payload.(map[string]any); p["input_id"] == queued.ID && p["reason"] == "user_cancel" {
			cancelled = true
		}
	}
	if !cancelled {
		t.Fatalf("撤下排队输入必须发 session.input_cancelled（input_id=%s）: %+v",
			queued.ID, deps.bus.EventsAfter("s1", -1))
	}
	// 幂等：再撤同一条 → 200 + deleted:false（不存在/已投递/已作废一律 false，不报错）。
	env, _ = do(t, r, "DELETE", "/v1/mate/sessions/s1/inputs/"+queued.ID, "")
	if env.Code != 0 || !strings.Contains(string(env.Data), `"deleted":false`) {
		t.Fatalf("重复撤下应幂等回 deleted:false: %+v", env)
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

// TestHistoryParamParsing 历史读面的**窗口语义**（端点 → 控制器参数 → 用例 → 会话域/总线）。
//
// P4a 正文第十刀把这面从 `manager` 上提到 `svcsession.History`，router 侧因此失去"替身转告参数"
// 这种证据——`svc.historyAfter` 那类标志位断言撤掉之后，**参数被忽略**与"参数生效但恰好没差别"
// 会变得无法区分（默认 after=-1 与显式 after=0 只差一条事件，而下限钳制、方向互斥都在这一面里）。
// 所以改成**真平面 + 真事件**：播 3 条（seq 0/1/2）再看**回来的窗口本身**。
func TestHistoryParamParsing(t *testing.T) {
	deps := stubDeps(t, &stubSvc{})
	deps.plane.RegisterSession(&session.SessionRec{ID: "s1"})
	for seq := int64(0); seq < 3; seq++ {
		deps.bus.PublishAt("s1", seq, fmt.Sprintf("evt_%d", seq), "session.message", 1, map[string]any{"n": seq})
	}
	r := newRestEngine(deps)

	// 默认：不给游标 = after 的哨兵 `-1`（早于一切事件），limit 默认 200 ⇒ 全部。
	page := histPageOf(t, r, "/v1/mate/sessions/s1/history")
	if got := page.seqs(); !equalSeqs(got, []int64{0, 1, 2}) || page.HasMore || page.FirstSeq != 0 || page.LastSeq != 2 {
		t.Fatalf("默认参数应回全部三条: %+v", page)
	}

	// 显式 after=0 与默认**不是**一回事：`0` 是合法位次，它跳过第 0 条。
	// （把两者混起来正是"翻更早"那条路径静默丢第一条的成因。）
	page = histPageOf(t, r, "/v1/mate/sessions/s1/history?after=0")
	if got := page.seqs(); !equalSeqs(got, []int64{1, 2}) {
		t.Fatalf("显式 after=0 应跳过第 0 条: %+v", page)
	}

	// before（向前翻页）取"该位次之前"。
	page = histPageOf(t, r, "/v1/mate/sessions/s1/history?before=2")
	if got := page.seqs(); !equalSeqs(got, []int64{0, 1}) || page.HasMore {
		t.Fatalf("before=2 应回 seq<2 的两条: %+v", page)
	}
	// before=0 显式给出 = "第 0 条之前" ⇒ 空页（"给了游标"与"没给"是两件事）。
	page = histPageOf(t, r, "/v1/mate/sessions/s1/history?before=0")
	if len(page.seqs()) != 0 {
		t.Fatalf("before=0 应是空页: %+v", page)
	}

	// limit：1 条 + 沿当前方向还有更多。
	page = histPageOf(t, r, "/v1/mate/sessions/s1/history?limit=1")
	if got := page.seqs(); !equalSeqs(got, []int64{0}) || !page.HasMore {
		t.Fatalf("limit=1 应回一条且 has_more: %+v", page)
	}
	// limit=0 钳到 1（0 条不是合法页大小：客户端会以为"没有历史"）。
	page = histPageOf(t, r, "/v1/mate/sessions/s1/history?limit=0")
	if got := page.seqs(); !equalSeqs(got, []int64{0}) {
		t.Fatalf("limit=0 应钳到 1: %+v", page)
	}
	// 上限 1000：造 1001 条，恰好回 1000 + has_more。
	depsBig := stubDeps(t, &stubSvc{})
	depsBig.plane.RegisterSession(&session.SessionRec{ID: "s1"})
	for seq := int64(0); seq < 1001; seq++ {
		depsBig.bus.PublishAt("s1", seq, fmt.Sprintf("evt_%d", seq), "session.message", 1, nil)
	}
	big := histPageOf(t, newRestEngine(depsBig), "/v1/mate/sessions/s1/history?limit=999999")
	if len(big.seqs()) != 1000 || !big.HasMore || big.LastSeq != 999 {
		t.Fatalf("limit 应钳到 1000: len=%d has_more=%v last=%d", len(big.seqs()), big.HasMore, big.LastSeq)
	}

	// 会话不存在 ⇒ 40401（这一条以前由替身自己判，现在由用例判）。
	if env, _ := do(t, r, "GET", "/v1/mate/sessions/nope/history", ""); env.Code != 40401 {
		t.Fatalf("未知会话期望 40401，got %+v", env)
	}

	// 方向互斥 ⇒ 40001（控制器拦，两个游标同时给没有合理解释）。
	if env, _ := do(t, r, "GET", "/v1/mate/sessions/s1/history?after=1&before=2", ""); env.Code != 40001 {
		t.Fatalf("after+before 期望 40001，got %+v", env)
	}
	// 非法游标 ⇒ 40000（负数与非数字都不接受；`-1` 是服务端内部哨兵，不是对外值）。
	for _, q := range []string{"before=-3", "after=-1", "after=abc", "limit=x"} {
		if env, _ := do(t, r, "GET", "/v1/mate/sessions/s1/history?"+q, ""); env.Code != 40000 {
			t.Fatalf("%s 期望 40000，got %+v", q, env)
		}
	}
}

// histPage 历史信封的判据视图（窗口本身，而不是"替身收到了什么"）。
type histPage struct {
	Events []struct {
		Seq  int64  `json:"seq"`
		Type string `json:"type"`
	} `json:"events"`
	HasMore  bool  `json:"has_more"`
	FirstSeq int64 `json:"first_seq"`
	LastSeq  int64 `json:"last_seq"`
}

func (p histPage) seqs() []int64 {
	out := make([]int64, 0, len(p.Events))
	for _, e := range p.Events {
		out = append(out, e.Seq)
	}
	return out
}

func equalSeqs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func histPageOf(t *testing.T, r *gin.Engine, url string) histPage {
	t.Helper()
	env, _ := do(t, r, "GET", url, "")
	if env.Code != 0 {
		t.Fatalf("%s 期望成功，got %+v", url, env)
	}
	var page histPage
	if err := json.Unmarshal(env.Data, &page); err != nil {
		t.Fatalf("%s data 解析失败: %s", url, env.Data)
	}
	return page
}

// TestPermissionMemoriesList GET /sessions/:id/permissions/memories 返回会话级权限记忆（含 decision 侧）。
//
// P4a 正文第四十刀：这一面归 `svcsession.DetailSvc`（用例层）之后，路由测试的替身只提供**域值对象**
// （`interaction.MemoryItem`），`since` 的 RFC3339 折叠与"空集回 []"都由用例层做——判据因此从
// "替身收到了 id"变成"投影真的发生了"。同时补上"未知会话 → 40401"：替身时代那条校验根本不存在，
// 而它是这一面的主要职责（存在性判据）。
func TestPermissionMemoriesList(t *testing.T) {
	svc := &stubSvc{memList: []interaction.MemoryItem{
		interaction.NewMemoryItem("s1", "运行演示命令 echo hello", "execute", "allow", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		interaction.NewMemoryItem("s1", "删除文件", "execute", "reject", time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC)),
	}}
	deps := stubDeps(t, svc)
	deps.plane.RegisterSession(&session.SessionRec{ID: "s1"})
	r := newRestEngine(deps)
	env, _ := do(t, r, "GET", "/v1/mate/sessions/s1/permissions/memories", "")
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var out []dtosession.MemoryResp
	if err := json.Unmarshal(env.Data, &out); err != nil || len(out) != 2 ||
		out[0].Title != "运行演示命令 echo hello" || out[0].Kind != "execute" || out[0].Decision != "allow" ||
		out[1].Decision != "reject" || out[0].Since != "2026-01-02T03:04:05Z" {
		t.Fatalf("memories data 不符: %s", env.Data)
	}
	// 未知会话 → 40401（用例层的校验，不是控制器的）。
	if env, _ := do(t, r, "GET", "/v1/mate/sessions/nope/permissions/memories", ""); env.Code != 40401 {
		t.Fatalf("未知会话应 40401: %+v", env)
	}
	// 空记忆回 `[]` 而不是 null（前端按数组渲染）。
	svc.memList = nil
	env, _ = do(t, r, "GET", "/v1/mate/sessions/s1/permissions/memories", "")
	if strings.TrimSpace(string(env.Data)) != "[]" {
		t.Fatalf("空记忆应回 []: %s", env.Data)
	}
}

// TestPendingPermissionsList GET /sessions/:id/permissions 返回挂起权限请求（工具调用与选项折叠透传）。
func TestPendingPermissionsList(t *testing.T) {
	svc := &stubSvc{pendingPerms: []interaction.PermissionItem{{
		CallID: "tc1", SessionID: "s1", Title: "执行 echo hello",
		ToolCall:  acp.ToolCallInfo{ID: "tc1", Kind: "execute", Input: json.RawMessage(`{"cmd":"echo hello"}`)},
		Options:   []acp.PermissionOption{{OptionID: "allow_once", Name: "本次允许", Kind: "allow_once"}},
		TimeoutAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}}
	deps := stubDeps(t, svc)
	deps.plane.RegisterSession(&session.SessionRec{ID: "s1"})
	r := newRestEngine(deps)
	env, _ := do(t, r, "GET", "/v1/mate/sessions/s1/permissions", "")
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var out []dtosession.PermissionResp
	if err := json.Unmarshal(env.Data, &out); err != nil || len(out) != 1 ||
		out[0].CallID != "tc1" || out[0].Kind != "execute" ||
		len(out[0].Options) != 1 || out[0].Options[0].OptionID != "allow_once" ||
		out[0].TimeoutAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("permissions data 不符: %s", env.Data)
	}
	if env, _ := do(t, r, "GET", "/v1/mate/sessions/nope/permissions", ""); env.Code != 40401 {
		t.Fatalf("未知会话应 40401: %+v", env)
	}
}

// TestRevokeMemory 单条撤销（title+kind）与一键撤销（均空）都正确透传。
func TestRevokeMemory(t *testing.T) {
	svc := &stubSvc{}
	deps := stubDeps(t, svc)
	deps.plane.RegisterSession(&session.SessionRec{ID: "s1"})
	r := newRestEngine(deps)

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

// TestElicitationList GET /sessions/:id/elicitations 返回挂起结构化提问（表单 schema / URL 二选一）。
func TestElicitationList(t *testing.T) {
	svc := &stubSvc{elicitations: []interaction.ElicitationItem{{
		ID: "el-abc", SessionID: "s1", ScopeSource: "wire", Mode: "form",
		Message: "请选择一个环境", TimeoutAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Schema: map[string]any{"type": "object", "properties": map[string]any{"env": map[string]any{"type": "string", "enum": []any{"dev", "prod"}}}},
	}}}
	deps := stubDeps(t, svc)
	deps.plane.RegisterSession(&session.SessionRec{ID: "s1"})
	r := newRestEngine(deps)
	env, _ := do(t, r, "GET", "/v1/mate/sessions/s1/elicitations", "")
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	var out []dtosession.ElicitationResp
	if err := json.Unmarshal(env.Data, &out); err != nil || len(out) != 1 || out[0].ElicitationID != "el-abc" ||
		out[0].Mode != "form" || out[0].ScopeSource != "wire" || out[0].Schema == nil || out[0].URL != "" {
		t.Fatalf("elicitations data 不符: %s", env.Data)
	}
	// url 模式：回 url、schema 为空（两者互斥，不许两个都回）。
	svc.elicitations = []interaction.ElicitationItem{{
		ID: "el-url", SessionID: "s1", Mode: "url", Message: "去这里确认",
		URL: "https://example.com/confirm", TimeoutAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}
	env, _ = do(t, r, "GET", "/v1/mate/sessions/s1/elicitations", "")
	// **必须用新变量**：`json.Unmarshal` 复用切片元素时**不清字段**——第一次解码留下的 `schema`
	// 会原样挂在这一条上，于是"url 模式不该回 schema"这条判据会假红（实测踩到过一次）。
	var outURL []dtosession.ElicitationResp
	if err := json.Unmarshal(env.Data, &outURL); err != nil || len(outURL) != 1 || outURL[0].URL == "" || outURL[0].Schema != nil {
		t.Fatalf("url 模式应回 url 且 schema 为空: %s", env.Data)
	}
	if env, _ := do(t, r, "GET", "/v1/mate/sessions/nope/elicitations", ""); env.Code != 40401 {
		t.Fatalf("未知会话应 40401: %+v", env)
	}
}

// TestResolveElicitation POST /sessions/:id/elicitation 决策透传（accept + content）。
func TestResolveElicitation(t *testing.T) {
	svc := &stubSvc{}
	deps := stubDeps(t, svc)
	deps.plane.RegisterSession(&session.SessionRec{ID: "s1"})
	r := newRestEngine(deps)
	env, _ := do(t, r, "POST", "/v1/mate/sessions/s1/elicitation",
		`{"elicitation_id":"el-abc","action":"accept","content":{"env":"prod"}}`)
	if env.Code != 0 {
		t.Fatalf("信封不符: %+v", env)
	}
	if svc.resolvedElicID != "el-abc" || svc.resolvedElicAction != "accept" || svc.resolvedElicContent["env"] != "prod" {
		t.Fatalf("决策透传不符: %+v", svc)
	}
	// 未知会话 → 40401（用例层的存在性判据，替身时代不存在）。
	if env, _ := do(t, r, "POST", "/v1/mate/sessions/nope/elicitation",
		`{"elicitation_id":"el-abc","action":"accept"}`); env.Code != 40401 {
		t.Fatalf("未知会话应 40401: %+v", env)
	}
}

// TestResolveElicitationBadBody 缺 elicitation_id/action → code 40000。
func TestResolveElicitationBadBody(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(t, svc))
	env, _ := do(t, r, "POST", "/v1/mate/sessions/s1/elicitation", `{"action":"accept"}`)
	if env.Code != 40000 {
		t.Fatalf("缺 elicitation_id 应 40000: %+v", env)
	}
	env, _ = do(t, r, "POST", "/v1/mate/sessions/s1/elicitation", `{"elicitation_id":"el-abc"}`)
	if env.Code != 40000 {
		t.Fatalf("缺 action 应 40000: %+v", env)
	}
}

// TestProviderRoutes provider 档案路由：POST 建 → GET 列出 → PATCH 只更新（不存在 → 40401）。
//
// 这条用例在 P4a 正文第四刀之后走的是**真域平面**（`ctrmodel` 从 `Ports.ProviderPlane`
// 自己造用例），所以它顺带钉住两条真行为：新建不带密钥也能建（自检会把它报成可执行的 fail）、
// 以及 PATCH 对不存在的 id **不 upsert**（v12 起 `provider_not_found`）——后者以前被替身
// 无条件 200 盖住了。
func TestProviderRoutes(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(t, svc))

	env, code := do(t, r, "POST", "/v1/mate/providers", `{"id":"default","protocol":"openai","base_url":"https://api.example.com"}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("POST provider 失败 code=%d env=%d msg=%s", code, env.Code, env.Msg)
	}
	env, code = do(t, r, "GET", "/v1/mate/providers", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET providers 失败 code=%d env=%d", code, env.Code)
	}
	var items []dtomodel.ProviderResp
	if err := json.Unmarshal(env.Data, &items); err != nil || len(items) != 1 || items[0].ID != "default" {
		t.Fatalf("GET providers data 不符: %s", env.Data)
	}
	env, code = do(t, r, "PATCH", "/v1/mate/providers/default", `{"note":"改了备注"}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("PATCH provider 失败 code=%d env=%d msg=%s", code, env.Code, env.Msg)
	}
	// 不存在 → `provider_not_found`(40401)（不是 upsert）。
	env, code = do(t, r, "PATCH", "/v1/mate/providers/nope", `{"note":"x"}`)
	if code != 200 || env.Code != 40401 {
		t.Fatalf("PATCH 不存在的 provider 应 40401，code=%d env=%d", code, env.Code)
	}
}

// testProviderPlane 造一个**真**供应商域平面（内存态 + 临时目录载体）。
//
// 注册表与凭据机制都必须与生产同源（`provider.NewPlane` 的注释：两份 = 两个事实源），
// 而这里刻意不给密钥：带密钥的写面会落托管文档，那是用例层测试的事，不是路由测试的事。
func testProviderPlane(t *testing.T) *provider.Plane {
	t.Helper()
	reg := dsh.Registry()
	plane := provider.NewPlane(reg, registry.NewProviderSecrets(credential.New(reg)))
	plane.SetProviderDir(t.TempDir())
	return plane
}

// TestCapabilityRoutes P1 能力数据面路由：GET /skills /mcps /tools 列出目录（信封 data 数组）。
func TestCapabilityRoutes(t *testing.T) {
	svc := &stubSvc{}
	deps := stubDeps(t, svc)
	// MCP 注册表读模型也归用例层（P4a 正文第十四刀）：夹具同样是"真注册表里注册过的 server"，
	// 探测态/工具计数/悬空引用体检三个源都来自真对象（条目本体之外的都是空态）。
	if err := deps.caps.RegisterMcpServer(&capability.McpServer{
		ID: "fs", Transport: "stdio", Command: "/usr/bin/npx",
	}); err != nil {
		t.Fatalf("注册 mcp 夹具失败: %v", err)
	}
	// 技能目录的**读模型**由域平面直接提供（P4a 正文第十三刀）：夹具是"真注册表里注册过的
	// 技能"，投影（warnings / scope / enabled）由 `skill.Plane` 做——用例层不再包一层转发。
	if err := deps.caps.RegisterSkill(&capability.Skill{
		ID: "plan", Name: "plan", Description: "先规划再动手", Body: "# 先规划",
	}); err != nil {
		t.Fatalf("注册技能夹具失败: %v", err)
	}
	// 工具目录的**读模型**归用例层（P4a 正文第十二刀）：夹具是"真注册表里注册过的工具"，
	// 而不是替身返回值——这样断言的才是"投影真的把注册表里那条读出来了"。
	deps.caps.RegisterTool(&capability.Tool{
		Name: "web_search", Source: "builtin", Description: "联网搜索", McpServer: "fs",
	})
	r := newRestEngine(deps)

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
	if tools[0].McpServer != "fs" || tools[0].Source != "builtin" {
		t.Fatalf("工具来源/归属未投影: %+v", tools[0])
	}
}

// TestSkillToggleRoute PATCH /skills/:id（§13.4 S4）：入参 {enabled} 透传到服务端口，
// 响应回带写回路径与物化计数；非法 JSON → 40000（控制器绑定错误带码）。
// TestSkillToggleRoute 启停 skill 这一格自 P4a 正文第三十六刀起走**真域平面**（`h.skills`，
// 投影 `dtocapability.SkillToggleResp` 与「未知 skill / 不可写」的错误码都在域里铸）。替身能证的
// 只有"控制器把 id 与 enabled 原样转发"，而那是**控制器唯一还做的事**；替身证不了域真的这么判。
//
// 所以这里改成证"请求确实打到了域平面"：本用例的引擎里没有那份 skill 定义，域平面的自然答复是
// `skill_not_found`（40401）——这正是真平面与替身的分界（替身会回 200 并自报成功）。
// 启停两个方向 + 物化成功路径的**本体**由 `internal/manager/skills_test.go` 用真平面 + 真目录钉。
func TestSkillToggleRoute(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(t, svc))

	env, code := do(t, r, "PATCH", "/v1/mate/skills/plan", `{"enabled":false}`)
	if code != 200 || env.Code != 40401 {
		t.Fatalf("未知 skill 应由域平面回 40401（说明请求真的进了域，而不是被替身截住）: code=%d env=%+v", code, env)
	}

	// 坏入参：控制器绑定失败 → 40000（这一格与域无关，仍是控制器自己的判定）。
	env, code = do(t, r, "PATCH", "/v1/mate/skills/plan", `{"enabled":`)
	if code != 200 || env.Code != 40000 {
		t.Fatalf("坏入参应 40000，code=%d env=%d", code, env.Code)
	}
}

// TestSkillMaterializationRoute 同上一格：物化现状的投影在域平面里（含三段状态与摘要比较），
// 这里只证"打到域平面"的自然答复；`in_sync` 等状态判定由 manager 侧的用例用真平面钉。
func TestSkillMaterializationRoute(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(t, svc))

	env, code := do(t, r, "GET", "/v1/mate/skills/plan/materialization", "")
	if code != 200 || env.Code != 40401 {
		t.Fatalf("未知 skill 应由域平面回 40401: code=%d env=%+v", code, env)
	}
}

// TestProbeMcpServerRoute P2 §5.2 MCP live 探测路由：POST /mcps/:id/probe 的错误经信封透出。
//
// P4a 正文第三十八刀：探测面归域平面（`h.mcpPlane.Probe`），替身的
// `probeResp`/`probeErr`/`probeID` 三个注入点随之退场——按"替身退场"的既定口径，这一层只证
// **真域平面能自然产生的那个码原样透出**：
//
//	· 未知 server → 40401（域里铸的 `mcp_server_not_found`）
//	· 已注册但连不上 → 50201（域真的去连了，失败码同样是域铸的）
//
// "探测成功 → Tool 目录整体替换 / 状态写回"那条路要一台真 MCP server（httptest），
// 在 `internal/manager` 的 `TestProbeMcpServer` 里覆盖；替身时代这里的"成功"是测试自己编的响应，
// 它能证明的只有"控制器把 X 原样转发"，证明不了"X 本身对"。
func TestProbeMcpServerRoute(t *testing.T) {
	svc := &stubSvc{}
	deps := stubDeps(t, svc)
	r := newRestEngine(deps)

	if env, code := do(t, r, "POST", "/v1/mate/mcps/nope/probe", ""); code != 200 || env.Code != 40401 {
		t.Fatalf("未知 server 应由域平面回 40401，got http=%d code=%d env=%+v", code, env.Code, env)
	}

	// 注册一个连不上的 http server：域会真的发请求，连接失败码（50201）原样透出。
	if err := deps.caps.RegisterMcpServer(&capability.McpServer{
		ID: "down", Name: "down", Transport: capability.TransportHTTP, URL: "http://127.0.0.1:1/mcp",
	}); err != nil {
		t.Fatal(err)
	}
	if env, code := do(t, r, "POST", "/v1/mate/mcps/down/probe", ""); code != 200 || env.Code != 50201 {
		t.Fatalf("连不上应 50201，got http=%d code=%d env=%+v", code, env.Code, env)
	}
}

// TestErrorEnvelopeMapping 端点错误必须经信封映射（修复：此前裸 acp 错误漏成 code=-1，
// 与 README「非 0 为业务错误（40001/40401/…）」契约不符）。
//
// v19：这些用例原挂在 agent 端点上（已随 agent 面删除）；映射表本身与端点无关，
// 只要求"某个端点把一支 acp 错误原样交给信封映射"。
//
// 承载端点换过三次（供应商面 → 会话列表 → 用量），每次都是因为那一面**归了真域平面/真用例**、
// 替身注入不进去了；P4a 正文第四十三刀用量面也归域平面后，改挂**会话写面**（`createErr`）。
// 映射表与端点无关，这条链只要还有一端替身就仍然成立。
func TestErrorEnvelopeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
	}{
		{"not_found", acp.NewError(acp.ClassRequest, "provider_not_found", "未知供应商", nil), 40401},
		{"request", acp.NewError(acp.ClassRequest, "invalid_provider_id", "id 非法", nil), 40001},
		{"conflict", acp.NewError(acp.ClassLimit, "provider_exists", "已存在", nil), 40901},
		{"unsupported", acp.NewError(acp.ClassUnsupported, "credentials_unsupported", "这套底座不支持托管凭据", nil), 40501},
		{"upstream", acp.NewError(acp.ClassService, "provider_write_failed", "载体提交失败", nil), 50201},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubSvc{createErr: tc.err}
			r := newRestEngine(stubDeps(t, svc))
			env, http := do(t, r, "POST", "/v1/mate/sessions", "{}")
			if http != 200 || env.Code != tc.code {
				t.Fatalf("期望 HTTP 200 code=%d，got http=%d code=%d msg=%q", tc.code, http, env.Code, env.Msg)
			}
			if env.Msg == "" || strings.HasPrefix(env.Msg, "acp error") {
				t.Fatalf("msg 应为可读详情，got %q", env.Msg)
			}
		})
	}
	// 已映射错误不得被二次映射（FromError 幂等）。
	svc := &stubSvc{createErr: gerror.Error{Code: 40001, Msg: "已映射"}}
	r := newRestEngine(stubDeps(t, svc))
	env, _ := do(t, r, "POST", "/v1/mate/sessions", "{}")
	if env.Code != 40001 || env.Msg != "已映射" {
		t.Fatalf("已映射错误应透传: %+v", env)
	}
}

// TestMessageSearchRoute GET /search/messages：参数校验（q 必填、limit 正整数）与**窗口语义**
// （读模型自 P4a 正文第四十三刀起直接由会话域平面提供）。
//
// 替身（`searchMsgResp`/`searchMsgQuery`/`searchMsgLimit`）撤掉之后，判据从「替身记下了什么」换成
// **回来的窗口本身**：memory 模式回扫 transcript（`source=transcript`），夹具因此是
// 「真会话 + 真 transcript 文件」。这样「limit 被忽略了」与「limit 生效但恰好没差别」才是可分的
// ——替身时代这两件事无法区分（它只证"替身收到了参数"）。
func TestMessageSearchRoute(t *testing.T) {
	dir := t.TempDir()
	// 平台会话 id（`ses_<26 位 ULID>`）：transcript 装载面**只认这个形状**的文件名，
	// 用 `c-1` 那种历史形态会被当成旧文件跳过（装载面的判据），而检索面按 `<dir>/<会话id>.jsonl`
	// 打开文件——文件名对不上就永远扫不到。
	sesID := objid.New(objid.Session)
	line := func(seq int64, typ, text string) string {
		return fmt.Sprintf(`{"id":%q,"seq":%d,"ts":"2026-01-02T03:04:05Z","type":%q,"payload":{"text":%q}}`,
			objid.New(objid.Event), seq, typ, text)
	}
	writeTranscript(t, dir, sesID,
		line(7, session.BusUserMessage, "网关 502"),
		line(8, session.BusMessage, "502 已恢复 upstream"),
		line(9, session.BusUserMessage, "谢谢"),
	)
	deps := stubDeps(t, &stubSvc{})
	// 顺序：先启用回扫（装载面会顺手把这条会话登记进来），再登记一次把标题/运行时补上
	// （检索返回的 `title`/`runtime_id` 取自会话记录，不取自文件）。
	deps.plane.SetTranscriptDir(dir)
	deps.plane.RegisterSession(&session.SessionRec{ID: sesID, Title: "部署", RuntimeID: "rtx"})
	r := newRestEngine(deps)

	env, http := do(t, r, "GET", "/v1/mate/search/messages?q=502", "")
	if http != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d %+v", http, env)
	}
	var resp dtosearch.MessageSearchResp
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatalf("data 解析: %v", err)
	}
	// 两条命中（「谢谢」不含关键词），按 seq 倒序；角色由事件类型分。
	if resp.Source != "transcript" || resp.Total != 2 || len(resp.Hits) != 2 {
		t.Fatalf("命中不符: %+v", resp)
	}
	if resp.Hits[0].Seq != 8 || resp.Hits[0].Role != "assistant" ||
		resp.Hits[1].Seq != 7 || resp.Hits[1].Role != "user" {
		t.Fatalf("命中的锚点/角色不符: %+v", resp.Hits)
	}
	if resp.Hits[1].SessionID != sesID || resp.Hits[1].Title != "部署" || resp.Hits[1].RuntimeID != "rtx" {
		t.Fatalf("命中的会话归属不符: %+v", resp.Hits[1])
	}
	if !strings.Contains(resp.Hits[1].Snippet, "网关 502") {
		t.Fatalf("摘要不符: %+v", resp.Hits[1])
	}

	// limit=1 → 只回最新一条 + `has_more`：这就是「limit 真的到了域」的证据。
	env, _ = do(t, r, "GET", "/v1/mate/search/messages?q=502&limit=1", "")
	var one dtosearch.MessageSearchResp
	if err := json.Unmarshal(env.Data, &one); err != nil {
		t.Fatalf("limit 页解析: %v", err)
	}
	if len(one.Hits) != 1 || !one.HasMore || one.Hits[0].Seq != 8 {
		t.Fatalf("limit=1 应回最新一条 + has_more: %+v", one)
	}

	// 缺 q → 40001；非法 limit → 40001（两条都是**控制器**判的，与域无关）。
	env, http = do(t, r, "GET", "/v1/mate/search/messages", "")
	if http != 200 || env.Code != 40001 || !strings.Contains(env.Msg, "invalid_search_query") {
		t.Fatalf("缺 q 校验不符: http=%d %+v", http, env)
	}
	env, http = do(t, r, "GET", "/v1/mate/search/messages?q=a&limit=abc", "")
	if http != 200 || env.Code != 40001 || !strings.Contains(env.Msg, "invalid_search_limit") {
		t.Fatalf("limit 校验不符: http=%d %+v", http, env)
	}
}

// writeTranscript 按 wire transcript 的落盘格式造一个会话的 jsonl 夹具。
//
// 首行是 header（`schema` + `session_id`），其后每行一条事件原语——域读的就是这份文件
// （`readTranscriptEvents` 认不出 header 就整份跳过，所以这个夹具同时钉住了「header 必须在前」）。
func writeTranscript(t *testing.T, dir, sessionID string, lines ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"` + sessionID + `"}`)
	b.WriteString("\n")
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("写 transcript 夹具: %v", err)
	}
}

// TestExportRoute POST /sessions/export：**真**导出用例（P4a：用例实例由 ctrexport 造）+
// 请求体绑定 + 错误映射。
//
// 这条用例以前把 `ExportSessions` 整个假掉了，于是它验的只是"信封能包住一个假响应"。现在替身
// 只交事实，判据（格式归一、ids 去重、未知会话进 skipped、错误码）都是用例层真的跑出来的。
func TestExportRoute(t *testing.T) {
	// 事实这一层也是**真的**（P4a 正文第八刀）：sqlite 载体 + 真会话行/事件行，替身已撤掉
	// ——否则"导出读的是哪份事实"又会被替身盖住。
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.UpsertSession(ctx, sessionrow.Item{
		ID: "c-1", Cwd: t.TempDir(), Title: "部署", Status: "idle",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	if _, err := st.AppendEvent(ctx, objid.New(objid.Event), "c-1", "session.run.started",
		map[string]any{"run_id": "run_1"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	deps := stubDeps(t, &stubSvc{})
	deps.store = st
	r := newRestEngine(deps)

	env, http := do(t, r, "POST", "/v1/mate/sessions/export", `{"ids":["c-1"],"format":"markdown"}`)
	if http != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d %+v", http, env)
	}
	data := string(env.Data)
	if !strings.Contains(data, `"session_id":"c-1"`) || !strings.Contains(data, `"exported":1`) {
		t.Fatalf("响应载荷不符（真 Exporter 应产出一份文件）: %s", data)
	}
	if !strings.Contains(data, ".md") {
		t.Fatalf("文件名扩展名应随 format 走: %s", data)
	}

	// 未知会话进 `skipped`（用例层的口径，不再由替身说了算）。
	env, http = do(t, r, "POST", "/v1/mate/sessions/export", `{"ids":["c-1","nope"],"format":"text"}`)
	if http != 200 || env.Code != 0 || !strings.Contains(string(env.Data), `"skipped":["nope"]`) {
		t.Fatalf("未知会话应进 skipped: http=%d %+v", http, env)
	}

	// 缺 ids → 40000（参数绑定失败）。
	env, http = do(t, r, "POST", "/v1/mate/sessions/export", `{"format":"text"}`)
	if http != 200 || env.Code != 40000 {
		t.Fatalf("缺 ids 校验不符: http=%d %+v", http, env)
	}
	// 非法格式 → 40001（用例层的真判据，替身注入已经不可能）。
	env, http = do(t, r, "POST", "/v1/mate/sessions/export", `{"ids":["c-1"],"format":"pdf"}`)
	if http != 200 || env.Code != 40001 || !strings.Contains(env.Msg, "invalid_export_format") {
		t.Fatalf("错误映射不符: http=%d %+v", http, env)
	}
}

// TestRuntimeDiagnosticsRoute GET /runtime/diagnostics：真执行面（P4a 正文第七刀）。
//
// 替身撤掉之后这条用例验的是**装配与读面**：`Ports.Runtime` 交的是执行面本身，用例在
// `ctrruntime` 里造，诊断读的是此刻的进程态（没有活连接 → `stopped` + `stderr_source: none`）。
// 形状里的"stderr 尾巴 + 最近失败"两段由执行面自己的用例覆盖（`internal/drivers/runtimeconn`）。
func TestRuntimeDiagnosticsRoute(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(t, svc))

	env, http := do(t, r, "GET", "/v1/mate/runtime/diagnostics", "")
	if http != 200 || env.Code != 0 {
		t.Fatalf("信封不符: http=%d %+v", http, env)
	}
	body := string(env.Data)
	for _, want := range []string{
		`"runtime":"` + goruntime.SelectedRuntime() + `"`,
		`"state":"stopped"`, `"connected":false`, `"stderr_source":"none"`, `"stderr_tail":[]`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("响应缺 %s: %s", want, body)
		}
	}
	// 身份面不许回带 agent 相关的选择面字段（被撤掉的概念不许从别的字段缝回来）——
	// 判据落在响应形状上（`rest_removed_agent_key_test.go` 有逐字的那一条）。
	if strings.Contains(body, `"runtimes"`) {
		t.Fatalf("诊断面不该出现 runtimes: %s", body)
	}
}

// fakeMcpServer 一台最小的 MCP JSON-RPC 假服务器（http 传输），**答复里回显收到的参数**。
//
// 为什么路由层也要一台真服务器（P4a 正文第三十九刀）：MCP live 五条归域平面之后，替身的
// `callMcpResp`/`callMcpErr`/`mcpLiveID`/`mcpLiveErr` 全部退场——路由这一层要证的不再是
// "控制器把参数原样转发给替身"（那种判据在真实现把参数丢掉时**照样绿**），而是"参数真的走进了域、
// 并被真的发给了 server"。回显让 id/tool/uri/name/arguments 与"空 body = 零参数"都成了
// **能从响应里读出来**的事实。
//
// 与 `internal/manager` 那台假服务器同一手法而**刻意不复用**：那是包内夹具（`package manager`），
// 两个包的用例各自持有自己的夹具，比把夹具提到一个"谁都能改"的公共位置更不容易漂。
func fakeMcpServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
				URI       string          `json:"uri"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		text := func(s string) []map[string]any {
			return []map[string]any{{"type": "text", "text": s}}
		}
		var result map[string]any
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18",
				"serverInfo": map[string]any{"name": "router-fake", "version": "1.0"}}
		case "tools/call":
			if msg.Params.Name == "boom" {
				result = map[string]any{"content": text("工具内部失败"), "isError": true}
			} else {
				result = map[string]any{"content": text("called " + msg.Params.Name + " args " + string(msg.Params.Arguments))}
			}
		case "resources/list":
			result = map[string]any{"resources": []map[string]any{{"uri": "file:///tmp/a.txt", "mimeType": "text/plain"}}}
		case "resources/templates/list":
			result = map[string]any{"resourceTemplates": []map[string]any{}}
		case "resources/read":
			result = map[string]any{"contents": []map[string]any{
				{"uri": msg.Params.URI, "mimeType": "text/plain", "text": "read " + msg.Params.URI}}}
		case "prompts/list":
			result = map[string]any{"prompts": []map[string]any{{"name": "greet"}}}
		case "prompts/get":
			result = map[string]any{"messages": []map[string]any{{"role": "user",
				"content": map[string]any{"type": "text", "text": "greet " + msg.Params.Name + " args " + string(msg.Params.Arguments)}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// registerFakeMcp 在**装配点交下去的那份能力注册表**上注册一个指向假服务器的 http server
// （plane 与读模型用的是同一张表，见 stubDeps）。
func registerFakeMcp(t *testing.T, deps restFixture, id, url string) {
	t.Helper()
	if err := deps.caps.RegisterMcpServer(&capability.McpServer{
		ID: id, Name: id, Transport: capability.TransportHTTP, URL: url,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestCallMcpToolRoute B1 MCP 工具调用路由：server/tool/arguments/timeout_ms **真的到了 server**；
// 工具级失败（is_error=true）仍是 code=0；非法 body 40000；未知 server 40401；连不上 50201。
func TestCallMcpToolRoute(t *testing.T) {
	svc := &stubSvc{}
	deps := stubDeps(t, svc)
	registerFakeMcp(t, deps, "fs", fakeMcpServer(t))
	r := newRestEngine(deps)

	env, code := do(t, r, "POST", "/v1/mate/mcps/fs/tools/read_file/call",
		`{"arguments":{"path":"/tmp/a"},"timeout_ms":1500}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("POST call 失败 http=%d code=%d body=%s", code, env.Code, env.Data)
	}
	var got dtocapability.McpToolCallResp
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("data 反序列化失败: %v", err)
	}
	if got.ServerID != "fs" || got.Tool != "read_file" {
		t.Fatalf("回执身份不符: %+v", got)
	}
	if !strings.Contains(got.Text, "called read_file") || !strings.Contains(got.Text, `{"path":"/tmp/a"}`) {
		t.Fatalf("tool/arguments 未透传到 server: %q", got.Text)
	}

	// 空 body = 无参数调用（不该报参数错误，也不该带上任何 arguments）。
	env, code = do(t, r, "POST", "/v1/mate/mcps/fs/tools/plain/call", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("空 body 应放行，got http=%d code=%d", code, env.Code)
	}
	var bare dtocapability.McpToolCallResp
	_ = json.Unmarshal(env.Data, &bare)
	if bare.Tool != "plain" || !strings.Contains(bare.Text, "called plain args ") {
		t.Fatalf("空 body 应零参数调用: %q", bare.Text)
	}

	// 非法 JSON body → 40000（控制器判，不走平面）。
	if env, code := do(t, r, "POST", "/v1/mate/mcps/fs/tools/t/call", "{oops"); code != 200 || env.Code != 40000 {
		t.Fatalf("非法 body 应 40000，got http=%d code=%d", code, env.Code)
	}

	// 工具级失败：仍 code=0，is_error 表达（服务端把自己当成"答复"而不是"平台错误"）。
	env, _ = do(t, r, "POST", "/v1/mate/mcps/fs/tools/boom/call", "")
	var failed dtocapability.McpToolCallResp
	_ = json.Unmarshal(env.Data, &failed)
	if env.Code != 0 || !failed.IsError {
		t.Fatalf("工具级失败应 code=0 + is_error=true，got code=%d %+v", env.Code, failed)
	}

	// 未知 server → 40401（域里铸的码经信封透出）。
	if env, code := do(t, r, "POST", "/v1/mate/mcps/nope/tools/t/call", ""); code != 200 || env.Code != 40401 {
		t.Fatalf("未知 server 应 40401，got http=%d code=%d", code, env.Code)
	}
	// 已注册但连不上 → 50201（域真的发了请求）。
	registerFakeMcp(t, deps, "down", "http://127.0.0.1:1/mcp")
	if env, code := do(t, r, "POST", "/v1/mate/mcps/down/tools/t/call", ""); code != 200 || env.Code != 50201 {
		t.Fatalf("连不上应 50201，got http=%d code=%d body=%s", code, env.Code, env.Data)
	}
}

// TestMcpResourcesPromptsRoutes B2：资源/提示词四条路由——uri/name/arguments 真的到了 server
// （响应回显），非法 body 40000、缺 uri 由**域**回 40001、未知 server 40401。
func TestMcpResourcesPromptsRoutes(t *testing.T) {
	svc := &stubSvc{}
	deps := stubDeps(t, svc)
	registerFakeMcp(t, deps, "fs", fakeMcpServer(t))
	r := newRestEngine(deps)

	env, code := do(t, r, "GET", "/v1/mate/mcps/fs/resources", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET resources 失败 http=%d code=%d body=%s", code, env.Code, env.Data)
	}
	var res dtocapability.McpResourcesResp
	_ = json.Unmarshal(env.Data, &res)
	if res.ServerID != "fs" || len(res.Resources) != 1 || res.Resources[0].URI != "file:///tmp/a.txt" {
		t.Fatalf("资源结果不符: %+v", res)
	}

	env, code = do(t, r, "POST", "/v1/mate/mcps/fs/resources/read", `{"uri":"file:///tmp/a.txt"}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("POST resources/read 失败 http=%d code=%d body=%s", code, env.Code, env.Data)
	}
	var rd dtocapability.McpResourceReadResp
	_ = json.Unmarshal(env.Data, &rd)
	if len(rd.Contents) != 1 || rd.Contents[0].Text != "read file:///tmp/a.txt" {
		t.Fatalf("uri 未透传到 server: %+v", rd.Contents)
	}
	// 非法 JSON → 40000（控制器）；缺 uri → 40001（域里判，控制器不代判）。
	if env, code := do(t, r, "POST", "/v1/mate/mcps/fs/resources/read", "{oops"); code != 200 || env.Code != 40000 {
		t.Fatalf("非法 body 应 40000，got http=%d code=%d", code, env.Code)
	}
	if env, code := do(t, r, "POST", "/v1/mate/mcps/fs/resources/read", "{}"); code != 200 || env.Code != 40001 {
		t.Fatalf("缺 uri 应由域回 40001，got http=%d code=%d", code, env.Code)
	}

	env, code = do(t, r, "GET", "/v1/mate/mcps/fs/prompts", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("GET prompts 失败 http=%d code=%d body=%s", code, env.Code, env.Data)
	}
	var ps dtocapability.McpPromptsResp
	_ = json.Unmarshal(env.Data, &ps)
	if ps.ServerID != "fs" || len(ps.Prompts) != 1 || ps.Prompts[0].Name != "greet" {
		t.Fatalf("提示词列表不符: %+v", ps)
	}

	env, code = do(t, r, "POST", "/v1/mate/mcps/fs/prompts/greet/get", `{"arguments":{"who":"世界"}}`)
	if code != 200 || env.Code != 0 {
		t.Fatalf("POST prompts/get 失败 http=%d code=%d body=%s", code, env.Code, env.Data)
	}
	var gp dtocapability.McpPromptGetResp
	_ = json.Unmarshal(env.Data, &gp)
	if len(gp.Messages) != 1 || !strings.Contains(string(gp.Messages[0].Content), "世界") {
		t.Fatalf("name/arguments 未透传到 server: %+v", gp.Messages)
	}
	// 空 body = 无参数取用（域只在有参数时才带 arguments）。
	env, code = do(t, r, "POST", "/v1/mate/mcps/fs/prompts/greet/get", "")
	if code != 200 || env.Code != 0 {
		t.Fatalf("空 body 应放行，got http=%d code=%d", code, env.Code)
	}
	var gpBare dtocapability.McpPromptGetResp
	_ = json.Unmarshal(env.Data, &gpBare)
	if len(gpBare.Messages) != 1 || strings.Contains(string(gpBare.Messages[0].Content), "世界") {
		t.Fatalf("空 body 应零参数: %+v", gpBare.Messages)
	}

	// 未知 server → 40401（四条路由共用同一个域入口，这里取资源面为代表）。
	if env, code := do(t, r, "GET", "/v1/mate/mcps/nope/resources", ""); code != 200 || env.Code != 40401 {
		t.Fatalf("未知 server 应 40401，got http=%d code=%d", code, env.Code)
	}
}

// TestResolvePermissionReceiptShape C5：POST /sessions/:id/permission 回执描述
// **实际投递结果**（allowed/rejected/always/请求 option_id/outcome/selected_option_id），
// 不再是 `{call_id, allowed}` 的请求参数回显。
func TestResolvePermissionReceiptShape(t *testing.T) {
	svc := &stubSvc{}
	deps := stubDeps(t, svc)
	deps.plane.RegisterSession(&session.SessionRec{ID: "s1"})
	r := newRestEngine(deps)
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

// TestLibraryRouteShape GET /library：真实用例跑起来（空注册表 → 空数组）+ 领域校验落成 40000。
//
// 替身撤掉之后这条用例验的是**装配**：控制器拿到的是域/包事实、用例在控制器里造、错误经信封映射。
// 读模型的过滤与逐 runtime 可达性在 `internal/service/svclibrary` 的测试里（那里有真注册表与投影）。
func TestLibraryRouteShape(t *testing.T) {
	svc := &stubSvc{}
	r := newRestEngine(stubDeps(t, svc))

	env, httpCode := do(t, r, "GET", "/v1/mate/library", "")
	if httpCode != 200 || env.Code != 0 {
		t.Fatalf("GET /library 应成功: http=%d env=%+v", httpCode, env)
	}
	// `applicability` 必须是空数组而不是 null（形状由用例层收口），空清单本身要是 `[]`。
	if got := strings.TrimSpace(string(env.Data)); got != "[]" {
		t.Fatalf("空注册表应回空数组: %s", got)
	}

	// 未知 kind 的**校验在用例层**（控制器只透传 query）——这里证明走的是真用例：
	// 替身会照 200 + 空表返回，而真用例铸 40000。
	env, httpCode = do(t, r, "GET", "/v1/mate/library?kind=command", "")
	if httpCode != 200 || env.Code != code.InvalidParam {
		t.Fatalf("用例层拒绝应落成 40000: http=%d env=%+v", httpCode, env)
	}
	if !strings.Contains(env.Msg, "未知 kind") {
		t.Fatalf("错误文案应来自用例层: %s", env.Msg)
	}
}

// 端口转换（v3.1 P4）：typed-nil 装进接口**不是 nil**，用例里 `st == nil` 的判据会失效。
// 测试装配与 `internal/bootstrap/assembly` 用同一条规矩：先判 nil，再交接口。
func nilSafeEventReader(st *store.Store) svcsession.EventReader {
	if st == nil {
		return nil
	}
	return st
}

func nilSafeSourceFacts(st *store.Store) svcsystem.SourceFactsReader {
	if st == nil {
		return nil
	}
	return st
}

func nilSafeModelStore(st *store.Store) svcmodel.ModelStore {
	if st == nil {
		return nil
	}
	return st
}
