package manager

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/runtimeconn"
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

// maxCatalogSampleAttempts 一次目录取样最多试几条候选会话。
//
// 逐条退避是**必要的**（最近那条可能是坏数据，见 `sampleCandidates`），但代价是每条候选一次
// resume 往返——全都是坏数据时（例如路由键改名后整批旧会话都解析不了选择），退避会变成
// 每次取数都跑满一遍。取 3 是这两件事的折中：足够跳过"最近一条或两条恰好坏了"，
// 又让最坏情况有界（目录取数在面板打开/会话变化时触发，不是轮询）。
const maxCatalogSampleAttempts = 3

// ModelCatalog 返回该 runtime 的运行时模型目录。
//
// 取样顺序（决策记录 D2；**任何一支都不新建会话**）：
//  1. session —— 该 runtime 有活跃会话：直接读它的配置项（运行时自报，零额外开销）；
//  2. cache   —— 内存缓存（TTL 内）；
//  3. resume  —— 取最近几条带原生会话 id 的历史会话，**逐条退避**地 resume 读配置项后 close；
//  4. none    —— 都没读到：空目录 + 说明（面板此时显示「跟随运行时默认」）。
//
// 目录为空是合法结果（运行时没配 provider/没登录/没有"当前选中的模型"），不做任何回落填充。
//
// **空目录必须带说得通的说明**（v26.1）：同样的空结果至少有三种成因——"底座不支持读"、
// "读不到（取样失败）"、"读到了但底座一个候选都没广告"——它们对用户的下一步完全不同。
// 2026-10-15 实测那次"下拉里没有我已配置的模型"正是最后一种，而当时那句文案一口咬定
// "还没有会话可供读取"，把用户（和照着它排查的 agent）一起引向了完全错误的方向。
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
	//
	// 注意"有活会话"与"有候选"是两件事：会话在、但运行时没有选中任何模型时它报**零个**
	// 配置项——那也要给说明（自举闭环的第一态：还没发过消息 / 没设默认模型）。
	if opts, ok := s.orch.liveConfigOptions(); ok {
		if len(opts) == 0 {
			return s.catalogResp(ctx, spec.ID, catalogFromSession, nil, time.Time{},
				s.catalogEmptyNote(ctx, true, nil)), nil
		}
		s.providerPlane.PutConfigOptions(spec.ID, opts)
		return s.catalogResp(ctx, spec.ID, catalogFromSession, opts, time.Now().UTC(), ""), nil
	}

	// 2) 缓存。
	if e, ok := s.providerPlane.ConfigOptions(spec.ID, defaultCatalogTTL); ok {
		return s.catalogResp(ctx, e.Runtime, catalogFromCache, e.Options, e.FetchedAt, ""), nil
	}

	// 3) resume 历史会话读取（只读取样：读完立即 close，不留下任何新会话）。
	opts, sampled, err := s.fetchCatalogByResume(ctx, spec)
	if err == nil && len(opts) > 0 {
		s.providerPlane.PutConfigOptions(spec.ID, opts)
		return s.catalogResp(ctx, spec.ID, catalogFromResume, opts, time.Now().UTC(), ""), nil
	}
	if err != nil {
		s.logCatalogFallback(spec.ID, err)
	}

	// 4) 无候选：空目录 + 说明（成因不同，说明不同）。
	return s.catalogResp(ctx, spec.ID, catalogFromNone, nil, time.Time{},
		s.catalogEmptyNote(ctx, sampled, err)), nil
}

