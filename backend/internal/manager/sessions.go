package manager

import (
	"context"
	"fmt"
	"github.com/morehao/ark-mate/internal/domain/session"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// ListSessions 返回会话概要；archived=false 仅未归档（默认），true 仅归档。
// cwd 非空 = 只返回工作空间精确等于该路径的会话（按工作空间过滤；路径须为绝对路径，
// 会话 cwd 已规范化为 canonical 形式，调用方传同一个字符串即可命中）。
// 按创建时间倒序（置顶优先）。
// ListSessions 列出会话（archived 过滤；cwd / workspaceID 非空则再收一道）。
//
// 两个过滤参数都是"按工作空间看"的入口，但口径不同（设计 ADR-2/ADR-5）：
//   - `cwd`：**执行事实**的字符串相等（历史行为，兼容保留）；
//   - `workspaceID`：**归属事实**（v14 起左栏分组用它，可索引，且不受路径写法差异影响）。
//
// 归属过滤会同时把"历史会话（workspace_id=”）"算进默认工作空间那一组：那些行在**读面**
// 归默认工作空间（设计 ADR-6），所以按 default id 过滤时它们必须出现——否则界面上默认工作
// 空间显示 N 个会话、点进去却是空的。
func (s *Service) ListSessions(archived bool, cwd, workspaceID string) ([]dtosession.SessionResp, error) {
	cwd = normalizeWorkspaceFilter(cwd)
	workspaceID = strings.TrimSpace(workspaceID)
	// 归属过滤要先把"默认工作空间 id"和"目标 id 是否就是默认"问出来（读面语义的一部分）。
	defaultID := ""
	if s.st != nil {
		if def, ok, err := s.st.DefaultWorkspace(context.Background()); err == nil && ok {
			defaultID = def.ID
		}
	}
	s.mu.Lock()
	out := make([]dtosession.SessionResp, 0, len(s.sessions))
	for _, r := range s.sessions {
		if r.Archived != archived {
			continue
		}
		if cwd != "" && r.Cwd != cwd {
			continue
		}
		if workspaceID != "" && !workspaceMatches(r.WorkspaceID, workspaceID, defaultID) {
			continue
		}
		resp := toSessionResp(r)
		// 归属的**原始值**必须先落到响应上：`toSessionResp` 刻意不带它（那会持锁读库），
		// 而下面的 fill 要靠它去解析展示名。漏这一步的表现是"所有会话都归到默认工作空间"
		// ——一个看起来完全正常的错误分组。
		resp.WorkspaceID = r.WorkspaceID
		out = append(out, resp)
	}
	s.mu.Unlock()
	// 展示字段（workspace_id / workspace_title）在**释放锁之后**补齐：它们要读库，
	// 而持锁读库会与 registerSession 的"先写内存再落库"顺序互相卡住。
	s.fillWorkspaceDisplay(out)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

// workspaceMatches 归属过滤的判定（设计 ADR-6）：`rWorkspaceID == ”` 的行归默认工作空间。
//
// 单独成函数是为了让这条语义只有一处：把它内联进循环，那个 `== ""` 的特例迟早被漏掉一处，
// 而漏掉的表现是"默认工作空间下面看不到历史会话"——一个没有解释余地的空列表。
func workspaceMatches(recWorkspaceID, wantID, defaultID string) bool {
	if wantID == defaultID {
		return recWorkspaceID == "" || recWorkspaceID == wantID
	}
	return recWorkspaceID == wantID
}

// fillWorkspaceDisplay 给一批响填补归属展示字段（v14；读面解析，见设计 §4.3）。
func (s *Service) fillWorkspaceDisplay(items []dtosession.SessionResp) {
	for i := range items {
		id, title := s.workspaceDisplayOf(items[i].WorkspaceID)
		items[i].WorkspaceID = id
		items[i].WorkspaceTitle = title
	}
}

// toSessionResp 把 SessionRec 折叠为 dtosession.SessionResp。
//
// **不含归属展示字段**（workspace_id / workspace_title）：那两个要读库，而本函数在持 s.mu 的
// 调用点里跑。需要它们的调用点用 `fillWorkspaceDisplay` 在释放锁之后补——这条分界必须显式，
// 否则下一个人会顺手在这里加一次查库，而症状是偶发死锁。
func toSessionResp(r *SessionRec) dtosession.SessionResp {
	return dtosession.SessionResp{
		Capabilities:       toCapabilitiesResp(r),
		ID:                 r.ID,
		RuntimeSessionID:   r.RuntimeSessionID,
		RuntimeID:          r.RuntimeID,
		Status:             r.Status,
		Title:              r.Title,
		Pinned:             r.Pinned,
		Archived:           r.Archived,
		Model:              r.ModelID,
		Cwd:                r.Cwd,
		WorkspaceTemporary: r.WorkspaceTemporary,
		Origin:             r.Origin,
		CreatedAt:          r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:          r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// capabilitySelectionFromReq 请求 DTO → 内部选定。
//
// `req == nil` **才**是"未声明"：`capabilities: {}` 是"声明了但什么都不限制"，
// 两者在库里的落值不同（空串 vs `{}`），代际也不同（见 CapabilitySelection 的注释）。
// 这里不做任何"聪明的归一"——把 `{}` 折叠成未声明会让 GET 回显与用户发的东西不一致。
func capabilitySelectionFromReq(req *dtosession.CapabilitiesReq) *CapabilitySelection {
	if req == nil {
		return nil
	}
	return &CapabilitySelection{Skills: normalizeIDs(req.Skills), Mcps: normalizeIDs(req.Mcps)}
}

// toCapabilitiesResp 把会话钉住的选定折叠成对外形态（nil = 未声明 → 缺省路径）。
//
// 判据是**记录本身**（CapabilityJSON 空 = 未声明），不是"指纹是否为空"：
// 两者今天同向（未声明 ⇔ 空指纹），但记录才是事实源，回显不该依赖一个派生量。
func toCapabilitiesResp(r *SessionRec) *dtosession.SessionCapabilitiesResp {
	if r == nil || strings.TrimSpace(r.CapabilityJSON) == "" {
		return nil
	}
	sel := ParseCapabilitySelection(r.CapabilityJSON)
	if sel == nil {
		return nil
	}
	// 逐 kind 原样回显：**缺键 = 未声明该 kind**（回落上一层），`[]` = 明确一个都不要。
	// 不做"未声明就补空数组"的归一——那会把两个语义相反的状态画成同一个样子，
	// 用户看到 `skills: []` 会以为"这个会话没有技能"，实际是"投给它的全部技能"。
	var skills, mcps *[]string
	if sel.Skills != nil {
		v := derefList(sel.Skills)
		skills = &v
	}
	if sel.Mcps != nil {
		v := derefList(sel.Mcps)
		mcps = &v
	}
	return &dtosession.SessionCapabilitiesResp{
		Skills: skills, Mcps: mcps, Fingerprint: r.Fingerprint,
	}
}

// GetSession 返回单会话详情（元数据 + 挂起权限 + 会话级始终允许记忆）。
func (s *Service) GetSession(ctx context.Context, id string) (*dtosession.SessionDetailResp, error) {
	if !s.hasSession(id) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	pending := s.PendingPermissions(id)
	perms := make([]dtosession.PermissionResp, 0, len(pending))
	for _, p := range pending {
		perms = append(perms, toPermissionResp(p))
	}
	mems := make([]dtosession.MemoryResp, 0)
	for _, it := range s.PermissionMemories(id) {
		mems = append(mems, dtosession.MemoryResp{Title: it.Title, Kind: it.Kind, Decision: it.Decision, Since: it.Since.UTC().Format(time.RFC3339)})
	}
	detail := dtosession.SessionDetailResp{SessionResp: s.sessionResp(id), Pending: perms, Memories: mems}
	return &detail, nil
}

// UpdateSession 更新会话元数据（title/pinned/archived 子集；nil 字段不更新）。
// sqlite 模式下同步写库（sessions 为 DB 权威元数据镜像）。
func (s *Service) UpdateSession(ctx context.Context, id string, patch dtosession.SessionPatch) (*dtosession.SessionResp, error) {
	s.mu.Lock()
	r, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if patch.Title != nil {
		r.Title = *patch.Title
	}
	if patch.Pinned != nil {
		r.Pinned = *patch.Pinned
	}
	if patch.Archived != nil {
		r.Archived = *patch.Archived
	}
	// 会话的 runtime 身份在**持锁区**里读出来（它在会话建立后不再变，但"不变"不该靠约定：
	// 解锁之后再读 `r` 的字段，读到的就可能是另一个 goroutine 刚写过的值）。
	sessionRuntime := r.RuntimeID
	r.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()

	// 会话内改模型：活会话经 session/set_config_option 立即生效（运行时是唯一判据）；
	// 失败即上抛且**不落元数据**（避免「界面显示新模型、实际仍用旧的」的静默偏差）。
	// 会话不活跃（未启动/已断连）→ 只更新元数据，下次恢复会话时生效。
	//
	// v26：`patch.Model` 是**平台模型行 id**，必须先在**活跃集合之外**解析成"行 + 运行时值"
	// （解析要读库，而上面那段持锁区不许读库——症状是偶发死锁）。解析失败即上抛（40401），
	// **一个字节都不落库**：静默换模型是这一版最该防的失败。
	appliedModelID := patch.Model
	if patch.Model != nil {
		platformID, cfg, err := s.resolveSessionModel(ctx, sessionRuntime, *patch.Model)
		if err != nil {
			return nil, code.FromError(err)
		}
		applied, err := s.orch.ApplySessionModel(ctx, id, cfg)
		if err != nil {
			return nil, code.FromError(err)
		}
		if !applied {
			glog.Debugw(ctx, "会话不在活跃集合，模型变更仅更新元数据", "session", id)
		}
		s.mu.Lock()
		// **落库的是身份**（平台行 id），不是运行时值——定案「辰」的核心。
		r.ModelID = platformID
		r.UpdatedAt = time.Now().UTC()
		s.mu.Unlock()
		appliedModelID = &platformID
	}

	if s.st != nil {
		meta := model.SessionMetaPatch{Title: patch.Title, Pinned: patch.Pinned, Archived: patch.Archived, ModelID: appliedModelID}
		ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := s.st.UpdateSessionMeta(ctx2, id, meta); err != nil {
			glog.Warnw(ctx, "store: update session meta failed", "session", id, "err", err)
		}
	}
	s.mu.Lock()
	resp := toSessionResp(r)
	s.mu.Unlock()
	return &resp, nil
}

// sessionResp 返回单个会话的轻量表示（须持锁调用）。
func (s *Service) sessionResp(id string) dtosession.SessionResp {
	r := s.sessions[id]
	return toSessionResp(r)
}

// deriveTitle 从首条用户消息推导会话标题（单行、截断、折叠空白）。
// 截断按 rune 数判断（len() 是字节数，CJK 一个字符占 3 字节：按字节判断会
// 在「字节够长但字符不足 48」时对 runes[:48] 越界 panic）。
func deriveTitle(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 48 {
		text = string(runes[:48]) + "…"
	}
	return text
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
		return nil, code.FromError(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
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
	target, assignment, err := s.resolveTargetWorkspace(req.WorkspaceID)
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
		row, ok, gerr := s.st.GetWorkspace(ctx, assignment)
		if gerr != nil {
			return nil, code.FromError(gerr)
		}
		if !ok {
			return nil, code.FromError(workspaceNotFoundError(assignment))
		}
		explicit := ""
		if strings.TrimSpace(req.Cwd) != "" {
			p, verr := ValidateWorkspace(req.Cwd)
			if verr != nil {
				return nil, code.FromError(verr)
			}
			explicit = p
		}
		if explicit != "" && explicit != row.Path {
			return nil, code.FromError(acl.NewError(acl.ClassRequest, "workspace_conflict",
				fmt.Sprintf("workspace_id 与 cwd 指向不同目录：%s ≠ %s", row.Path, explicit), nil))
		}
		spec = row.Path
	}
	convID := objid.New(objid.Session)
	ws, err := ResolveWorkspace(spec, target)
	if err != nil {
		return nil, code.FromError(err)
	}
	// 归属与 cwd 定稿：用户显式选中的登记项优先；否则按 cwd 查登记表补（查不到就留空，
	// 读面归默认工作空间——**不自动登记**用户没主动选过的目录，设计 §3.2）。
	if assignment != "" {
		ws.WorkspaceID = assignment
	} else if ws.WorkspaceID == "" {
		ws.WorkspaceID = s.workspaceIDByPath(ctx, ws.Path)
	}
	// 能力选定（M3）：`capabilities` 未传 → nil，与引入本字段之前逐字节相同。
	// 传了（哪怕 `{}`）→ 声明了：两个 kind 各自 nil（不限制）或白名单，由 DTO 的指针区分。
	// 可达性校验在编排器内、**建连之前**做（校验不过一个副作用都不留：不建会话/不拉进程/不写库）。
	selection := capabilitySelectionFromReq(req.Capabilities)
	// 底座不由请求指定（构建期固定，见 pkg/runtime/build.go）：这里没有 runtime 参数要校验。
	res, err := s.startSession(runCtx, StartOptions{
		SessionID:          convID,
		Cwd:                ws.Path,
		WorkspaceTemporary: ws.Temporary,
		WorkspaceID:        ws.WorkspaceID,
		Model:              runtime.ModelConfig{Model: req.Model},
		Selection:          selection,
		Origin:             req.Origin,
	})
	if err != nil {
		// v14 起这是空操作（不再有会话独占目录要清理；默认工作空间是共享的，删不得）。
		DiscardWorkspace(ws)
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
func (s *Service) startSession(ctx context.Context, opt StartOptions) (*CreateResult, error) {
	return s.orch.StartSession(ctx, opt)
}

// PromptSession 向既有会话追加一轮 prompt（后台执行）。
// run 生命周期同样脱离 HTTP 请求（WithoutCancel），结束/取消只由 agent 或
// POST /sessions/:id/cancel 决定。
func (s *Service) PromptSession(ctx context.Context, id string, req dtosession.PromptReq) (*dtosession.SessionPromptResp, error) {
	if s.orch == nil {
		return nil, code.FromError(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if id == "" || req.Prompt == "" {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "prompt_required", "session_id 与 prompt 必填", nil))
	}
	// 投递模式先校验：非法值不该走完 acquire 才发现（§6；空 = reject = 历史行为）。
	if _, err := NormalizeDelivery(req.Delivery); err != nil {
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

// DeleteQueuedInput 撤下一条尚未投递的排队输入（§6）。撤下后发 session.input_cancelled——
// 队列项的离队必须是一条可回放的事实（前端队列只认事件，不做本地推断）。
func (s *Service) DeleteQueuedInput(ctx context.Context, id, inputID string) (bool, error) {
	if id == "" || inputID == "" {
		return false, code.FromError(acl.NewError(acl.ClassRequest, "input_required", "session_id 与 input_id 必填", nil))
	}
	ok, err := s.deleteQueuedInput(ctx, id, inputID)
	if err != nil {
		return false, code.FromError(err)
	}
	if ok {
		runtimeID := ""
		if rec := s.sessionRec(id); rec != nil {
			runtimeID = rec.RuntimeID
		}
		s.publish(session.BusInputCancelled, id, map[string]any{
			"session_id": id, "runtime": runtimeID, "input_id": inputID, "reason": ReasonUserCancel,
		})
	}
	return ok, nil
}

// CancelRun 取消会话进行中的 run（无 run 时幂等 no-op）。
func (s *Service) CancelRun(ctx context.Context, id string) error {
	if s.orch == nil {
		return code.FromError(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if err := s.orch.CancelRun(ctx, id); err != nil {
		return code.FromError(err)
	}
	return nil
}

// SessionHistory 返回会话事件历史（seq 升序，after 向后 / before 向前）。
//
// 数据源：sqlite 模式（st != nil）读 events 表——durable、不受 eventbus 内存上限（65536）约束，
// 「加载更早」可一直翻到会话第一条；内存模式回放 eventbus（进程内）。
// 语义：after（开区间，seq > after）、before（开区间，seq < before，返回最近 limit 条并升序排列）
// 互斥（handler 已拦，服务层再兜一次 → 40001）；has_more 沿当前方向；首尾 seq 一并回带。
//
// **游标口径（v21）**：`seq` 是会话内位次、0 是合法值，所以"没有 after"的哨兵是 `-1`（早于该会话
// 一切事件），由 handler 在缺省时填入；`after = 0` 表示"第 0 条之后"（跳过 `session.created`）。
// 空页的 FirstSeq/LastSeq 回 **-1**（此前是 0，而 0 现在是一个真实位次，"空"与"第 0 条"必须可分）。
func (s *Service) SessionHistory(ctx context.Context, id string, q dtosession.HistoryQuery) (*dtosession.SessionHistoryResp, error) {
	if !s.hasSession(id) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if q.After != nil && q.Before != nil {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "history_cursor_conflict",
			"after 与 before 互斥（一个向后、一个向前）", nil))
	}
	// 缺席的游标 = -1（v21 哨兵："没有游标"）。两个方向都**不夹到 0**：0 是会话第一条事件的
	// 合法位次，夹没了它，首屏/翻页就会丢掉那一条。
	after, before := int64(-1), int64(-1)
	if q.After != nil {
		if after = *q.After; after < -1 {
			after = -1
		}
	}
	if q.Before != nil {
		if before = *q.Before; before < -1 {
			before = -1
		}
	}
	if q.Limit < 1 {
		q.Limit = 1
	}
	if q.Limit > 1000 {
		q.Limit = 1000
	}

	var (
		events  []eventbus.Event
		hasMore bool
	)
	if s.st != nil {
		var batch []model.EventEntity
		var err error
		if q.Before != nil {
			batch, hasMore, err = s.st.EventsBefore(ctx, id, before, int64(q.Limit))
		} else {
			batch, hasMore, err = s.st.EventsAfter(ctx, id, after, int64(q.Limit))
		}
		if err != nil {
			return nil, code.FromError(acl.NewError(acl.ClassService, "history_read_failed",
				fmt.Sprintf("读取会话历史失败：%v", err), nil))
		}
		events = make([]eventbus.Event, 0, len(batch))
		for _, e := range batch {
			events = append(events, eventbus.Event{
				ID: e.ID, Type: e.Type, Seq: e.Seq, AggregID: e.SessionID, Payload: decodePayload(e.PayloadJSON),
			})
		}
	} else {
		// 内存模式：先取该会话**全部**事件（起点固定 `-1`，与游标无关——游标只决定裁哪一端），
		// 再按方向裁窗口。起点若跟着一个缺省的 after=0 走，会让"翻更早"这条路径每次静默
		// 丢掉第 0 条事件。
		mine := s.bus.EventsAfter(id, -1)
		if q.Before != nil {
			// `before < 0` = 从最新往回（哨兵）；`before >= 0` 是"该位次之前"。
			if before >= 0 {
				kept := mine[:0]
				for _, evt := range mine {
					if evt.Seq < before {
						kept = append(kept, evt)
					}
				}
				mine = kept
			}
			// 取窗口末尾 limit 条（保持升序）；窗口外还有更早 → has_more。
			if len(mine) > q.Limit {
				hasMore = true
				mine = mine[len(mine)-q.Limit:]
			}
		} else {
			if after >= 0 {
				kept := mine[:0]
				for _, evt := range mine {
					if evt.Seq > after {
						kept = append(kept, evt)
					}
				}
				mine = kept
			}
			if len(mine) > q.Limit {
				hasMore = true
				mine = mine[:q.Limit]
			}
		}
		events = mine
	}

	resp := &dtosession.SessionHistoryResp{ID: id, Events: events, HasMore: hasMore, FirstSeq: -1, LastSeq: -1}
	if len(events) > 0 {
		resp.FirstSeq = events[0].Seq
		resp.LastSeq = events[len(events)-1].Seq
	}
	return resp, nil
}
