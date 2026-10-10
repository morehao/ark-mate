package manager

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/acl"
)

// ——— T8：清单（**意图**）与运行时广告（**事实**）的对账 ———
//
// 为什么对账不是 UX 装饰：平台写完配置之后，唯一能回答"它到底生效了没有"的东西就是**正面的事实比对**。
// 两类错只有它能抓：
//
//	① entry 层静默失败（目标 id 写错 → include 只 warn 一句然后跳过整条，退出码不变）；
//	② 字段被静默改名（schemastery 默认 strict=false，未知字段被静默合并回来——`--dump-config`
//	   与 JSON Schema 都会判它合法）。
// 配置校验门对这两类都无能为力，**对账是唯一出口**（设计 R8）。
//
// 差集本身不是"错了"，两个底座上的默认期望**不同**（这是必须先写下来的语义非对称）：
//
//	dsh：平台声明的 `models` **整体替换**该路由的目录 ⇒ `declared_not_advertised` 期望为空；
//	opencode：声明是**追加（upsert）**，内置 models.dev 目录也在候选里
//	         ⇒ `advertised_not_declared` **默认非空**，那不是故障。

// reconcileSeverity 差集条目的严重度。取值由**用户会遇到什么**决定，不是技术分类。
const (
	// severityError 选了就报错（硬白名单路由：未列出的 id 抛 UNKNOWN_MODEL）。
	severityError = "error"
	// severityDegrade 静默降级（咨询性目录：未列出的 id 照样发请求，只是按 text-only 估能力）。
	severityDegrade = "degrade"
	// severityInfo 只是事实（底座自带目录里的条目；平台不管它）。
	severityInfo = "info"
)

// reconcileAdvertisedNotDeclaredSeverity 判断"底座有、平台没声明"这一侧的严重度。
//
// 判据是**该底座的目录语义**：opencode 的声明是追加，内置目录永远在候选里 ⇒ 这是常态（info）；
// dsh 的 pi-ai 路由是整体替换，多出来的条目意味着"平台没列它却存在"⇒ 值得看一眼（degrade）。
func reconcileAdvertisedNotDeclaredSeverity(runtimeID string) string {
	switch runtimeID {
	case "opencode":
		return severityInfo
	case "deepseekharness":
		return severityDegrade
	default:
		return severityInfo
	}
}

// declaredNotAdvertisedSeverity 判断"平台声明了、底座没广告"这一侧的严重度——**按 route 分**。
//
// dsh 的两个路由语义完全不同（设计约束 ③）：
//
//	`llm-pi-ai`     硬白名单：未列出的 model id 在请求时抛 `UNKNOWN_MODEL`（选了就用不了）；
//	`llm-deepseek`  咨询性目录：未列出的 id **静默降级成 text-only**，请求照发。
//
// 所以同一个差集在两条路由上是"报了错"与"能力被悄悄降级"两种后果，不能只报一个数字。
// route 名就是 provider 档案 id（平台用它当路由 key），所以这里按它判。
func declaredNotAdvertisedSeverity(runtimeID, providerID string) string {
	if runtimeID != "deepseekharness" {
		// opencode：声明的条目进目录是 upsert，没进去通常意味着配置没被读（严重）。
		return severityError
	}
	if strings.Contains(strings.ToLower(providerID), "deepseek") {
		return severityDegrade
	}
	return severityError
}