// catalogEmptyNote 组装"目录里一个候选都没有"的说明。
//
// `sampled` = 至少有一条候选会话读通了（空结果 = 运行时确实没广告候选）；
// `sampleErr` = 全部候选都读不通时的最后一个失败原因。
//
// 空的成因有五种，出路分四类（因为用户要做的事不同）：
//   - 平台还没声明任何模型   → 先去「设置 · 模型」添加（第 1 句）；
//   - 有候选但都读不通       → 如实说这次没读到，下一次会话动作会重新取样（第 2 句）；
//   - 读通了但零候选         → 运行时没有**选中的模型**，去设置里选一条默认模型（第 3 句）；
//   - 压根没有候选（没会话）/ 该底座不支持读 → 共用第 4 句（"发一条消息就会补全"）。
//
// 后两类共用一个出口是**有意的**：这两种成因给用户的出路是同一条（等一个会话把目录带出来），
// 差别只在留痕级别（`logCatalogFallback` 对它们都只打 Debug，不当故障报）。
//
// 第 2 句与第 3 句正是 2026-10-15 那次事故的两端：真实成因是第 3 种（底座读通了、但当时没有
// 选中的模型，于是它一个候选都不广告），而当时代码把第 2 与第 3 混成了一句、讲成了"还没有会话
// 可供读取"——用户照着那句去查会话，方向从一开始就错了。
func (s *Service) catalogEmptyNote(ctx context.Context, sampled bool, sampleErr error) string {
	declared := 0
	if rows, err := s.providerPlane.ListModels(ctx); err == nil {
		declared = len(rows)
	}
	switch {
	case declared == 0:
		return "运行时此刻没有广告任何模型候选，平台清单里也还没有模型：" +
			"先到「设置 · 模型」里添加供应商与模型，再选一条默认模型。"
	case sampleErr != nil && !errors.Is(sampleErr, errNoSampleCandidate) && !samplingUnsupported(sampleErr):
		return fmt.Sprintf("这次没能从运行时的历史会话里读到模型目录（取样失败）："+
			"平台已声明 %d 条模型，这次却一条候选都没读到。发送一条消息后目录会重新取样。", declared)
	case sampled:
		return fmt.Sprintf("平台已声明 %d 条模型，但运行时此刻没有选中的模型："+
			"到「设置 · 模型」里选一条默认模型（新会话会带上它），候选就会出现。", declared)
	default:
		return "运行时尚未提供模型目录：还没有会话可供读取，且没有缓存。发送一条消息后目录会自动补全。"
	}
}

// errNoSampleCandidate 连一条候选会话都没有（平台无记录、runtime 也列不出会话）。
//
// 它是一个**独立**的失败值而不是"取样失败"的一种：说明文案里这两种必须分开讲——
// "还没有会话可供读取"是可执行而正常的（发一条消息就有了），"取样失败"是这次真的读不通。
var errNoSampleCandidate = acl.NewError(acl.ClassRequest, "no_resumable_session",
	"平台既无会话记录，runtime 也没有可列举的会话", nil)

// samplingUnsupported 报告这次失败是"该底座不支持"（正常能力事实，不是故障）。
//
// 判据取 **Class** 而不是 code 前缀：这一类的 code 长这样——`resume_session_unsupported`、
// `list_sessions_unsupported`——后缀在尾不在头（我第一版按 `unsupported_` 前缀判，于是
// "不支持"被讲成了"取样失败"；是 `TestCatalogEmptyNoteTellsTheCauseApart` 逮住的）。
func samplingUnsupported(err error) bool {
	ae, ok := acl.As(err)
	return ok && ae != nil && ae.Class == acl.ClassUnsupported
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
// 返回三态（调用方据此分辨"底座确实没给"与"我们没读到"）：
//   - `opts` 非空 → 取样成功；
//   - `sampled=true, err=nil` → 至少一条候选读通了，但运行时一个候选都没广告；
//   - `sampled=false, err!=nil` → 所有候选都读不通（err 是**最后一次**的失败原因）。
//
// 为什么要逐条退避而不是只试最近一条：最近那条可能是坏数据（旧会话引用了底座已不再提供的
// 路由、会话文件损坏…），一条就把整个目录打成空，会让"下拉里什么都没有"与"平台没配模型"
// 长得一模一样——2026-10-15 的真实事故正是这个形状。
func sampleCandidates[T any](cands []T, sample func(T) ([]acl.ConfigOption, error)) ([]acl.ConfigOption, bool, error) {
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
		// 没有候选（调用方本该先判空）：不要把它伪装成"读不到"——说明文案要靠这个值把
		// "还没有会话可供读取"与"有会话但读不通"分开。
		lastErr = errNoSampleCandidate
	}
	return nil, false, lastErr
}

