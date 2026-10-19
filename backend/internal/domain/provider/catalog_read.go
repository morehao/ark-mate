package provider

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/morehao/ark-mate/internal/code"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// ——— 运行时派生的模型目录（决策记录 D2/D3）———
//
// 目录是**事实**（运行时此刻广告什么），平台声明是另一份事实（`models` 表）。这一层只读事实，
// 不写任何配置；取样所需的"活会话 / 恢复历史会话"是执行面的事，经三个**窄端口**交进来：
//
//	LiveConfigOptionsSource —— 任一活跃会话的配置项（首选来源，零额外开销）
//	CatalogSampler          —— 恢复一条历史会话读目录（执行面 `runtimeconn.SampleConfigOptions`）
//	CatalogConnectionFacts  —— 鉴权引导与"能不能收图"（连接期事实，取不到就是 unknown）
//
// 三个端口都只有 1–2 个方法，且都由 `sessionruntime` 的同一个 Service 实现——目录逻辑搬进域
// 之后，"取样"仍然只有一处实现，域只负责**顺序、缓存、空目录说明与拒绝值标注**。

// 目录取样来源标注（对外契约字段 source）。
const (
	CatalogFromSession = "session" // 直接读该 runtime 的活跃会话
	CatalogFromResume  = "resume"  // 恢复最近一条历史会话读取（读完即 close）
	CatalogFromCache   = "cache"   // 内存缓存（TTL 内）
	CatalogFromNone    = "none"    // 无可用来源（空目录）
)

// DefaultCatalogTTL 目录缓存有效期。目录只在 new/resume/set 响应与 config_option_update
// 推送时变化，TTL 仅用于「没有活会话时避免反复 resume」；5 分钟与面板交互节奏匹配。
const DefaultCatalogTTL = 5 * time.Minute

// LiveConfigOptionsSource 任一活跃会话的配置项（`ok=false` = 没有活跃会话）。
type LiveConfigOptionsSource interface {
	LiveConfigOptions() ([]acl.ConfigOption, bool)
}

// CatalogSampler 恢复已有会话读取目录（**不新建会话**）。
//
// 三态：`opts` 非空 = 读到；`sampled=true, err=nil` = 至少一条候选读通但运行时零候选；
// `sampled=false, err!=nil` = 全部候选读不通（`ErrNoSampleCandidate` = 一条候选都没有）。
type CatalogSampler interface {
	SampleConfigOptions(ctx context.Context) (opts []acl.ConfigOption, sampled bool, err error)
}

// CatalogConnectionFacts 目录响应里的连接期事实（没有活跃连接时返回空/nil）。
type CatalogConnectionFacts interface {
	// AuthGuidance 已连接 runtime 声明的鉴权方式（无连接 → 空）。
	AuthGuidance() []dtomodel.AuthGuidanceResp
	// PromptImageSupported 「当前这条路由能不能收图」（nil = 取不到，**不是 false**）。
	PromptImageSupported() *bool
}

// SetLiveConfigOptionsSource 装配期登记活会话取样口（不登记 = 没有执行面）。
func (pl *Plane) SetLiveConfigOptionsSource(src LiveConfigOptionsSource) { pl.live = src }

// SetCatalogSampler 装配期登记历史会话取样口（不登记 = 不能 resume）。
func (pl *Plane) SetCatalogSampler(s CatalogSampler) { pl.sampler = s }

// SetCatalogConnectionFacts 装配期登记连接期事实（不登记 = 鉴权引导/收图能力留空）。
func (pl *Plane) SetCatalogConnectionFacts(f CatalogConnectionFacts) { pl.connFacts = f }

// ErrNoSampleCandidate 连一条候选会话都没有（平台无记录、runtime 也列不出会话）。
//
// 它是**正常状态**（第一条消息发送前就是这样），不是故障——说明文案与留痕级别都靠它把
// "还没有会话可供读取"与"有会话但读不通"分开（2026-10-15 的事故正是把两者讲成同一句话）。
var ErrNoSampleCandidate = acl.NewError(acl.ClassRequest, "no_resumable_session",
	"还没有可供取样的会话（发送一条消息后目录会自动补全）", nil)