// reconcileModels 算差集。**只读两边、只报不改**。
//
// 两侧的比较基准是**生成的 value**（不是 model_id）：运行时的目录里只有它自己的不透明串，
// 而平台侧的 model_id 是"写出去给底座的输入"。两者靠同一份生成规则对齐（`modelValueFor`）——
// 这正是"平台只存生成 value 的输入，绝不把运行时的 value 存回清单"那条约束的用处。
func (s *Service) reconcileModels(ctx context.Context, runtimeID, agentID string) (dtomodel.ModelDiff, error) {
	diff := dtomodel.ModelDiff{
		DeclaredNotAdvertised: []dtomodel.ModelDiffEntry{},
		AdvertisedNotDeclared: []dtomodel.ModelDiffEntry{},
	}
	declared, err := s.ListModels(ctx, runtimeID)
	if err != nil {
		return diff, err
	}
	advertised, err := s.advertisedModels(ctx, agentID)
	if err != nil {
		return diff, err
	}
	advByValue := map[string]dtomodel.AdvertisedModel{}
	for _, a := range advertised {
		advByValue[a.Value] = a
	}
	declaredValues := map[string]bool{}
	for _, m := range declared {
		declaredValues[m.Value] = true
		if _, ok := advByValue[m.Value]; ok {
			continue
		}
		diff.DeclaredNotAdvertised = append(diff.DeclaredNotAdvertised, dtomodel.ModelDiffEntry{
			ModelID:    m.ModelID,
			Value:      m.Value,
			ProviderID: m.ProviderID,
			Route:      m.ProviderID,
			Severity:   declaredNotAdvertisedSeverity(runtimeID, m.ProviderID),
			Reason:     declaredNotAdvertisedReason(runtimeID, m.ProviderID) + s.projectionHint(runtimeID, m.ProviderID),
		})
	}
	for _, a := range advertised {
		if declaredValues[a.Value] {
			continue
		}
		diff.AdvertisedNotDeclared = append(diff.AdvertisedNotDeclared, dtomodel.ModelDiffEntry{
			Value:    a.Value,
			Route:    routeOfValue(runtimeID, a.Value),
			Severity: reconcileAdvertisedNotDeclaredSeverity(runtimeID),
			Reason:   "底座自己的目录里的条目（平台没有声明它）：平台不管它，如实展示、不删",
		})
	}
	sort.Slice(diff.DeclaredNotAdvertised, func(i, j int) bool {
		return diff.DeclaredNotAdvertised[i].Value < diff.DeclaredNotAdvertised[j].Value
	})
	sort.Slice(diff.AdvertisedNotDeclared, func(i, j int) bool {
		return diff.AdvertisedNotDeclared[i].Value < diff.AdvertisedNotDeclared[j].Value
	})
	return diff, nil
}

// declaredNotAdvertisedReason 把"写了但没生效"翻成一句可执行的说明。
func declaredNotAdvertisedReason(runtimeID, providerID string) string {
	base := "平台已声明，但底座当前的目录里没有它（**写了没生效**）"
	switch {
	case runtimeID == "deepseekharness" && strings.Contains(strings.ToLower(providerID), "deepseek"):
		return base + "：这条路由是**咨询性目录**，底座不会报错，只是把该模型按 text-only 对待（能力被静默降级）"
	case runtimeID == "deepseekharness":
		return base + "：这条路由是**硬白名单**，选它会直接报 UNKNOWN_MODEL——先确认配置有没有被读到（`POST /models/apply` 后新起进程再看）"
	default:
		return base + "：确认配置已物化且**拉了新进程**（配置在进程启动时读），或看这条 provider 的 baseURL/密钥是否可用"
	}
}

// projectionHint 给"声明了但没广告到"补一句**最可能的成因**：该 provider 有没有声明这个 runtime
// 的运行时参数（`projections.<runtime>`）。
//
// 为什么值得单列：dsh 侧一条没有 `api`/`baseURL` 的 route 是**不可用**的，而它失败起来很不像"缺参数"
// ——实测表现是新会话直接 `Internal error`（2026-10-10）。把"你还没填运行时参数"摆在差集条目里，
// 用户就不用从一句不透明的错误倒推。
func (s *Service) projectionHint(runtimeID, providerID string) string {
	s.mu.Lock()
	d := s.providers[providerID]
	s.mu.Unlock()
	if d == nil {
		return ""
	}
	frags, _ := d.Body["projections"].(map[string]any)
	frag, _ := frags[runtimeID].(map[string]any)
	if len(frag) > 0 {
		return ""
	}
	return "；**提示**：这条供应商没有声明 " + runtimeID + " 的运行时参数（在「设置 · 模型」的供应商表单里填 `api`/`baseURL`，opencode 侧通常还要 `package`）——dsh 上缺它这条 route 不可用"
}

