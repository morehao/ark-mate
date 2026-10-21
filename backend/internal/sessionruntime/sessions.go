package sessionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/morehao/ark-mate/internal/core/failure"
	"strings"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/workspace"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/dto/dtosession"

	"github.com/morehao/ark-mate/internal/runtime"
)

// capabilitySelectionFromReq 请求 DTO → 内部选定。
//
// `req == nil` **才**是"未声明"：`capabilities: {}` 是"声明了但什么都不限制"，
// 两者在库里的落值不同（空串 vs `{}`），代际也不同（见 session.CapabilitySelection 的注释）。
// 这里不做任何"聪明的归一"——把 `{}` 折叠成未声明会让 GET 回显与用户发的东西不一致。
func capabilitySelectionFromReq(req *dtosession.CapabilitiesReq) *session.CapabilitySelection {
	if req == nil {
		return nil
	}
	// P3c：选定值对象与规范化都在 `domain/session`（`NormalizeIDs` 是那条规则的唯一实现）。
	return &session.CapabilitySelection{Skills: session.NormalizeIDs(req.Skills), Mcps: session.NormalizeIDs(req.Mcps)}
}

// CreateSession 在 agent 对应 runtime 上建立会话，若携带 prompt 则同步发起首轮
// （后台执行，事件经 bus/WS 出流）。返回的 EventSeq 为「已发布事件」后的水位，
// 前端 WS attach 传 since_seq=该值即可续传首轮增量。
//
// 工作空间（cwd）在此定稿并随响应回读：
//   - req.Cwd 非空 → 校验（绝对/存在/是目录）+ 符号链接规范化 + 根白名单（workspaceRoots）；
//   - req.Cwd 为空 → 落在**默认工作空间**（一个落库的登记项；Temporary=true）；
//   - 没有默认工作空间且未指定 → workspace_required（不回落 os.Getwd()）。
//
// 归属（workspace_id）与 cwd 是两件事（设计 ADR-2）：cwd 是执行事实、归属是分组事实。
// 显式 cwd 命中已登记目录时按路径补上归属；命中不了就留空（读面归默认工作空间）
// ——**不自动登记**用户没主动选过的目录（设计 §3.2 的处置）。
func (s *Service) CreateSession(ctx context.Context, req dtosession.SessionCreateReq) (*dtosession.SessionCreateResp, error) {
	if s.orch == nil {
		return nil, code.FromError(failure.NewError(failure.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	// 会话与 run 生命周期不随 HTTP 请求结束：WithoutCancel 继承 values 但脱离取消
	//（run 由 Orchestrator 持有，取消走 POST /sessions/:id/cancel）。
	runCtx := context.WithoutCancel(ctx)
	// 写面显式拒绝 `agent_id`（v19：agent 概念整个并入 runtime）：底座构建期固定，收下这个键
	// 只会让用户以为"我给这个会话指定了 agent"。**在解析工作空间之前**判定——工作空间解析会
	// 触及文件系统，不该为一个本来就无效的请求留下痕迹。
	if err := rejectRemovedAgentSelection(req.AgentID); err != nil {
		return nil, code.FromError(err)
	}
	// 归属目标：显式 `workspace_id` 优先（选择器选中了某个登记项），否则用默认工作空间。
	// 两者都给出且不一致 → 拒绝：猜用户的意图迟早猜错，而这一处猜错的后果是会话跑错目录
	// （那个目录正是原生权限 workspace-write 的沙箱边界）。
	target, assignment, err := s.planes.Workspaces.ResolveTargetWorkspace(req.WorkspaceID)
	if err != nil {
		return nil, code.FromError(err)
	}
	// 定出 cwd 的**规格**并校验：
	//   - 用户显式选了登记项 → 规格取它的路径（此时 cwd 可有可无）；
	//   - 否则用请求里的 cwd（空 = 交给 ResolveWorkspace 落默认工作空间）。
	// 两条来源同时给出时必须一致（见 resolveTargetWorkspace）；比对走同一条规范化，否则
	// /tmp 与 /private/tmp 会被判成冲突（macOS 上它们是同一个目录）。
	spec := req.Cwd
	if assignment != "" {
		row, ok, gerr := s.planes.Resources.Store().GetWorkspace(ctx, assignment)
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
			return nil, code.FromError(failure.NewError(failure.ClassRequest, "workspace_conflict",
				fmt.Sprintf("workspace_id 与 cwd 指向不同目录：%s ≠ %s", row.Path, explicit), nil))
		}
		spec = row.Path
	}
	convID := objid.New(objid.Session)
	ws, err := workspace.ResolveWorkspace(spec, target)
	if err != nil {
		return nil, code.FromError(err)
	}
	// 归属与 cwd 定稿：用户显式选中的登记项优先；否则按 cwd 查登记表补（查不到就留空，
	// 读面归默认工作空间——**不自动登记**用户没主动选过的目录，设计 §3.2）。
	if assignment != "" {
		ws.WorkspaceID = assignment
	} else if ws.WorkspaceID == "" {
		ws.WorkspaceID = s.planes.Workspaces.WorkspaceIDByPath(ctx, ws.Path)
	}
	// 能力选定（M3）：`capabilities` 未传 → nil，与引入本字段之前逐字节相同。
	// 传了（哪怕 `{}`）→ 声明了：两个 kind 各自 nil（不限制）或白名单，由 DTO 的指针区分。
	// 可达性校验在编排器内、**建连之前**做（校验不过一个副作用都不留：不建会话/不拉进程/不写库）。
	selection := capabilitySelectionFromReq(req.Capabilities)
	// 工作空间**默认值**（v27 / ADR-7）：请求没声明能力选定时，落到该工作空间声明的默认那一档。
	//
	// 合并链只有一份实现（`workspace.ResolveSessionDefaults`）：显式 > 工作空间默认 > 平台缺省。
	// 这里只负责把"请求写了什么"与"工作空间声明了什么"交给它——**不在这里再写一遍 if**，
	// 那是"某个入口不吃工作空间默认值"的唯一来源。
	if selection == nil && ws.WorkspaceID != "" {
		if def, ok := s.planes.Workspaces.WorkspaceDefaultsOf(ws.WorkspaceID); ok && def.CapabilityJSON != "" {
			// `ParseCapabilitySelection` 对坏 JSON 回 nil（= 未声明）并留日志——默认值是**便利**，
			// 不是"会话能不能建"的判据。
			selection = session.ParseCapabilitySelection(def.CapabilityJSON)
		}
	}
	// **原生权限模式**（v29 / P1-f 下半）：同一条合并链——请求没声明就落到该工作空间声明的默认
	// 那一档，都没有就**不注入**（空串，刻意不物化平台缺省）。
	//
	// 为什么必须在**这里**定下来并落库：模式只能以进程 env（`DSH_PERMISSION_MODE`）下发
	// （探针实测 `/permission` 命令面在 ACP 上不存在），所以两个模式不同的会话不能共用一个暖
	// 进程；而暖进程的键要在取连接之前已知 ⇒ 模式是会话的**快照字段**，不是每次现算的推导值。
	nativeMode := ""
	if ws.WorkspaceID != "" {
		if def, ok := s.planes.Workspaces.WorkspaceDefaultsOf(ws.WorkspaceID); ok {
			nativeMode = def.PermissionMode
		}
	}
	// 底座不由请求指定（构建期固定，见 pkg/runtime/build.go）：这里没有 runtime 参数要校验。
	res, err := s.startSession(runCtx, session.StartOptions{
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
		// v14 起这是空操作（不再有会话独占目录要清理；默认工作空间是共享的，删不得）。
		workspace.DiscardWorkspace(ws)
		return nil, code.FromError(err)
	}
	resp := &dtosession.SessionCreateResp{
		ID: res.SessionID, RuntimeID: res.Runtime, Status: res.Status, EventSeq: res.EventSeq,
		Cwd: ws.Path, WorkspaceID: ws.WorkspaceID, WorkspaceTemporary: ws.Temporary,
	}
	if req.Prompt != "" {
		seq, err := s.orch.Prompt(runCtx, res.SessionID, req.Prompt)
		if err != nil {
			return nil, code.FromError(err)
		}
		resp.EventSeq = seq
		resp.Status = session.SessionStatusInProgress
	}
	return resp, nil
}

// startSession 建会话。**底座不由调用方指定**：编排器内部取构建期选定的那一个
// （见 pkg/runtime/build.go 与 Orchestrator.StartSession）。
func (s *Service) startSession(ctx context.Context, opt session.StartOptions) (*session.CreateResult, error) {
	return s.orch.StartSession(ctx, opt)
}

// PromptSession 向既有会话追加一轮 prompt（后台执行）。
// run 生命周期同样脱离 HTTP 请求（WithoutCancel），结束/取消只由 agent 或
// POST /sessions/:id/cancel 决定。
func (s *Service) PromptSession(ctx context.Context, id string, req dtosession.PromptReq) (*dtosession.SessionPromptResp, error) {
	if s.orch == nil {
		return nil, code.FromError(failure.NewError(failure.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if id == "" || req.Prompt == "" {
		return nil, code.FromError(failure.NewError(failure.ClassRequest, "prompt_required", "session_id 与 prompt 必填", nil))
	}
	// 投递模式先校验：非法值不该走完 acquire 才发现（§6；空 = reject = 历史行为）。
	if _, err := session.NormalizeDelivery(req.Delivery); err != nil {
		return nil, code.FromError(err)
	}
	out, err := s.orch.PromptWithDelivery(context.WithoutCancel(ctx), id, req.Prompt, req.Delivery)
	if err != nil {
		return nil, code.FromError(err)
	}
	return &dtosession.SessionPromptResp{
		ID: id, Status: session.SessionStatusInProgress, EventSeq: out.EventSeq,
		Queued: out.Queued, InputID: out.InputID,
	}, nil
}

// CancelRun 取消会话进行中的 run（无 run 时幂等 no-op）。
func (s *Service) CancelRun(ctx context.Context, id string) error {
	if s.orch == nil {
		return code.FromError(failure.NewError(failure.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if err := s.orch.CancelRun(ctx, id); err != nil {
		return code.FromError(err)
	}
	return nil
}

// CancelElicitations 归档时作废该会话的挂起提问（`reason:archive`，ADR-11）。
//
// 走 `CancelSessionReason` 而不是直接删表项：那一支会把 `cancel` 投给正在等的那一轮、
// 再由 `RegisterAndWait` 的终局回调发 `elicitation_resolved{action:cancel, reason:archive}`
// ——**事件是前端收敛卡片的唯一事实**，删表项不发事件等于留一张永远回答不了的卡。
func (s *Service) CancelElicitations(sessionID string) {
	if s.orch == nil || sessionID == "" {
		return
	}
	s.orch.CancelElicitations(sessionID)
}

// ——— 已下线键的**显式拒绝** ———
//
// 纪律（与 `rejectRuntimeSelection`/`rejectDerivedDefaultModel`/`rejectRuntimeHintInPatch` 同源）：
// JSON 绑定默认**丢弃未知键**，所以"把字段从 DTO 里删掉"并不等于"用户带了这个键会被拦下"——
// 它会让那个键变成「写了、存了、什么都不发生」的骗人字段（业务风险 B1）。凡是**平台上不再存在
// 的可选面**，都要留一个同名 raw 字段，出现即 40001 + 指路。`null` 视为"没带"。

// rejectRemovedAgentSelection 显式拒绝会话写面里的 `agent_id`（v19：agent 概念整个并入 runtime）。
//
// 为什么宁可报错也不静默忽略：底座由**构建期固定**（`pkg/runtime/build.go`，用户不可自定义），
// 而 JSON 绑定会默默收下 `{"agent_id":"x"}` —— 用户会以为"我给这个会话指定了 agent"，实际上
// 那个键从未被读过。指路的方向也写在文案里：换底座要重新构建（`-X pkg/runtime.BuildRuntime=<id>`）。
func rejectRemovedAgentSelection(agentID json.RawMessage) error {
	if session.JSONNullish(agentID) {
		return nil
	}
	return failure.NewError(failure.ClassRequest, "agent_selection_removed",
		"agent 概念已并入 runtime：底座由构建期固定（本机为 "+runtime.SelectedRuntime()+"），"+
			"会话不再有 `agent_id`；换底座要重新构建（-X pkg/runtime.BuildRuntime=<id>）", nil)
}