// ModelCatalog 返回该 runtime 的运行时模型目录。
//
// 取样顺序（决策记录 D2；**任何一支都不新建会话**）：
//  1. session —— 该 runtime 有活跃会话：直接读它的配置项（运行时自报，零额外开销）；
//  2. cache   —— 内存缓存（TTL 内）；
//  3. resume  —— 取样端口恢复历史会话读取（候选与退避在端口实现里）；
//  4. none    —— 都没读到：空目录 + 说明（面板此时显示「跟随运行时默认」）。
//
// 目录为空是合法结果，不做任何回落填充；但**空目录必须带说得通的说明**（三种成因：
// 不支持读 / 读不到 / 读到了但零候选，对用户的下一步完全不同）。
func (pl *Plane) ModelCatalog(ctx context.Context) (*dtomodel.ModelCatalogResp, error) {
	spec, ok := pl.reg.Get(runtime.SelectedRuntime())
	if !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "runtime_not_registered",
			"[runtime_not_registered] 构建期选定的 runtime 未装配", nil))
	}

	// 没有活会话端口 = 纯列表装配（测试/无执行面）：退化为空目录，不做任何拨号。
	if pl.live == nil && pl.sampler == nil {
		return pl.catalogResp(ctx, spec.ID, CatalogFromNone, nil, time.Time{},
			"本进程未启用执行面（无 Orchestrator），无法从运行时读取模型目录。"), nil
	}

	// 1) 活会话优先：该会话的配置项就是运行时此刻的真实状态。
	//
	// 注意"有活会话"与"有候选"是两件事：会话在、但运行时没有选中任何模型时它报**零个**
	// 配置项——那也要给说明（自举闭环的第一态：还没发过消息 / 没设默认模型）。
	if pl.live != nil {
		if opts, ok := pl.live.LiveConfigOptions(); ok {
			if len(opts) == 0 {
				return pl.catalogResp(ctx, spec.ID, CatalogFromSession, nil, time.Time{},
					pl.CatalogEmptyNote(ctx, true, nil)), nil
			}
			pl.PutConfigOptions(spec.ID, opts)
			return pl.catalogResp(ctx, spec.ID, CatalogFromSession, opts, time.Now().UTC(), ""), nil
		}
	}

	// 2) 缓存。
	if e, ok := pl.ConfigOptions(spec.ID, DefaultCatalogTTL); ok {
		return pl.catalogResp(ctx, e.Runtime, CatalogFromCache, e.Options, e.FetchedAt, ""), nil
	}

	// 3) resume 历史会话读取（只读取样：读完立即 close，不留下任何新会话）。
	if pl.sampler != nil {
		opts, sampled, err := pl.sampler.SampleConfigOptions(ctx)
		if err == nil && len(opts) > 0 {
			pl.PutConfigOptions(spec.ID, opts)
			return pl.catalogResp(ctx, spec.ID, CatalogFromResume, opts, time.Now().UTC(), ""), nil
		}
		if err != nil {
			pl.logCatalogFallback(spec.ID, err)
		}
		// 4) 无候选：空目录 + 说明（成因不同，说明不同）。
		return pl.catalogResp(ctx, spec.ID, CatalogFromNone, nil, time.Time{},
			pl.CatalogEmptyNote(ctx, sampled, err)), nil
	}

	return pl.catalogResp(ctx, spec.ID, CatalogFromNone, nil, time.Time{},
		"该交付面未提供目录取样口，无法从运行时读取模型目录。"), nil
}

// catalogEmptyNote 组装"目录里一个候选都没有"的说明（成因不同，出路不同）。
func (pl *Plane) CatalogEmptyNote(ctx context.Context, sampled bool, sampleErr error) string {
	declared := 0
	if rows, err := pl.ListModels(ctx); err == nil {
		declared = len(rows)
	}
	switch {
	case declared == 0:
		return "运行时此刻没有广告任何模型候选，平台清单里也还没有模型：" +
			"先到「设置 · 模型」里添加供应商与模型，再选一条默认模型。"
	case sampleErr != nil && !errors.Is(sampleErr, ErrNoSampleCandidate) && !samplingUnsupported(sampleErr):
		return "这次没能从运行时的历史会话里读到模型目录（取样失败）：" +
			"平台已声明 " + itoa(declared) + " 条模型，这次却一条候选都没读到。发送一条消息后目录会重新取样。"
	case sampled:
		return "平台已声明 " + itoa(declared) + " 条模型，但运行时此刻没有选中的模型：" +
			"在「设置 · 模型」里选一条默认模型后，目录会带上候选。"
	default:
		return "平台已声明 " + itoa(declared) + " 条模型；还没有可供取样的会话：" +
			"发送一条消息后目录会自动补全。"
	}
}

// itoa 小整数转字符串（避免为一个说明文案引入 fmt）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// samplingUnsupported 报告这次失败是"该底座不支持"（正常能力事实，不是故障）。
//
// 判据取 **Class** 而不是 code 前缀：这一类的 code 长这样——`resume_session_unsupported`、
// `list_sessions_unsupported`——后缀在尾不在头。
func samplingUnsupported(err error) bool {
	ae, ok := acl.As(err)
	return ok && ae != nil && ae.Class == acl.ClassUnsupported
}

