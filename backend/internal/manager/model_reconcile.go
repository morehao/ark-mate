package manager

import (
	"context"
	"sort"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
)

// ——— 运行时广告（**事实**）一侧的取样 ———
//
// 平台写完配置之后，唯一能回答"它到底生效了没有"的东西就是**正面的事实比对**：把平台声明
// 折成该底座认得的不透明值，再与它自己报的候选集合比对。配置校验门抓不到两类错——entry 层
// 静默跳过（退出码不变）与字段被静默改名（`schemastery` 默认 strict=false）——对账是唯一出口。
//
// v12 起差集不再是一等 DTO：它折进 `GET /models` 的 `sync[].advertised` / `sync[].missing`
// （机器面）与 `mate doctor` 的 facts（`Missing`）。**两个底座上的默认期望仍然不同**，这条
// 非对称必须先写下来，否则对账上线第一天就会被当成 bug：
//
//	dsh：平台声明的 `models` **整体替换**该路由的目录 ⇒ 声明了却没出现是真问题；
//	opencode：声明是**追加（upsert）**，内置 models.dev 目录也在候选里 ⇒ 候选比声明多是常态。
//
// 判据始终是"**平台声明 → 该 runtime 认得的不透明值**"（`modelValueFor`），不是 `model_id`
// 字符串比对——运行时的目录里只有它自己的不透明串。

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
	// Runtime 本次取样面对的 runtime（差集比对要用它把平台声明折成不透明值）。
	Runtime string
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
		return advertisedSample{Runtime: s.runtimeOfAgent(agentID), Models: []dtomodel.AdvertisedModel{}}, nil
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
	return advertisedSample{Runtime: resp.Runtime, Models: out, Source: resp.Source, Note: resp.Note}, nil
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
