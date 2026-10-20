package svcsession

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 会话写面用例（P5：从 `sessionruntime.Service` 搬入）———
//
// 为什么它们属于这里：`CreateSession` 的三条判据（`agent_id` 显式拒绝、工作空间解析与
// `workspace_id/cwd` 一致性、能力选定/原生模式的默认值合并）都是**用例语义**，与"谁拨号、
// 谁落库"无关；`PromptSession` 的 delivery 校验、`CancelRun` 的幂等口径同理。
//
// 搬走之后执行面只剩"能力"：`Orchestrator.StartSession/Prompt/CancelRun` 与
// `Service.DeliverQueued`。两个中心收成一个，`SessionSvc` 的实现不再跨包。

// SessionDeps 会话写面的依赖事实（装配点折端口交下来）。
type SessionDeps struct {
	Workspaces WorkspaceFacts
	Store      WorkspaceStore
	Starter    SessionStarter
	Prompt     PromptDelivery
	Canceller  RunCanceller
	Queued     QueuedDelivery
}

// Session 会话写面用例（`SessionSvc` 的唯一实现）。
type Session struct {
	deps SessionDeps
}

// NewSession 造会话写面用例。
func NewSession(deps SessionDeps) SessionSvc { return &Session{deps: deps} }

var _ SessionSvc = (*Session)(nil)

// capabilitySelectionFromReq 请求 DTO → 内部选定。
//
// `req == nil` **才**是"未声明"：`capabilities: {}` 是"声明了但什么都不限制"，
// 两者在库里的落值不同（空串 vs `{}`），代际也不同。这里不做任何"聪明的归一"。
func capabilitySelectionFromReq(req *dtosession.CapabilitiesReq) *session.CapabilitySelection {
	if req == nil {
		return nil
	}
	return &session.CapabilitySelection{Skills: session.NormalizeIDs(req.Skills), Mcps: session.NormalizeIDs(req.Mcps)}
}

// CreateSession 在构建期选定的底座上建立会话，若携带 prompt 则发起首轮（后台执行，事件经 WS 出流）。
func (s *Session) CreateSession(ctx context.Context, req dtosession.SessionCreateReq) (*dtosession.SessionCreateResp, error) {
	if s.deps.Starter == nil {
		return nil, code.FromError(code.ServiceError("orchestrator_unavailable", "服务尚未装配编排器"))
	}
	runCtx := context.WithoutCancel(ctx)
	if err := rejectRemovedAgentSelection(req.AgentID); err != nil {
		return nil, code.FromError(err)
	}
	target, assignment, err := s.deps.Workspaces.ResolveTargetWorkspace(req.WorkspaceID)
	if err != nil {
		return nil, code.FromError(err)
	}
	spec := req.Cwd
	if assignment != "" {
		row, ok, gerr := s.deps.Store.GetWorkspace(ctx, assignment)
		if gerr != nil {
			return nil, code.FromError(gerr)
		}
		if !ok {
			return nil, code.FromError(workspace.WorkspaceNotFoundError(assignment))
		}
		explicit := ""
		if strings.TrimSpace(req.Cwd) != "" {
			p, verr := workspace.ValidateWorkspace(req.Cwd)
			if verr != nil {
				return nil, code.FromError(verr)
			}
			explicit = p
		}
		if explicit != "" && explicit != row.Path {
			return nil, code.FromError(code.RequestError("workspace_conflict", fmt.Sprintf("workspace_id 与 cwd 指向不同目录：%s ≠ %s", row.Path, explicit)))
		}
		spec = row.Path
	}
	convID := objid.New(objid.Session)
	ws, err := workspace.ResolveWorkspace(spec, target)
	if err != nil {
		return nil, code.FromError(err)
	}
	if assignment != "" {
		ws.WorkspaceID = assignment
	} else if ws.WorkspaceID == "" {
		ws.WorkspaceID = s.deps.Workspaces.WorkspaceIDByPath(ctx, ws.Path)
	}
	selection := capabilitySelectionFromReq(req.Capabilities)
	if selection == nil && ws.WorkspaceID != "" {
		if def, ok := s.deps.Workspaces.WorkspaceDefaultsOf(ws.WorkspaceID); ok && def.CapabilityJSON != "" {
			selection = session.ParseCapabilitySelection(def.CapabilityJSON)
		}
	}
	nativeMode := ""
	if ws.WorkspaceID != "" {
		if def, ok := s.deps.Workspaces.WorkspaceDefaultsOf(ws.WorkspaceID); ok {
			nativeMode = def.PermissionMode
		}
	}
	res, err := s.deps.Starter.StartSession(runCtx, session.StartOptions{
		SessionID:          convID,
		Cwd:                ws.Path,
		WorkspaceTemporary: ws.Temporary,
		WorkspaceID:        ws.WorkspaceID,
		Model:              runtime.ModelConfig{Model: req.Model},
		Selection:          selection,
		NativeMode:         nativeMode,
		Origin:             req.Origin,
	})
	if err != nil {
		workspace.DiscardWorkspace(ws)
		return nil, code.FromError(err)
	}
	resp := &dtosession.SessionCreateResp{
		ID: res.SessionID, RuntimeID: res.Runtime, Status: res.Status, EventSeq: res.EventSeq,
		Cwd: ws.Path, WorkspaceID: ws.WorkspaceID, WorkspaceTemporary: ws.Temporary,
	}
	if req.Prompt != "" {
		seq, err := s.deps.Prompt.Prompt(runCtx, res.SessionID, req.Prompt)
		if err != nil {
			return nil, code.FromError(err)
		}
		resp.EventSeq = seq
		resp.Status = session.SessionStatusInProgress
	}
	return resp, nil
}

