package manager

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// ——— 运行时派生的模型目录（决策记录 D2/D3）———

// 目录取样来源标注（对外契约字段 source）。
const (
	catalogFromSession = "session" // 直接读该 runtime 的活跃会话
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
//
// 键是 **runtime id**（v19 之前是 agent id）：单底座下它只有一个取值，但保留这一维让
// "这条目录是谁说的"在缓存里显式可见——换成裸单例会让将来多底座时又多一处要改的隐式前提。
type catalogStore struct {
	mu       sync.Mutex
	entries  map[string]*catalogEntry            // key = runtimeID
	rejected map[string]map[string]rejectedValue // key = runtimeID → value → 拒绝记录
}

func newCatalogStore() *catalogStore {
	return &catalogStore{
		entries:  map[string]*catalogEntry{},
		rejected: map[string]map[string]rejectedValue{},
	}
}

// put 记录一次目录取样（options 为空也记录：表示「运行时确实没给」）。
func (c *catalogStore) put(runtimeID string, options []acl.ConfigOption) {
	c.mu.Lock()
	c.entries[runtimeID] = &catalogEntry{runtime: runtimeID, options: cloneOptions(options), fetchedAt: time.Now().UTC()}
	c.mu.Unlock()
}

// get 取未过期缓存。
func (c *catalogStore) get(runtimeID string, ttl time.Duration) (*catalogEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[runtimeID]
	if !ok || len(e.options) == 0 {
		return nil, false
	}
	if ttl > 0 && time.Since(e.fetchedAt) > ttl {
		return nil, false
	}
	out := &catalogEntry{runtime: e.runtime, options: cloneOptions(e.options), fetchedAt: e.fetchedAt}
	return out, true
}

// markRejected 记录一次「运行时拒绝了该值」（按 runtime 维度）。
// 只在运行时明确拒绝（invalidParams 类）时调用；网络/超时等暂态失败不在此列。
func (c *catalogStore) markRejected(runtimeID, value, reason string) {
	if runtimeID == "" || value == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.rejected[runtimeID]
	if m == nil {
		m = map[string]rejectedValue{}
		c.rejected[runtimeID] = m
	}
	m[value] = rejectedValue{reason: reason, at: time.Now()}
}

// rejection 取某值最近的拒绝记录（过期 → 无）。
func (c *catalogStore) rejection(runtimeID, value string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.rejected[runtimeID]
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
func (c *catalogStore) clearRejection(runtimeID, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m := c.rejected[runtimeID]; m != nil {
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

// ModelCatalog 返回该 runtime 的运行时模型目录。
//
// 取样顺序（决策记录 D2；**任何一支都不新建会话**）：
//  1. session —— 该 runtime 有活跃会话：直接读它的配置项（运行时自报，零额外开销）；
//  2. cache   —— 内存缓存（TTL 内）；
//  3. resume  —— 取最近一条带原生会话 id 的历史会话，resume 读配置项后 close；
//  4. none    —— 都没有：空目录 + 说明（面板此时显示「跟随运行时默认」）。
//
// 目录为空是合法结果（运行时没配 provider/没登录），不做任何回落填充。
func (s *Service) ModelCatalog(ctx context.Context) (*dtomodel.ModelCatalogResp, error) {
	spec, ok := s.reg.Get(runtime.SelectedRuntime())
	if !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "runtime_not_registered",
			"[runtime_not_registered] 构建期选定的 runtime 未装配", nil))
	}

	// orch 为 nil = 纯列表装配（测试/无执行面）：退化为空目录，不做任何拨号。
	if s.orch == nil {
		return s.catalogResp(ctx, spec.ID, catalogFromNone, nil, time.Time{},
			"本进程未启用执行面（无 Orchestrator），无法从运行时读取模型目录。"), nil
	}

	// 1) 活会话优先：该会话的配置项就是运行时此刻的真实状态。
	if opts, ok := s.orch.liveConfigOptions(); ok {
		s.catalog.put(spec.ID, opts)
		return s.catalogResp(ctx, spec.ID, catalogFromSession, opts, time.Now().UTC(), ""), nil
	}

	// 2) 缓存。
	if e, ok := s.catalog.get(spec.ID, defaultCatalogTTL); ok {
		return s.catalogResp(ctx, e.runtime, catalogFromCache, e.options, e.fetchedAt, ""), nil
	}

	// 3) resume 历史会话读取（只读取样：读完立即 close，不留下任何新会话）。
	if opts, err := s.fetchCatalogByResume(ctx, spec); err == nil && len(opts) > 0 {
		s.catalog.put(spec.ID, opts)
		return s.catalogResp(ctx, spec.ID, catalogFromResume, opts, time.Now().UTC(), ""), nil
	} else if err != nil {
		s.logCatalogFallback(spec.ID, err)
	}

	// 4) 无来源：空目录 + 运行时鉴权引导。
	return s.catalogResp(ctx, spec.ID, catalogFromNone, nil, time.Time{},
		"运行时尚未提供模型目录：还没有会话可供读取，且没有缓存。发送一条消息后目录会自动补全。"), nil
}

// fetchCatalogByResume 用「恢复一条已有会话 → 读配置项 → close」取目录。
// 不新建会话（session/new 会在两家 runtime 里落一条持久记录，且都没有 delete 能力）。
//
// 候选会话取两处（都是**已有**会话，按可靠性排序）：
//  1. 本平台记录（内存/sqlite）里最近一条带原生 id 的会话；
//  2. 兜底：运行时的 ACP `session/list`（跨客户端可见，如用户此前在终端里跑过的会话）——
//     仅在平台无记录时用，且优先带绝对 cwd 的条目；dsh 会主动排除仍在活跃的会话，
//     所以不会抢用户终端里正在用的那条。
func (s *Service) fetchCatalogByResume(ctx context.Context, spec runtime.Spec) ([]acl.ConfigOption, error) {
	// 目录探测不绑定任何会话，按缺省代际复用连接（不额外起进程）。
	rc, err := s.orch.acquire(ctx, spec, "")
	if err != nil {
		return nil, err
	}
	caps := rc.client.Capabilities()
	if !caps.SessionResume && !caps.Raw.LoadSession {
		return nil, acl.NewError(acl.ClassUnsupported, "resume_session_unsupported",
			"该 runtime 不支持 session/resume|session/load，无法在不新建会话的前提下读取目录", nil)
	}

	// **两个名字，两种语义**（本文件最容易踩的一处）：同一个变量既"给底座用"又"比较/落库用"
	// 时，谁把解包后的值写回 rec 就会把裸 id 落库，而这件事不响（守卫② 事后才发现）。
	//   rawID     = 底座原文，**只**出现在 ACP 调用参数里；
	//   wrappedID = 平台侧的值（`rt_` 壳），用于判空与（若将来落 rec）持久化。
	var rawID, wrappedID, cwd string
	if rec := s.newestResumableSession(); rec != nil {
		wrappedID, cwd = rec.RuntimeSessionID, rec.Cwd
		raw, uerr := objid.UnwrapRuntime(wrappedID)
		if uerr != nil {
			return nil, uerr
		}
		rawID = raw
	} else if items, err := rc.client.ListSessions(ctx, ""); err == nil {
		if pick, ok := pickListedSession(items); ok {
			// W3：`session/list` 直出**底座原文**（第二个最容易漏的点），必须包装后才是平台侧的值。
			wrapped, werr := objid.WrapRuntime(pick.ID)
			if werr != nil {
				return nil, werr
			}
			rawID, wrappedID, cwd = pick.ID, wrapped, pick.Cwd
		}
	}
	if wrappedID == "" {
		return nil, acl.NewError(acl.ClassRequest, "no_resumable_session",
			"平台既无会话记录，runtime 也没有可列举的会话", nil)
	}

	var sess *acl.Session
	if caps.Raw.LoadSession {
		sess, err = rc.client.LoadSession(ctx, rawID, acl.SessionOptions{Cwd: cwd})
	} else {
		sess, err = rc.client.ResumeSession(ctx, rawID, acl.SessionOptions{Cwd: cwd})
	}
	if err != nil {
		return nil, err
	}
	// 取样用完即释放（close 只解除活跃态，不删该会话的持久记录）。
	defer func() {
		if cerr := sess.Close(context.WithoutCancel(ctx)); cerr != nil {
			glog.Debugw(ctx, "目录取样会话 close 失败（忽略）", "runtime", spec.ID, "err", cerr)
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

// newestResumableSession 找最近一条可用于 resume 的会话（有原生 id + cwd）。
// 内存集合优先（活跃/本进程可见），sqlite 兜底（重启后历史）。
//
// 不按底座过滤：单底座下所有会话都在同一个 runtime 上，"谁的会话"这个问题没有第二个答案。
func (s *Service) newestResumableSession() *SessionRec {
	s.mu.Lock()
	var best *SessionRec
	for _, r := range s.sessions {
		if r.RuntimeSessionID == "" || r.Cwd == "" {
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
	// sqlite 兜底：列出全部会话后挑最近的一条（表量级小；失败即视为无可恢复会话）。
	list, err := st.ListSessions(context.Background(), nil)
	if err != nil {
		return nil
	}
	var out *SessionRec
	for i := range list {
		c := list[i]
		if c.RuntimeSessionID == "" || c.Cwd == "" {
			continue
		}
		rec := &SessionRec{ID: c.ID, Cwd: c.Cwd,
			RuntimeSessionID: c.RuntimeSessionID, UpdatedAt: c.UpdatedAt}
		if out == nil || rec.UpdatedAt.After(out.UpdatedAt) {
			out = rec
		}
	}
	return out
}

// modelIDByValue 算"该 runtime 的不透明值 → 平台模型行 id"的注解表（v26，定案「辰」）。
//
// 比对的是**派生值对派生值**：对每条平台行算 `modelValueFor`，再与候选逐字比。所以平台
// **不解析**底座词表（这与"平台不猜底座形状"是同一条纪律），只是把同一份生成规则跑了两处。
//
// 多命中按 **fail-closed**（不收录）：两条平台行派生出同一个运行时值时，这个值指不到确定的一行
// ——随便挑一条就是"静默选错模型"，而它正是这一版最该防的失败。零命中 = 平台没声明过它。
//
// memory 模式（没有平台清单载体）返回 nil：所有候选都"未声明"，这是**正确结论**而不是失败。
func (s *Service) modelIDByValue(ctx context.Context, runtimeID string) map[string]string {
	if s.st == nil {
		return nil
	}
	rows, err := s.st.ListModelDefinitions(ctx)
	if err != nil {
		glog.Warnw(ctx, "读平台模型清单失败（本次目录候选不带平台注解，客户端因此选不了它们）",
			"runtime", runtimeID, "err", err)
		return nil
	}
	seen := make(map[string]string, len(rows))
	dup := map[string]bool{}
	for i := range rows {
		v, verr := modelValueFor(s.reg, runtimeID, rows[i].ProviderID, rows[i].Model)
		if verr != nil || v == "" {
			// 该底座不消费平台清单 / 这一行编不出值：它没有可注解的候选。
			continue
		}
		if _, ok := seen[v]; ok {
			dup[v] = true
			continue
		}
		seen[v] = rows[i].ID
	}
	for v := range dup {
		delete(seen, v)
	}
	return seen
}

// catalogResp 组装对外响应：选项投影 + 拒绝值标注 + 鉴权引导。
func (s *Service) catalogResp(ctx context.Context, runtimeID, source string, opts []acl.ConfigOption,
	fetchedAt time.Time, note string) *dtomodel.ModelCatalogResp {
	// 注解表一次算好（逐候选查表，不逐候选扫清单）。
	byValue := s.modelIDByValue(ctx, runtimeID)
	resp := &dtomodel.ModelCatalogResp{
		Runtime: runtimeID,
		Source:  source,
		Options: make([]dtomodel.ConfigOptionResp, 0, len(opts)),
		Note:    note,
	}
	if !fetchedAt.IsZero() {
		resp.FetchedAt = fetchedAt.UTC().Format(time.RFC3339)
	}
	for _, o := range opts {
		item := dtomodel.ConfigOptionResp{
			ID:           o.ID,
			Name:         o.Name,
			Description:  o.Description,
			Category:     o.Category,
			Type:         o.Type,
			CurrentValue: o.CurrentValue,
			Grouped:      o.Grouped,
			Values:       make([]dtomodel.ConfigOptionValueResp, 0, len(o.Values)),
		}
		for _, v := range o.Values {
			val := dtomodel.ConfigOptionValueResp{
				Value:       v.Value,
				Name:        v.Name,
				Description: v.Description,
				Group:       v.Group,
				GroupName:   v.GroupName,
			}
			if reason, bad := s.catalog.rejection(runtimeID, v.Value); bad {
				val.Unavailable = true
				val.UnavailableReason = reason
			}
			// 平台行的注解（v26）：候选值若恰好等于某条平台行派生的运行时值，就带上那一行的 id。
			// 没有注解 = 平台没声明过它（客户端**选不了**它）——那正是"底座广告了、平台没声明"
			// 这条最该被看见的状态，界面上如实标出而不是悄悄过滤掉。
			val.ModelID = byValue[v.Value]
			item.Values = append(item.Values, val)
		}
		resp.Options = append(resp.Options, item)
	}
	// 鉴权引导取自连接能力（需要握手过的连接；未连接时为空，面板按 Note 提示）。
	resp.AuthGuidance = s.authGuidanceFor()
	// 「当前这条路由能不能收图」同样取自连接期事实（ADR-14）：取不到就是 nil（**不是 false**）
	// ——"没握手"与"握手了说不能"是两件事，前者只能记"无法校验"。
	resp.ImageInput = s.promptImageSupported()
	return resp
}

// authGuidanceFor 取已连接 runtime 声明的鉴权方式（无连接 → 空）。
//
// 连接按 **runtime** 找（不是按 agent）：单底座下"这条连接属于谁"没有第二个候选。
func (s *Service) authGuidanceFor() []dtomodel.AuthGuidanceResp {
	if s.orch == nil {
		return nil
	}
	rc := s.orch.connForRuntime(runtime.SelectedRuntime())
	if rc == nil {
		return nil
	}
	caps := rc.client.Capabilities()
	if len(caps.AuthGuidance) == 0 {
		return nil
	}
	out := make([]dtomodel.AuthGuidanceResp, 0, len(caps.AuthGuidance))
	for _, g := range caps.AuthGuidance {
		out = append(out, dtomodel.AuthGuidanceResp{
			MethodID:    g.MethodID,
			Name:        g.Name,
			Description: g.Description,
			Terminal:    g.Terminal,
			Command:     g.Command,
		})
	}
	return out
}

// promptImageSupported 「当前这条路由能不能收图」的**连接期**事实（nil = 取不到）。
//
// 取不到（没有活跃连接）时回 nil 而不是 false：ACP 的 `promptCapabilities.image` 只有握手过
// 才知道，把"没握手"说成"不支持"正是 ADR-14 要拦的那类错误结论。
func (s *Service) promptImageSupported() *bool {
	if s.orch == nil {
		return nil
	}
	rc := s.orch.connForRuntime(runtime.SelectedRuntime())
	if rc == nil {
		return nil
	}
	v := rc.client.Capabilities().Images
	return &v
}

// logCatalogFallback 目录降级留痕（resume 取样失败不阻塞接口：返回空目录由面板提示）。
func (s *Service) logCatalogFallback(runtimeID string, err error) {
	if ae, ok := acl.As(err); ok && ae != nil {
		glog.Debugw(context.Background(), "模型目录 resume 取样失败", "runtime", runtimeID,
			"code", ae.Code, "class", string(ae.Class), "err", ae.Details)
		return
	}
	glog.Debugw(context.Background(), "模型目录 resume 取样失败", "runtime", runtimeID, "err", err)
}