// modelIDByValue 算"该 runtime 的不透明值 → 平台模型行 id"的注解表（v26，定案「辰」）。
//
// 比对的是**派生值对派生值**：对每条平台行算 `ModelValueFor`，再与候选逐字比。所以平台
// **不解析**底座词表，只是把同一份生成规则跑了两处。
//
// 多命中按 **fail-closed**（不收录）：两条平台行派生出同一个运行时值时，这个值指不到确定的一行。
// memory 模式（没有平台清单载体）返回 nil：所有候选都"未声明"，这是**正确结论**而不是失败。
func (pl *Plane) ModelIDByValue(ctx context.Context, runtimeID string) map[string]string {
	if pl.st == nil {
		return nil
	}
	rows, err := pl.st.ListModelDefinitions(ctx)
	if err != nil {
		glog.Warnw(ctx, "读平台模型清单失败（本次目录候选不带平台注解，客户端因此选不了它们）",
			"runtime", runtimeID, "err", err)
		return nil
	}
	seen := make(map[string]string, len(rows))
	dup := map[string]bool{}
	for i := range rows {
		v, verr := pl.ModelValueFor(runtimeID, rows[i].ProviderID, rows[i].Model)
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
func (pl *Plane) catalogResp(ctx context.Context, runtimeID, source string, opts []acl.ConfigOption,
	fetchedAt time.Time, note string) *dtomodel.ModelCatalogResp {
	// 注解表一次算好（逐候选查表，不逐候选扫清单）。
	byValue := pl.ModelIDByValue(ctx, runtimeID)
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
			if reason, bad := pl.ModelRejection(runtimeID, v.Value); bad {
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
	// 鉴权引导与收图能力取自连接期事实（需要握手过的连接；未连接时留空/nil，面板按 Note 提示）。
	if pl.connFacts != nil {
		resp.AuthGuidance = pl.connFacts.AuthGuidance()
		resp.ImageInput = pl.connFacts.PromptImageSupported()
	}
	return resp
}

// logCatalogFallback 目录取样**全部候选都没读通**时的留痕（不阻塞接口：返回空目录 + 说明）。
//
// 级别是 **Warn** 而不是 Debug（2026-10-15 改）：这条留痕出现时，用户看到的是**空下拉**，
// 而"下拉里没有我刚配的模型"完全可能被当成平台没交付。两种**不是故障**的情形降到 Debug，
// 否则它们会把真正的失败淹掉：一条会话都还没有 / 该底座本来就不支持读。
func (pl *Plane) logCatalogFallback(runtimeID string, err error) {
	if errors.Is(err, ErrNoSampleCandidate) || samplingUnsupported(err) {
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

// AdvertisedSample 一次运行时目录取样（**事实一侧**），并带上"这份取样到底可不可信"的来源。
//
// 为什么要把来源一起带出来：空目录有**两种完全不同的含义**，只看长度分不开——
//
//	source=none    没有来源（离线进程没有编排器 / 还没有活跃会话）→ **无法校验**；
//	source=session|cache|resume  真的读到了目录，它就是"底座此刻广告了什么"。
//
// 把两者混成一个"空"会在对账上造出两个方向的谎：对 `mate doctor` 是**假红**
// （把"没取到"报成"写了完全没生效"），对 `GET /models` 是**假绿**（把"没取到"报成"两侧一致"）。
type AdvertisedSample struct {
	// Runtime 本次取样面对的 runtime（差集比对要用它把平台声明折成不透明值）。
	Runtime string
	// Models 运行时不透明值（category=model 的全量候选，按 value 升序）。
	Models []dtomodel.AdvertisedModel
	// Source 取样来源（session|cache|resume|none）；空 = 本次没有取样（无执行面 / 未注册底座）。
	Source string
	// Note 取样面给的人话说明（source=none 时就是"为什么没有"）。
	Note string
}

// Known 本次取样是否真的读到了运行时目录。
func (a AdvertisedSample) Known() bool {
	switch a.Source {
	case CatalogFromSession, CatalogFromCache, CatalogFromResume:
		return true
	}
	return false
}

// AdvertisedSample 取**构建期固定那个底座**的运行时目录（读 `GET /models` 的同一实现），
// 带上来源与说明。v19 起取样不再经 agent：目录只有一个来源面。
func (pl *Plane) AdvertisedSample(ctx context.Context) (AdvertisedSample, error) {
	if pl.live == nil && pl.sampler == nil {
		return AdvertisedSample{Runtime: runtime.SelectedRuntime(), Models: []dtomodel.AdvertisedModel{}}, nil
	}
	resp, err := pl.ModelCatalog(ctx)
	if err != nil {
		return AdvertisedSample{}, err
	}
	out := []dtomodel.AdvertisedModel{}
	for _, opt := range resp.Options {
		if opt.Category != "model" {
			continue
		}
		for _, v := range opt.Values {
			out = append(out, dtomodel.AdvertisedModel{
				Value: v.Value, Name: v.Name, Group: v.Group, GroupName: v.GroupName,
				Unavailable: v.Unavailable, UnavailableReason: v.UnavailableReason,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return AdvertisedSample{Runtime: resp.Runtime, Models: out, Source: resp.Source, Note: resp.Note}, nil
}