// PromptSession 向既有会话追加一轮 prompt（后台执行）。
func (s *Session) PromptSession(ctx context.Context, id string, req dtosession.PromptReq) (*dtosession.SessionPromptResp, error) {
	if s.deps.Prompt == nil {
		return nil, code.FromError(code.ServiceError("orchestrator_unavailable", "服务尚未装配编排器"))
	}
	if id == "" || req.Prompt == "" {
		return nil, code.FromError(code.RequestError("prompt_required", "session_id 与 prompt 必填"))
	}
	if _, err := session.NormalizeDelivery(req.Delivery); err != nil {
		return nil, code.FromError(err)
	}
	out, err := s.deps.Prompt.PromptWithDelivery(context.WithoutCancel(ctx), id, req.Prompt, req.Delivery)
	if err != nil {
		return nil, code.FromError(err)
	}
	return &dtosession.SessionPromptResp{
		ID: id, Status: session.SessionStatusInProgress, EventSeq: out.EventSeq,
		Queued: out.Queued, InputID: out.InputID,
	}, nil
}

// CancelRun 取消会话进行中的 run（无 run 时幂等 no-op）。
func (s *Session) CancelRun(ctx context.Context, id string) error {
	if s.deps.Canceller == nil {
		return code.FromError(code.ServiceError("orchestrator_unavailable", "服务尚未装配编排器"))
	}
	if err := s.deps.Canceller.CancelRun(ctx, id); err != nil {
		return code.FromError(err)
	}
	return nil
}

// DeliverQueuedNow 会话空闲时立刻投递队首排队输入（三种 no-op 都不报错）。
func (s *Session) DeliverQueuedNow(_ context.Context, id string) error {
	if s.deps.Queued == nil || id == "" {
		return nil
	}
	s.deps.Queued.DeliverQueued(id)
	return nil
}

// rejectRemovedAgentSelection 显式拒绝会话写面里的 `agent_id`（v19：agent 概念并入 runtime）。
func rejectRemovedAgentSelection(agentID json.RawMessage) error {
	if session.JSONNullish(agentID) {
		return nil
	}
	return code.RequestError("agent_selection_removed", "agent 概念已并入 runtime：底座由构建期固定（本机为 "+runtime.SelectedRuntime()+"），"+
		"会话不再有 `agent_id`；换底座要重新构建（-X pkg/runtime.BuildRuntime=<id>）")
}
