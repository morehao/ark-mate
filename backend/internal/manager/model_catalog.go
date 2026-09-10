package manager

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/handler"
)

// ——— 运行时派生的模型目录（决策记录 D2/D3）———

// 目录取样来源标注（对外契约字段 source）。
const (
	catalogFromSession = "session" // 直接读该 agent 的活跃会话
	catalogFromResume  = "resume"  // 恢复最近一条历史会话读取（读完即 close）
	catalogFromCache   = "cache"   // 内存缓存（TTL 内）
	catalogFromNone    = "none"    // 无可用来源（空目录）
)

// defaultCatalogTTL 目录缓存有效期。目录只在 new/resume/set 响应与 config_option_update
// 推送时变化，TTL 仅用于「没有活会话时避免反复 resume」；5 分钟与面板交互节奏匹配。
const defaultCatalogTTL = 5 * time.Minute

// rejectedValueTTL 被运行时拒绝的值（如凭证缺失）在目录里标注为不可用的时长。
const rejectedValueTTL = 10 * time.Minute

// catalogEntry 一次目录取样的结果（内存缓存，不落盘：任何一次真实会话都会刷新它）。
type catalogEntry struct {
	runtime   string
	options   []acl.ConfigOption
	fetchedAt time.Time
}

// rejectedValue 运行时拒绝过的一个取值（用于目录里的「不可用」标注）。
type rejectedValue struct {
	reason string
	at     time.Time
}

// catalogStore 目录缓存与拒绝值反馈（独立锁，避免与 s.mu / o.mu 交叉）。
type catalogStore struct {
	mu       sync.Mutex
	entries  map[string]*catalogEntry            // key = agentID
	rejected map[string]map[string]rejectedValue // key = agentID → value → 拒绝记录
}

func newCatalogStore() *catalogStore {
	return &catalogStore{
		entries:  map[string]*catalogEntry{},
		rejected: map[string]map[string]rejectedValue{},
	}
}

// put 记录一次目录取样（options 为空也记录：表示「运行时确实没给」）。
func (c *catalogStore) put(agentID, runtimeID string, options []acl.ConfigOption) {
	c.mu.Lock()
	c.entries[agentID] = &catalogEntry{runtime: runtimeID, options: cloneOptions(options), fetchedAt: time.Now().UTC()}
	c.mu.Unlock()
}

// get 取未过期缓存。
func (c *catalogStore) get(agentID string, ttl time.Duration) (*catalogEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[agentID]
	if !ok || len(e.options) == 0 {
		return nil, false
	}
	if ttl > 0 && time.Since(e.fetchedAt) > ttl {
		return nil, false
	}
	out := &catalogEntry{runtime: e.runtime, options: cloneOptions(e.options), fetchedAt: e.fetchedAt}
	return out, true
}

// markRejected 记录一次「运行时拒绝了该值」（按 agent 维度）。
// 只在运行时明确拒绝（invalidParams 类）时调用；网络/超时等暂态失败不在此列。
func (c *catalogStore) markRejected(agentID, value, reason string) {
	if agentID == "" || value == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.rejected[agentID]
	if m == nil {
		m = map[string]rejectedValue{}
		c.rejected[agentID] = m
	}
	m[value] = rejectedValue{reason: reason, at: time.Now()}
}

// rejection 取某值最近的拒绝记录（过期 → 无）。
func (c *catalogStore) rejection(agentID, value string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.rejected[agentID]
	if m == nil {
		return "", false
	}
	r, ok := m[value]
	if !ok {
		return "", false
	}
	if time.Since(r.at) > rejectedValueTTL {
		delete(m, value)
		return "", false
	}
	return r.reason, true
}

// clearRejection 运行时接受了该值时清除拒绝标记（自愈）。
func (c *catalogStore) clearRejection(agentID, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m := c.rejected[agentID]; m != nil {
		delete(m, value)
	}
}

// cloneOptions 深拷贝选项（含每个选项的值切片），避免缓存与调用方共享底层数组。
func cloneOptions(in []acl.ConfigOption) []acl.ConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]acl.ConfigOption, len(in))
	for i, o := range in {
		out[i] = o
		if len(o.Values) > 0 {
			out[i].Values = append([]acl.ConfigOptionValue(nil), o.Values...)
		}
	}
	return out
}

// DefaultAgentID 返回默认 agent（对外契约：面板未指定 agent 时用它）。
func (s *Service) DefaultAgentID() string { return defaultAgentID }