// routeOfValue 从不透明值里取出路由前缀（只用于**展示分组**，不参与任何判据）。
//
// 两个底座的前缀规则不同（dsh 是 JSON 数组的第一个元素、opencode 是第一个 `/` 之前），
// 取不到就返回空——**不猜**。
func routeOfValue(runtimeID, value string) string {
	switch runtimeID {
	case "opencode":
		if i := strings.IndexByte(value, '/'); i > 0 {
			return value[:i]
		}
	case "deepseekharness":
		var arr []string
		if err := jsonUnmarshalString(value, &arr); err == nil && len(arr) > 0 {
			return arr[0]
		}
	}
	return ""
}

// modelReconcileSeverities 让 `severity` 的取值集合在编译期可见（守卫测试与文档引用它）。
var modelReconcileSeverities = map[string]bool{
	severityError: true, severityDegrade: true, severityInfo: true,
}

// jsonUnmarshalString 解一个字符串数组（dsh 的 value 是 `["provider","model"]`）。
func jsonUnmarshalString(s string, out *[]string) error { return json.Unmarshal([]byte(s), out) }

// aclRuntimeNotFound runtime 未注册的对外错误。
func aclRuntimeNotFound(id string) error {
	return acl.NewError(acl.ClassRequest, "runtime_not_found", "runtime "+id+" 未注册", nil)
}

// advertisedSample 一次运行时目录取样（**事实一侧**），并带上"这份取样到底可不可信"的来源。
//
// 为什么要把来源一起带出来：空目录有**两种完全不同的含义**，只看长度分不开——
//
//	source=none    没有来源（离线进程没有编排器 / 该 agent 还没有会话）→ **无法校验**；
//	source=session|cache|resume  真的读到了目录，它就是"底座此刻广告了什么"。
//
// 把两者混成一个"空"会在对账上造出两个方向的谎：对 `mate doctor` 是**假红**
// （把"没取到"报成"写了完全没生效"），对 `GET /models` 是**假绿**（把"没取到"报成"两侧一致"）。
type advertisedSample struct {
	// Models 运行时不透明值（category=model 的全量候选，按 value 升序）。
	Models []dtomodel.AdvertisedModel
	// Source 取样来源（session|cache|resume|none）；空 = 本次没有取样（无 agent / 无执行面）。
	Source string
	// Note 取样面给的人话说明（source=none 时就是"为什么没有"）。
	Note string
}

// Known 本次取样是否真的读到了运行时目录。
func (a advertisedSample) Known() bool {
	switch a.Source {
	case catalogFromSession, catalogFromCache, catalogFromResume:
		return true
	}
	return false
}