// fetchCatalogByResume 用「恢复已有会话 → 读配置项 → close」取目录：候选由
// `catalogResumeCandidates` 给，逐条退避由 `sampleCandidates` 做。
// 不新建会话（session/new 会在两家 runtime 里落一条持久记录，且都没有 delete 能力）。
func (s *Service) fetchCatalogByResume(ctx context.Context, spec runtime.Spec) ([]acl.ConfigOption, bool, error) {
	// 目录探测不绑定任何会话，按缺省代际复用连接（不额外起进程）。原生模式同样走缺省（空 =
	// 不注入）——目录探测不需要沙箱边界，用缺省那一档才可能命中已有的暖进程。
	rc, err := s.orch.acquire(ctx, spec, "", "")
	if err != nil {
		return nil, false, err
	}
	caps := rc.Client.Capabilities()
	if !caps.SessionResume && !caps.Raw.LoadSession {
		return nil, false, acl.NewError(acl.ClassUnsupported, "resume_session_unsupported",
			"该 runtime 不支持 session/resume|session/load，无法在不新建会话的前提下读取目录", nil)
	}

	cands := s.catalogResumeCandidates(ctx, rc)
	if len(cands) > maxCatalogSampleAttempts {
		cands = cands[:maxCatalogSampleAttempts]
	}

	// 逐条退避：最近那条读不通就试下一条（见 sampleCandidates 的注释）。**没有候选时也走这里**
	// ——"一条会话都没有"这个成因只在 `sampleCandidates` 里产出一次（`errNoSampleCandidate`）。
	// 这里原本自己早退并新造一个同码错误，于是"没有候选"在说明文案里被讲成了"取样失败"：
	// 同一个事实两处产出，是我在隔离环境跑端到端时才看见的（`testrig` 的桩底座不支持 resume，
	// 单测走不到这一支）。
	return sampleCandidates(cands, func(t resumeTarget) ([]acl.ConfigOption, error) {
		var sess *acl.Session
		var rerr error
		if caps.Raw.LoadSession {
			sess, rerr = rc.Client.LoadSession(ctx, t.RawID, acl.SessionOptions{Cwd: t.Cwd})
		} else {
			sess, rerr = rc.Client.ResumeSession(ctx, t.RawID, acl.SessionOptions{Cwd: t.Cwd})
		}
		if rerr != nil {
			return nil, rerr
		}
		// 取样用完即释放（close 只解除活跃态，不删该会话的持久记录）。
		defer func() {
			if cerr := sess.Close(context.WithoutCancel(ctx)); cerr != nil {
				glog.Debugw(ctx, "目录取样会话 close 失败（忽略）", "runtime", spec.ID, "err", cerr)
			}
		}()
		return sess.ConfigOptions(), nil
	})
}

