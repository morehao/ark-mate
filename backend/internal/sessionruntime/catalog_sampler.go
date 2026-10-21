package sessionruntime

import (
	"context"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/infra/protocol/acp"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// ——— 目录取样的**编排侧适配器**（P5a 取样面）———
//
// 目录的逻辑（顺序 / 缓存 / 空目录说明 / 拒绝值标注）已搬进 `domain/provider`；本文件只实现
// 那三个窄端口，因为"哪条会话值得试、连接从哪来"是编排/执行面的事：
//
//	LiveConfigOptions      → 活跃会话的配置项（Runner 的运行期状态）
//	SampleConfigOptions    → 候选枚举 + 逐条退避（每条由 `runtimeconn.SampleConfigOptions` 执行）
//	AuthGuidance / 收图能力 → 连接期能力事实（连接池的活客户端）
//
// 一条判据不变：**任何一支都不新建会话**（session/new 会落持久记录，而两家 runtime 都没有 delete）。

// defaultCatalogSampleAttempts 一次目录取样最多试几条候选会话（够跳过最近一两条坏数据，
// 又让最坏情况有界；目录取数在面板打开/会话变化时触发，不是轮询）。
const maxCatalogSampleAttempts = 3

// LiveConfigOptions 实现 `provider.LiveConfigOptionsSource`。
func (s *Service) LiveConfigOptions() ([]acp.ConfigOption, bool) {
	if s.orch == nil {
		return nil, false
	}
	return s.orch.liveConfigOptions()
}

// AuthGuidance 实现 `provider.CatalogConnectionFacts`。
//
// 连接按 **runtime** 找（不是按 agent）：单底座下"这条连接属于谁"没有第二个候选。
func (s *Service) AuthGuidance() []provider.AuthGuide {
	if s.orch == nil {
		return nil
	}
	rc := s.runtimePool.ConnForRuntime(runtime.SelectedRuntime())
	if rc == nil {
		return nil
	}
	caps := rc.Client.Capabilities()
	if len(caps.AuthGuidance) == 0 {
		return nil
	}
	out := make([]provider.AuthGuide, 0, len(caps.AuthGuidance))
	for _, g := range caps.AuthGuidance {
		out = append(out, provider.AuthGuide{
			MethodID:    g.MethodID,
			Name:        g.Name,
			Description: g.Description,
			Terminal:    g.Terminal,
			Command:     g.Command,
		})
	}
	return out
}

// PromptImageSupported 实现 `provider.CatalogConnectionFacts`。
//
// 取不到（没有活跃连接）时回 nil 而不是 false：ACP 的 `promptCapabilities.image` 只有握手过
// 才知道，把"没握手"说成"不支持"正是 ADR-14 要拦的那类错误结论。
func (s *Service) PromptImageSupported() *bool {
	if s.orch == nil {
		return nil
	}
	rc := s.runtimePool.ConnForRuntime(runtime.SelectedRuntime())
	if rc == nil {
		return nil
	}
	v := rc.Client.Capabilities().Images
	return &v
}

// SampleConfigOptions 实现 `provider.CatalogSampler`：枚举候选、逐条退避取样。
func (s *Service) SampleConfigOptions(ctx context.Context) ([]acp.ConfigOption, bool, error) {
	if s.orch == nil {
		return nil, false, provider.ErrNoSampleCandidate
	}
	spec, ok := s.planes.Registry.Get(runtime.SelectedRuntime())
	if !ok {
		return nil, false, failure.NewError(failure.ClassRequest, "runtime_not_registered",
			"[runtime_not_registered] 构建期选定的 runtime 未装配", nil)
	}
	// 目录探测不绑定任何会话，按缺省代际复用连接（不额外起进程）。原生模式同样走缺省（空 =
	// 不注入）——目录探测不需要沙箱边界，用缺省那一档才可能命中已有的暖进程。
	rc, err := s.orch.acquire(ctx, spec, "", "")
	if err != nil {
		return nil, false, err
	}
	caps := rc.Client.Capabilities()
	if !caps.SessionResume && !caps.Raw.LoadSession {
		return nil, false, failure.NewError(failure.ClassUnsupported, "resume_session_unsupported",
			"该 runtime 不支持 session/resume|session/load，无法在不新建会话的前提下读取目录", nil)
	}

	cands := s.catalogResumeCandidates(ctx, rc)
	if len(cands) > maxCatalogSampleAttempts {
		cands = cands[:maxCatalogSampleAttempts]
	}
	// 逐条退避：最近那条读不通就试下一条。**没有候选时**返回 `ErrNoSampleCandidate`
	// （正常状态：第一条消息发送前就是这样），说明文案据此与"读不通"分开。
	return sampleCandidates(cands, func(t resumeTarget) ([]acp.ConfigOption, error) {
		return runtimeconn.SampleConfigOptions(ctx, rc, t.RawID, t.Cwd)
	})
}

// resumeTarget 一条候选会话（底座的**原文** id + cwd）。
//
// 两个字段都只出现在 ACP 调用参数里：平台侧的值（`rt_` 壳）不进这里——同名不同物是这一带
// 最容易踩的一处，写错不响（守卫② 事后才发现）。
type resumeTarget struct {
	RawID string
	Cwd   string
}

// sampleCandidates 逐条候选尝试取样，返回**第一个非空**结果；顺序由调用方给（最近的在前）。
//
// 三态：opts 非空；sampled=true（读通但零候选）；sampled=false + err（全部读不通）。
// 没有候选时返回 `provider.ErrNoSampleCandidate`，而不是把"没有候选"讲成"取样失败"。
func sampleCandidates[T any](cands []T, sample func(T) ([]acp.ConfigOption, error)) ([]acp.ConfigOption, bool, error) {
	var lastErr error
	sampled := false
	for _, c := range cands {
		opts, err := sample(c)
		if err != nil {
			lastErr = err
			continue
		}
		sampled = true
		if len(opts) > 0 {
			return opts, true, nil
		}
	}
	if sampled {
		return nil, true, nil
	}
	if lastErr == nil {
		lastErr = provider.ErrNoSampleCandidate
	}
	return nil, false, lastErr
}

// catalogResumeCandidates 列出可用于取样的**已有**会话，最近的在前。
//
// 两处来源（任何一支都不新建会话）：
//  1. 本平台记录（内存集合 + sqlite）里带原生 id + cwd 的会话，按 UpdatedAt 倒序；
//  2. 兜底：运行时的 ACP `session/list`（跨客户端可见）——仅在平台无记录时用，且优先带绝对
//     cwd 的条目；dsh 会主动排除仍在活跃的会话，所以不会抢用户终端里正在用的那条。
func (s *Service) catalogResumeCandidates(ctx context.Context, rc *runtimeconn.Conn) []resumeTarget {
	// 1) 平台记录（内存集合 + sqlite 兜底）。
	st := s.planes.Resources.Store()
	var recs []session.SessionRec
	for _, r := range s.sessionPlane.Snapshot() {
		if r.RuntimeSessionID == "" || r.Cwd == "" {
			continue
		}
		recs = append(recs, r)
	}
	if st != nil {
		// 表量级小；失败即视为"这一侧没有候选"（内存集合那边照常可用）。
		if list, err := st.ListSessions(context.Background(), nil); err == nil {
			for i := range list {
				c := list[i]
				if c.RuntimeSessionID == "" || c.Cwd == "" {
					continue
				}
				recs = append(recs, session.SessionRec{ID: c.ID, Cwd: c.Cwd,
					RuntimeSessionID: c.RuntimeSessionID, UpdatedAt: c.UpdatedAt})
			}
		}
	}
	if targets := resumeTargetsFrom(ctx, recs, nil); len(targets) > 0 {
		return targets
	}

	// 2) 兜底：底座的 session/list（顺序即底座给的"最近优先"）。
	items, err := rc.Client.ListSessions(ctx, "")
	if err != nil {
		return nil
	}
	return resumeTargetsFrom(ctx, nil, items)
}

// resumeTargetsFrom 把两处来源折成候选列表（无 ACP 依赖，顺序/跳过两条判据可单测）。
func resumeTargetsFrom(ctx context.Context, recs []session.SessionRec, listed []acp.SessionInfo) []resumeTarget {
	targets := make([]resumeTarget, 0, 4)
	seen := map[string]bool{}
	add := func(rawID, cwd string) {
		if rawID == "" || cwd == "" || seen[rawID] {
			return
		}
		seen[rawID] = true
		targets = append(targets, resumeTarget{RawID: rawID, Cwd: cwd})
	}

	// 不按底座过滤：单底座下所有会话都在同一个 runtime 上，"谁的会话"没有第二个答案。
	recs = append([]session.SessionRec(nil), recs...)
	sort.SliceStable(recs, func(i, j int) bool {
		if !recs[i].UpdatedAt.Equal(recs[j].UpdatedAt) {
			return recs[i].UpdatedAt.After(recs[j].UpdatedAt)
		}
		return recs[i].ID > recs[j].ID
	})
	for i := range recs {
		raw, uerr := objid.UnwrapRuntime(recs[i].RuntimeSessionID)
		if uerr != nil {
			glog.Debugw(ctx, "目录取样跳过一条壳不合法的会话记录",
				"session", recs[i].ID, "err", uerr)
			continue
		}
		add(raw, recs[i].Cwd)
	}
	if len(targets) > 0 {
		return targets
	}
	if pick, ok := pickListedSession(listed); ok {
		add(pick.ID, pick.Cwd)
	}
	return targets
}

// pickListedSession 从 session/list 结果里挑一条可恢复的会话：优先带绝对 cwd 的条目
// （resume 需要 cwd），runtime 大多按最近活跃倒序返回（dsh 明确如此），故取首个。
func pickListedSession(items []acp.SessionInfo) (acp.SessionInfo, bool) {
	for _, it := range items {
		if it.ID != "" && strings.HasPrefix(it.Cwd, "/") {
			return it, true
		}
	}
	return acp.SessionInfo{}, false
}