// advertisedSample 取某 agent 的运行时目录（读 `GET /models` 的同一实现），带上来源与说明。
func (s *Service) advertisedSample(ctx context.Context, agentID string) (advertisedSample, error) {
	if agentID == "" || s.orch == nil {
		return advertisedSample{Models: []dtomodel.AdvertisedModel{}}, nil
	}
	resp, err := s.ModelCatalog(ctx, agentID)
	if err != nil {
		return advertisedSample{}, err
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
	return advertisedSample{Models: out, Source: resp.Source, Note: resp.Note}, nil
}

// advertisedModels 取某 agent 的运行时目录里的模型候选（读 `GET /models` 的同一实现）。
//
// 只要候选、不要来源的调用方用它；**要判"这份事实可不可信"的调用方必须走 `advertisedSample`**
// （差集与体检都属后者，见 `advertisedSample` 的注释）。
func (s *Service) advertisedModels(ctx context.Context, agentID string) ([]dtomodel.AdvertisedModel, error) {
	sample, err := s.advertisedSample(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return sample.Models, nil
}

// agentForRuntime 取一个"服务该 runtime"的 agent id（对账要借它的目录读数）。
//
// 优先「当前 Agent」，否则取声明了该 runtime 的第一个（id 升序）——对账是只读的，
// 借哪个 agent 读目录不影响"平台清单 vs 底座事实"这个比对本身。
func (s *Service) agentForRuntime(runtimeID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.currentDefaultLocked(); cur != "" {
		if a, ok := s.agents[cur]; ok && containsString(a.Runtimes, runtimeID) {
			return cur
		}
	}
	ids := make([]string, 0, len(s.agents))
	for id, a := range s.agents {
		if a != nil && containsString(a.Runtimes, runtimeID) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// ModelsView 组装 `GET /models` 的完整读面：**平台清单 + 运行时事实 + 差集 + 物化状态**。
//
// `Options` 只在**该 runtime 未声明 ConfigProjection** 时回落到纯旧形态（避免第三个 runtime
// 未来接入时"没有清单可读"就什么都读不到）。
func (s *Service) ModelsView(ctx context.Context, runtimeID, agentID string, refresh bool) (*dtomodel.ModelsResp, error) {
	if runtimeID == "" {
		runtimeID = s.runtimeOfAgent(agentID)
	}
	if runtimeID == "" {
		runtimeID = "dsh"
		if _, ok := s.reg.Get(runtimeID); !ok {
			for _, spec := range s.reg.List() {
				runtimeID = spec.ID
				break
			}
		}
	}
	if _, ok := s.reg.Get(runtimeID); !ok {
		return nil, aclRuntimeNotFound(runtimeID)
	}
	// 显式指定了一个不存在的 agent → 40401（与旧行为逐字一致：打错 id 不会被静默忽略）。
	if agentID != "" {
		if _, ok := s.AgentByID(agentID); !ok {
			return nil, acl.NewError(acl.ClassRequest, "agent_not_found", "未知 agent "+agentID, nil)
		}
	}
	if agentID == "" {
		agentID = s.agentForRuntime(runtimeID)
	}
	_, declared, _ := s.configProjectionOf(runtimeID)

	resp := &dtomodel.ModelsResp{
		Runtime:    runtimeID,
		Agent:      agentID,
		Declared:   []dtomodel.ModelResp{},
		Advertised: []dtomodel.AdvertisedModel{},
	}
	if items, err := s.ListModels(ctx, runtimeID); err == nil {
		resp.Declared = items
	} else {
		return nil, err
	}
	catalog, err := s.advertisedModels(ctx, agentID)
	if err != nil {
		// 目录读不到不是致命的（对账的"事实"一侧缺失）：如实记 note，清单照常回。
		resp.Note = "运行时目录本次不可读：" + err.Error()
	} else {
		resp.Advertised = catalog
	}
	if diff, derr := s.reconcileModels(ctx, runtimeID, agentID); derr == nil {
		resp.Diff = diff
	}
	if st, serr := s.runtimeConfigState(ctx, runtimeID, nil); serr == nil && st != nil {
		resp.Config = *st
		// **`pending` 的权威判据是差集**（有样本时）：清单里每一条都被底座广告到了 ⇒ 不是 pending；
		// 还有没广告到的（或被判 error/degrade 的）⇒ 仍是 pending。没有样本（离线/无会话）时保留
		// `runtimeConfigState` 的保守值 true——"不知道"不许说成"已生效"。
		if resp.Note == "" && len(resp.Advertised) > 0 {
			resp.Config.Pending = len(resp.Diff.DeclaredNotAdvertised) > 0
		}
	}
	// 未声明落点的 runtime：回落到纯旧形态（`options` + 取样来源），避免"没有清单就什么都读不到"。
	if !declared {
		cat, cerr := s.ModelCatalog(ctx, agentID)
		if cerr != nil {
			return nil, cerr
		}
		resp.Source = cat.Source
		resp.FetchedAt = cat.FetchedAt
		resp.Options = cat.Options
		resp.AuthGuidance = cat.AuthGuidance
		if resp.Note == "" {
			resp.Note = cat.Note
		}
		if resp.Note == "" {
			resp.Note = "该 runtime 未声明模型配置落点：平台清单对它不可达，这里只有运行时派生的目录"
		}
	}
	if refresh {
		// 刷新只是**重新取样**运行时目录（缓存是 5 分钟的）；清单与差集本来就是实时算的。
		resp.Source = "session"
	}
	_ = model.TableModel
	return resp, nil
}

// runtimeOfAgent 取某 agent 当前会用的 runtime（列表首项；空 = 不知道）。
func (s *Service) runtimeOfAgent(agentID string) string {
	if agentID == "" {
		return ""
	}
	a, ok := s.AgentByID(agentID)
	if !ok || len(a.Runtimes) == 0 {
		return ""
	}
	return a.Runtimes[0]
}