// ModelCatalog 返回该 agent 的运行时模型目录。
//
// 取样顺序（决策记录 D2；**任何一支都不新建会话**）：
//  1. session —— 该 agent 有活跃会话：直接读它的配置项（运行时自报，零额外开销）；
//  2. cache   —— 内存缓存（TTL 内）；
//  3. resume  —— 取该 agent 最近一条带原生会话 id 的历史会话，resume 读配置项后 close；
//  4. none    —— 都没有：空目录 + 说明（面板此时显示「跟随 Agent 默认」）。
//
// 目录为空是合法结果（运行时没配 provider/没登录），不做任何回落填充。
func (s *Service) ModelCatalog(ctx context.Context, agentID string) (*handler.ModelCatalogResp, error) {
	agent, ok := s.AgentByID(agentID)
	if !ok {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "agent_not_found",
			"[agent_not_found] 未知 agent "+agentID, nil))
	}
	spec, ok := s.reg.Get(agent.pickRuntime())
	if !ok {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "runtime_not_registered",
			"[runtime_not_registered] agent "+agentID+" 的 runtime 未注册", nil))
	}

	// orch 为 nil = 纯列表装配（测试/无执行面）：退化为空目录，不做任何拨号。
	if s.orch == nil {
		return s.catalogResp(agentID, spec.ID, catalogFromNone, nil, time.Time{},
			"本进程未启用执行面（无 Orchestrator），无法从运行时读取模型目录。"), nil
	}

	// 1) 活会话优先：该会话的配置项就是运行时此刻的真实状态。
	if opts, ok := s.orch.liveConfigOptions(agentID); ok {
		s.catalog.put(agentID, spec.ID, opts)
		return s.catalogResp(agentID, spec.ID, catalogFromSession, opts, time.Now().UTC(), ""), nil
	}

	// 2) 缓存。
	if e, ok := s.catalog.get(agentID, defaultCatalogTTL); ok {
		return s.catalogResp(agentID, e.runtime, catalogFromCache, e.options, e.fetchedAt, ""), nil
	}

	// 3) resume 历史会话读取（只读取样：读完立即 close，不留下任何新会话）。
	if opts, err := s.fetchCatalogByResume(ctx, agent, spec); err == nil && len(opts) > 0 {
		s.catalog.put(agentID, spec.ID, opts)
		return s.catalogResp(agentID, spec.ID, catalogFromResume, opts, time.Now().UTC(), ""), nil
	} else if err != nil {
		s.logCatalogFallback(agentID, spec.ID, err)
	}

	// 4) 无来源：空目录 + 运行时鉴权引导。
	return s.catalogResp(agentID, spec.ID, catalogFromNone, nil, time.Time{},
		"运行时尚未提供模型目录：该 agent 还没有会话可供读取，且没有缓存。发送一条消息后目录会自动补全。"), nil
}

// fetchCatalogByResume 用「恢复一条已有会话 → 读配置项 → close」取目录。
// 不新建会话（session/new 会在两家 runtime 里落一条持久记录，且都没有 delete 能力）。
//
// 候选会话取两处（都是**已有**会话，按可靠性排序）：
//  1. 本平台记录（内存/sqlite）里该 agent 最近一条带原生 id 的会话；
//  2. 兜底：运行时的 ACP `session/list`（跨客户端可见，如用户此前在终端里跑过的会话）——
//     仅在平台无记录时用，且优先带绝对 cwd 的条目；dsh 会主动排除仍在活跃的会话，
//     所以不会抢用户终端里正在用的那条。
func (s *Service) fetchCatalogByResume(ctx context.Context, agent *AgentRec, spec runtime.Spec) ([]acl.ConfigOption, error) {
	rc, err := s.orch.acquire(ctx, agent, spec)
	if err != nil {
		return nil, err
	}
	caps := rc.client.Capabilities()
	if !caps.SessionResume && !caps.Raw.LoadSession {
		return nil, acl.NewError(acl.ClassUnsupported, "resume_session_unsupported",
			"该 runtime 不支持 session/resume|session/load，无法在不新建会话的前提下读取目录", nil)
	}

	nativeID, cwd := "", ""
	if rec := s.newestResumableConversation(agent.ID); rec != nil {
		nativeID, cwd = rec.ProviderSessionID, rec.Cwd
	} else if items, err := rc.client.ListSessions(ctx, ""); err == nil {
		if pick, ok := pickListedSession(items); ok {
			nativeID, cwd = pick.ID, pick.Cwd
		}
	}
	if nativeID == "" {
		return nil, acl.NewError(acl.ClassRequest, "no_resumable_session",
			"该 agent 既无平台会话记录，runtime 也没有可列举的会话", nil)
	}

	var sess *acl.Session
	if caps.Raw.LoadSession {
		sess, err = rc.client.LoadSession(ctx, nativeID, acl.SessionOptions{Cwd: cwd})
	} else {
		sess, err = rc.client.ResumeSession(ctx, nativeID, acl.SessionOptions{Cwd: cwd})
	}
	if err != nil {
		return nil, err
	}
	// 取样用完即释放（close 只解除活跃态，不删该会话的持久记录）。
	defer func() {
		if cerr := sess.Close(context.WithoutCancel(ctx)); cerr != nil {
			slog.Default().Debug("目录取样会话 close 失败（忽略）", "agent", agent.ID, "err", cerr)
		}
	}()
	return sess.ConfigOptions(), nil
}