// catalogResumeCandidates 列出可用于取样的**已有**会话，最近的在前。
//
// 两处来源（任何一支都不新建会话）：
//  1. 本平台记录（内存集合 + sqlite）里带原生 id + cwd 的会话，按 UpdatedAt 倒序；
//  2. 兜底：运行时的 ACP `session/list`（跨客户端可见，如用户此前在终端里跑过的会话）——
//     仅在平台无记录时用，且优先带绝对 cwd 的条目；dsh 会主动排除仍在活跃的会话，
//     所以不会抢用户终端里正在用的那条。
//
// **两个名字，两种语义**（本文件最容易踩的一处）：平台记录里存的是平台侧的值（`rt_` 壳），
// 底座的原文由 `UnwrapRuntime` 现解；`session/list` 直出**底座原文**（另一个最容易漏的点），
// 包一次壳只为验证它是合法的平台侧值。写错任一处都不响（守卫② 事后才发现）。
//
// 不合法的记录**跳过**而不是整体失败：取样是"尽力读到目录"，不是校验库里的旧值——为了
// 一条残留把整个目录打成空，正是这条链最不该有的行为。
func (s *Service) catalogResumeCandidates(ctx context.Context, rc *runtimeconn.Conn) []resumeTarget {
	// 1) 平台记录（内存集合 + sqlite 兜底）。
	//
	// **快照读不持 `s.mu`**（P2b 前置）：`st` 是装配期一次性写的引用（见 `attachStore`），
	// 不需要与 `s.mu` 同临界区；持锁去请求会话快照会把"manager 锁 → session 锁"这条边固化下来，
	// 而会话域会回调 workspace 侧——workspace 状态进域平面后就是**锁序反转**（挂死，不是红）。
	st := s.st
	var recs []SessionRec
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
				recs = append(recs, SessionRec{ID: c.ID, Cwd: c.Cwd,
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

// resumeTargetsFrom 把两处来源折成候选列表（无 ACP 依赖，便于把顺序/跳过这两条判据单测掉）。
//
//   - `recs`（平台记录）：按 `UpdatedAt` 倒序——最近的在前；壳不合法的记录**跳过**
//     （它不是底座的原文，拿去问底座只会得到一句难懂的错，而别的候选可能是好的）。
//   - `listed`（`session/list` 直出**底座原文**）：只在平台记录一条候选都没折出来时用，
//     且只取首个带绝对 cwd 的条目；包一次壳只为验证它是合法的平台侧值。
func resumeTargetsFrom(ctx context.Context, recs []SessionRec, listed []acl.SessionInfo) []resumeTarget {
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
	recs = append([]SessionRec(nil), recs...)
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
func pickListedSession(items []acl.SessionInfo) (acl.SessionInfo, bool) {
	for _, it := range items {
		if it.ID != "" && strings.HasPrefix(it.Cwd, "/") {
			return it, true
		}
	}
	return acl.SessionInfo{}, false
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
		v, verr := s.providerPlane.ModelValueFor(runtimeID, rows[i].ProviderID, rows[i].Model)
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
			if reason, bad := s.providerPlane.ModelRejection(runtimeID, v.Value); bad {
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
	rc := s.runtimePool.ConnForRuntime(runtime.SelectedRuntime())
	if rc == nil {
		return nil
	}
	caps := rc.Client.Capabilities()
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
	rc := s.runtimePool.ConnForRuntime(runtime.SelectedRuntime())
	if rc == nil {
		return nil
	}
	v := rc.Client.Capabilities().Images
	return &v
}

// logCatalogFallback 目录取样**全部候选都没读通**时的留痕（不阻塞接口：返回空目录 + 说明）。
//
// 级别是 **Warn** 而不是 Debug（2026-10-15 改）：这条留痕出现时，用户看到的是**空下拉**，
// 而"下拉里没有我刚配的模型"完全可能被当成平台没交付。真实原因（旧会话引用了底座已不再
// 提供的路由、会话文件损坏…）只在这一行里，放在 Debug 等于**没人会在出事那天看到它**。
// 目录取数在面板打开/会话变化时触发，不是轮询，所以一次失败一条 Warn 是合适的量级。
//
// 两种**不是故障**的情形降到 Debug，否则它们会把真正的失败淹掉（每次都 Warn = 没人再看）：
//   - `errNoSampleCandidate`：一条会话都还没有（第一条消息发送前就是这个状态）；
//   - ClassUnsupported：该底座本来就不支持 resume/load（能力事实）。
func (s *Service) logCatalogFallback(runtimeID string, err error) {
	if errors.Is(err, errNoSampleCandidate) || samplingUnsupported(err) {
		glog.Debugw(context.Background(), "模型目录暂时读不到（还没有可供取样的会话 / 该底座不支持读）",
			"runtime", runtimeID, "err", err)
		return
	}
	if ae, ok := acl.As(err); ok && ae != nil {
		glog.Warnw(context.Background(), "模型目录取样失败（所有候选会话都读不通）",
			"runtime", runtimeID, "code", ae.Code, "class", string(ae.Class), "err", ae.Details)
		return
	}
	glog.Warnw(context.Background(), "模型目录取样失败（所有候选会话都读不通）",
		"runtime", runtimeID, "err", err)
}
