package manager

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/morehao/ark-mate/internal/dto/dtoconversation"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// ListConversations 返回会话概要；archived=false 仅未归档（默认），true 仅归档。
// cwd 非空 = 只返回工作空间精确等于该路径的会话（按工作空间过滤；路径须为绝对路径，
// 会话 cwd 已规范化为 canonical 形式，调用方传同一个字符串即可命中）。
// 按创建时间倒序（置顶优先）。
func (s *Service) ListConversations(archived bool, cwd string) ([]dtoconversation.ConversationResp, error) {
	cwd = normalizeWorkspaceFilter(cwd)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]dtoconversation.ConversationResp, 0, len(s.conversations))
	for _, r := range s.conversations {
		if r.Archived != archived {
			continue
		}
		if cwd != "" && r.Cwd != cwd {
			continue
		}
		out = append(out, toConversationResp(r))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

// toConversationResp 把 ConversationRec 折叠为 dtoconversation.ConversationResp。
func toConversationResp(r *ConversationRec) dtoconversation.ConversationResp {
	return dtoconversation.ConversationResp{
		Capabilities:       toCapabilitiesResp(r),
		ID:                 r.ID,
		AgentID:            r.AgentID,
		Status:             r.Status,
		Title:              r.Title,
		Pinned:             r.Pinned,
		Archived:           r.Archived,
		Provider:           r.Provider,
		Model:              r.Model,
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
func capabilitySelectionFromReq(req *dtoconversation.CapabilitiesReq) *CapabilitySelection {
	if req == nil {
		return nil
	}
	return &CapabilitySelection{Skills: normalizeIDs(req.Skills), Mcps: normalizeIDs(req.Mcps)}
}

// toCapabilitiesResp 把会话钉住的选定折叠成对外形态（nil = 未声明 → 缺省路径）。
//
// 判据是**记录本身**（CapabilityJSON 空 = 未声明），不是"指纹是否为空"：
// 两者今天同向（未声明 ⇔ 空指纹），但记录才是事实源，回显不该依赖一个派生量。
func toCapabilitiesResp(r *ConversationRec) *dtoconversation.ConversationCapabilitiesResp {
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
	return &dtoconversation.ConversationCapabilitiesResp{
		Skills: skills, Mcps: mcps, Fingerprint: r.Fingerprint,
	}
}

// GetConversation 返回单会话详情（元数据 + 挂起权限 + 会话级始终允许记忆）。
func (s *Service) GetConversation(ctx context.Context, id string) (*dtoconversation.ConversationDetailResp, error) {
	if !s.hasConversation(id) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	pending := s.PendingPermissions(id)
	perms := make([]dtoconversation.PermissionResp, 0, len(pending))
	for _, p := range pending {
		perms = append(perms, toPermissionResp(p))
	}
	mems := make([]dtoconversation.MemoryResp, 0)
	for _, it := range s.PermissionMemories(id) {
		mems = append(mems, dtoconversation.MemoryResp{Title: it.Title, Kind: it.Kind, Decision: it.Decision, Since: it.Since.UTC().Format(time.RFC3339)})
	}
	detail := dtoconversation.ConversationDetailResp{ConversationResp: s.conversationResp(id), Pending: perms, Memories: mems}
	return &detail, nil
}

// UpdateConversation 更新会话元数据（title/pinned/archived 子集；nil 字段不更新）。
// sqlite 模式下同步写库（conversations 为 DB 权威元数据镜像）。
func (s *Service) UpdateConversation(ctx context.Context, id string, patch dtoconversation.ConversationPatch) (*dtoconversation.ConversationResp, error) {
	s.mu.Lock()
	r, ok := s.conversations[id]
	if !ok {
		s.mu.Unlock()
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", id), nil))
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
	r.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()

	// 会话内改模型：活会话经 session/set_config_option 立即生效（运行时是唯一判据）；
	// 失败即上抛且**不落元数据**（避免「界面显示新模型、实际仍用旧的」的静默偏差）。
	// 会话不活跃（未启动/已断连）→ 只更新元数据，下次恢复会话时生效。
	if patch.Model != nil {
		applied, err := s.orch.ApplyConversationModel(ctx, id, runtime.ModelConfig{Model: *patch.Model})
		if err != nil {
			return nil, code.FromError(err)
		}
		if !applied {
			glog.Debugw(ctx, "会话不在活跃集合，模型变更仅更新元数据", "conversation", id)
		}
		s.mu.Lock()
		r.Model = *patch.Model
		r.UpdatedAt = time.Now().UTC()
		s.mu.Unlock()
	}

	if s.st != nil {
		meta := store.ConversationMetaPatch{Title: patch.Title, Pinned: patch.Pinned, Archived: patch.Archived, Model: patch.Model}
		ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := s.st.UpdateConversationMeta(ctx2, id, meta); err != nil {
			glog.Warnw(ctx, "store: update conversation meta failed", "conversation", id, "err", err)
		}
	}
	s.mu.Lock()
	resp := toConversationResp(r)
	s.mu.Unlock()
	return &resp, nil
}

// conversationResp 返回单个会话的轻量表示（须持锁调用）。
func (s *Service) conversationResp(id string) dtoconversation.ConversationResp {
	r := s.conversations[id]
	return toConversationResp(r)
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

// CreateConversation 在 agent 对应 runtime 上建立会话，若携带 prompt 则同步发起首轮
// （后台执行，事件经 bus/WS 出流）。返回的 EventSeq 为「已发布事件」后的水位，
// 前端 WS attach 传 since_seq=该值即可续传首轮增量。
//
// 工作空间（cwd）在此定稿并随响应回读：
//   - req.Cwd 非空 → 校验（绝对/存在/是目录）+ 符号链接规范化 + 根白名单（workspaceRoots）；
//   - req.Cwd 为空 → 平台在 workspaceDir 下按**会话 id**代建临时目录（WorkspaceTemporary=true）；
//   - 代建关闭且未指定 → workspace_required（不回落 os.Getwd()）。
//
// 会话 id 在 session/new 之前定号：代建目录名以它为准，且目录必须先于 session/new 存在
// （底座对 cwd 做存在性校验）。
func (s *Service) CreateConversation(ctx context.Context, req dtoconversation.ConversationCreateReq) (*dtoconversation.ConversationCreateResp, error) {
	if s.orch == nil {
		return nil, code.FromError(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	// 会话与 turn 生命周期不随 HTTP 请求结束：WithoutCancel 继承 values 但脱离取消
	//（turn 由 Orchestrator 持有，取消走 POST /conversations/:id/cancel）。
	runCtx := context.WithoutCancel(ctx)
	agentID, err := s.resolveConversationAgent(req.AgentID)
	if err != nil {
		return nil, code.FromError(err)
	}
	// 先确认 agent 存在，再解析工作空间：解析可能**建目录**（代建），
	// 不该为一个本来就无效的请求留下文件系统副作用。
	if _, ok := s.AgentByID(agentID); !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "agent_not_found",
			fmt.Sprintf("未知 agent %q", agentID), nil))
	}
	convID := uuid.NewString()
	ws, err := ResolveWorkspace(req.Cwd, s.WorkspaceDir(), s.WorkspaceRoots(), convID)
	if err != nil {
		return nil, code.FromError(err)
	}
	// 能力选定（M3）：`capabilities` 未传 → nil，与引入本字段之前逐字节相同。
	// 传了（哪怕 `{}`）→ 声明了：两个 kind 各自 nil（不限制）或白名单，由 DTO 的指针区分。
	// 可达性校验在编排器内、**建连之前**做（校验不过一个副作用都不留：不建会话/不拉进程/不写库）。
	selection := capabilitySelectionFromReq(req.Capabilities)
	// 底座不由请求指定（构建期固定，见 pkg/runtime/build.go）：这里没有 runtime 参数要校验。
	res, err := s.startConversation(runCtx, agentID, StartOptions{
		ConversationID:     convID,
		Cwd:                ws.Path,
		WorkspaceTemporary: ws.Temporary,
		Model:              runtime.ModelConfig{Model: req.Model},
		Selection:          selection,
		Origin:             req.Origin,
	})
	if err != nil {
		// 会话未能建立 → 清理刚代建的目录（非临时工作空间与用户目录一律不动）。
		DiscardWorkspace(ws)
		return nil, code.FromError(err)
	}
	resp := &dtoconversation.ConversationCreateResp{
		ID: res.ConversationID, AgentID: res.AgentID, Status: res.Status, EventSeq: res.EventSeq,
		Cwd: ws.Path, WorkspaceTemporary: ws.Temporary,
	}
	if req.Prompt != "" {
		seq, err := s.orch.Prompt(runCtx, res.ConversationID, req.Prompt)
		if err != nil {
			return nil, code.FromError(err)
		}
		resp.EventSeq = seq
		resp.Status = ConversationStatusInProgress
	}
	return resp, nil
}

// startConversation 建会话。**底座不由调用方指定**：编排器内部取构建期选定的那一个
// （见 pkg/runtime/build.go 与 Orchestrator.StartConversation）。
func (s *Service) startConversation(ctx context.Context, agentID string, opt StartOptions) (*CreateResult, error) {
	return s.orch.StartConversation(ctx, agentID, opt)
}

// PromptConversation 向既有会话追加一轮 prompt（后台执行）。
// turn 生命周期同样脱离 HTTP 请求（WithoutCancel），结束/取消只由 agent 或
// POST /conversations/:id/cancel 决定。
func (s *Service) PromptConversation(ctx context.Context, id string, req dtoconversation.PromptReq) (*dtoconversation.ConversationPromptResp, error) {
	if s.orch == nil {
		return nil, code.FromError(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if id == "" || req.Prompt == "" {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "prompt_required", "conversation_id 与 prompt 必填", nil))
	}
	// 投递模式先校验：非法值不该走完 acquire 才发现（§6；空 = reject = 历史行为）。
	if _, err := NormalizeDelivery(req.Delivery); err != nil {
		return nil, code.FromError(err)
	}
	out, err := s.orch.PromptWithDelivery(context.WithoutCancel(ctx), id, req.Prompt, req.Delivery)
	if err != nil {
		return nil, code.FromError(err)
	}
	return &dtoconversation.ConversationPromptResp{
		ID: id, Status: ConversationStatusInProgress, EventSeq: out.EventSeq,
		Queued: out.Queued, InputID: out.InputID,
	}, nil
}

// DeleteQueuedInput 撤下一条尚未投递的排队输入（§6）。撤下后发 session.input_cancelled——
// 队列项的离队必须是一条可回放的事实（前端队列只认事件，不做本地推断）。
func (s *Service) DeleteQueuedInput(ctx context.Context, id, inputID string) (bool, error) {
	if id == "" || inputID == "" {
		return false, code.FromError(acl.NewError(acl.ClassRequest, "input_required", "conversation_id 与 input_id 必填", nil))
	}
	ok, err := s.deleteQueuedInput(ctx, id, inputID)
	if err != nil {
		return false, code.FromError(err)
	}
	if ok {
		agentID := ""
		if rec := s.conversationRec(id); rec != nil {
			agentID = rec.AgentID
		}
		s.publish(BusInputCancelled, id, map[string]any{
			"session_id": id, "agent_id": agentID, "input_id": inputID, "reason": ReasonUserCancel,
		})
	}
	return ok, nil
}

// CancelTurn 取消会话进行中的 turn（无 turn 时幂等 no-op）。
func (s *Service) CancelTurn(ctx context.Context, id string) error {
	if s.orch == nil {
		return code.FromError(acl.NewError(acl.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil))
	}
	if err := s.orch.CancelTurn(ctx, id); err != nil {
		return code.FromError(err)
	}
	return nil
}

// ConversationHistory 返回会话事件历史（seq 升序，after 向后 / before 向前）。
//
// 数据源：sqlite 模式（st != nil）读 events 表——durable、不受 eventbus 内存上限（65536）约束，
// 「加载更早」可一直翻到会话第一条；内存模式回放 eventbus（进程内）。
// 语义：after（开区间，seq > after）与 before（开区间，seq < before，返回最近 limit 条并升序排列）
// 互斥（handler 已拦，服务层再兜一次 → 40001）；has_more 沿当前方向；首尾 seq 一并回带。
func (s *Service) ConversationHistory(ctx context.Context, id string, q dtoconversation.HistoryQuery) (*dtoconversation.ConversationHistoryResp, error) {
	if !s.hasConversation(id) {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "conversation_not_found", fmt.Sprintf("未知会话 %q", id), nil))
	}
	if q.After > 0 && q.HasBefore {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "history_cursor_conflict",
			"after 与 before 互斥（一个向后、一个向前）", nil))
	}
	if q.After < 0 {
		q.After = 0
	}
	if q.Before < 0 {
		q.Before = 0
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
		var batch []store.Event
		var err error
		if q.HasBefore {
			batch, hasMore, err = s.st.EventsBefore(ctx, id, q.Before, int64(q.Limit))
		} else {
			batch, hasMore, err = s.st.EventsAfter(ctx, id, q.After, int64(q.Limit))
		}
		if err != nil {
			return nil, code.FromError(acl.NewError(acl.ClassService, "history_read_failed",
				fmt.Sprintf("读取会话历史失败：%v", err), nil))
		}
		events = make([]eventbus.Event, 0, len(batch))
		for _, e := range batch {
			events = append(events, eventbus.Event{
				Type: e.Type, Seq: e.Seq, AggregID: e.ConversationID, Payload: decodePayload(e.PayloadJSON),
			})
		}
	} else {
		all := s.bus.EventsAfter(0)
		mine := make([]eventbus.Event, 0, len(all))
		for _, evt := range all {
			if evt.AggregID != id {
				continue
			}
			if q.HasBefore {
				if q.Before > 0 && evt.Seq >= q.Before {
					continue
				}
			} else if evt.Seq <= q.After {
				continue
			}
			mine = append(mine, evt)
		}
		if q.HasBefore {
			// 取窗口末尾 limit 条（保持升序）；窗口外还有更早 → has_more。
			if len(mine) > q.Limit {
				hasMore = true
				mine = mine[len(mine)-q.Limit:]
			}
		} else if len(mine) > q.Limit {
			hasMore = true
			mine = mine[:q.Limit]
		}
		events = mine
	}

	resp := &dtoconversation.ConversationHistoryResp{ID: id, Events: events, HasMore: hasMore}
	if len(events) > 0 {
		resp.FirstSeq = events[0].Seq
		resp.LastSeq = events[len(events)-1].Seq
	}
	return resp, nil
}