// pickListedSession 从 session/list 结果里挑一条可恢复的会话：优先带绝对 cwd 的条目
// （resume 需要 cwd），runtime 大多按最近活跃倒序返回（dsh 明确如此），故取首个。
func pickListedSession(items []acl.SessionInfo) (acl.SessionInfo, bool) {
	for _, it := range items {
		if it.ID != "" && strings.HasPrefix(it.Cwd, "/") {
			return it, true
		}
	}
	return acl.SessionInfo{}, false
}

// newestResumableConversation 找该 agent 最近一条可用于 resume 的会话（有原生 id + cwd）。
// 内存集合优先（活跃/本进程可见），sqlite 兜底（重启后历史）。
func (s *Service) newestResumableConversation(agentID string) *ConversationRec {
	s.mu.Lock()
	var best *ConversationRec
	for _, r := range s.conversations {
		if r.AgentID != agentID || r.ProviderSessionID == "" || r.Cwd == "" {
			continue
		}
		if best == nil || r.UpdatedAt.After(best.UpdatedAt) || (r.UpdatedAt.Equal(best.UpdatedAt) && r.ID > best.ID) {
			best = r
		}
	}
	st := s.st
	s.mu.Unlock()
	if best != nil {
		return best
	}
	if st == nil {
		return nil
	}
	// sqlite 兜底：列出全部会话后挑该 agent 最近的（表量级小；失败即视为无可恢复会话）。
	list, err := st.ListConversations(context.Background(), nil)
	if err != nil {
		return nil
	}
	var out *ConversationRec
	for i := range list {
		c := list[i]
		if c.AgentID != agentID || c.ProviderSessionID == "" || c.Cwd == "" {
			continue
		}
		rec := &ConversationRec{ID: c.ID, AgentID: c.AgentID, Cwd: c.Cwd,
			ProviderSessionID: c.ProviderSessionID, UpdatedAt: c.UpdatedAt}
		if out == nil || rec.UpdatedAt.After(out.UpdatedAt) {
			out = rec
		}
	}
	return out
}

// catalogResp 组装对外响应：选项投影 + 拒绝值标注 + 鉴权引导。
func (s *Service) catalogResp(agentID, runtimeID, source string, opts []acl.ConfigOption,
	fetchedAt time.Time, note string) *handler.ModelCatalogResp {
	resp := &handler.ModelCatalogResp{
		Agent:   agentID,
		Runtime: runtimeID,
		Source:  source,
		Options: make([]handler.ConfigOptionResp, 0, len(opts)),
		Note:    note,
	}
	if !fetchedAt.IsZero() {
		resp.FetchedAt = fetchedAt.UTC().Format(time.RFC3339)
	}
	for _, o := range opts {
		item := handler.ConfigOptionResp{
			ID:           o.ID,
			Name:         o.Name,
			Description:  o.Description,
			Category:     o.Category,
			Type:         o.Type,
			CurrentValue: o.CurrentValue,
			Grouped:      o.Grouped,
			Values:       make([]handler.ConfigOptionValueResp, 0, len(o.Values)),
		}
		for _, v := range o.Values {
			val := handler.ConfigOptionValueResp{
				Value:       v.Value,
				Name:        v.Name,
				Description: v.Description,
				Group:       v.Group,
				GroupName:   v.GroupName,
			}
			if reason, bad := s.catalog.rejection(agentID, v.Value); bad {
				val.Unavailable = true
				val.UnavailableReason = reason
			}
			item.Values = append(item.Values, val)
		}
		resp.Options = append(resp.Options, item)
	}
	// 鉴权引导取自连接能力（需要握手过的连接；未连接时为空，面板按 Note 提示）。
	resp.AuthGuidance = s.authGuidanceFor(agentID)
	return resp
}

// authGuidanceFor 取该 agent 已连接 runtime 声明的鉴权方式（无连接 → 空）。
func (s *Service) authGuidanceFor(agentID string) []handler.AuthGuidanceResp {
	if s.orch == nil {
		return nil
	}
	rc := s.orch.connForAgent(agentID)
	if rc == nil {
		return nil
	}
	caps := rc.client.Capabilities()
	if len(caps.AuthGuidance) == 0 {
		return nil
	}
	out := make([]handler.AuthGuidanceResp, 0, len(caps.AuthGuidance))
	for _, g := range caps.AuthGuidance {
		out = append(out, handler.AuthGuidanceResp{
			MethodID:    g.MethodID,
			Name:        g.Name,
			Description: g.Description,
			Terminal:    g.Terminal,
			Command:     g.Command,
		})
	}
	return out
}

// logCatalogFallback 目录降级留痕（resume 取样失败不阻塞接口：返回空目录由面板提示）。
func (s *Service) logCatalogFallback(agentID, runtimeID string, err error) {
	if ae, ok := acl.As(err); ok && ae != nil {
		slog.Default().Debug("模型目录 resume 取样失败", "agent", agentID, "runtime", runtimeID,
			"code", ae.Code, "class", string(ae.Class), "err", ae.Details)
		return
	}
	slog.Default().Debug("模型目录 resume 取样失败", "agent", agentID, "runtime", runtimeID, "err", err)
}
