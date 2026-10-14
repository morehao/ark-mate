package manager

import (
	"context"
	"sort"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— 运行时广告（**事实**）一侧的取样 ———
//
// 平台写完配置之后，唯一能回答"它到底生效了没有"的东西就是**正面的事实比对**：把平台声明
// 折成该底座认得的不透明值，再与它自己报的候选集合比对。配置校验门抓不到两类错——entry 层
// 静默跳过（退出码不变）与字段被静默改名（`schemastery` 默认 strict=false）——对账是唯一出口。
//
// v12 起差集不再是一等 DTO：它折进 `GET /models` 的 `sync[].advertised` / `sync[].missing`
// （机器面）与 `mate doctor` 的 facts（`Missing`）。**不同底座对同一份声明的默认期望不同**，
// 这条非对称必须先写下来，否则对账上线第一天就会被当成 bug：
//
//	整体替换型：平台声明的 `models` 直接换掉该路由的目录 ⇒ 声明了却没出现是**真问题**；
//	追加（upsert）型：平台的声明是**追加**，底座自带的目录也在候选里 ⇒ 候选比声明多是常态。
//
// 本文件**不判断某个底座属于哪一类**（那是各 adapter 自己的事），只如实报两侧。
// 判据始终是"**平台声明 → 该 runtime 认得的不透明值**"（`modelValueFor`，形态由该 adapter
// 声明，见 `pkg/runtime/model_value.go`），不是 `model_id` 字符串比对——运行时的目录里
// 只有它自己的不透明串。

// advertisedSample 一次运行时目录取样（**事实一侧**），并带上"这份取样到底可不可信"的来源。
//
// 为什么要把来源一起带出来：空目录有**两种完全不同的含义**，只看长度分不开——
//
//	source=none    没有来源（离线进程没有编排器 / 还没有活跃会话）→ **无法校验**；
//	source=session|cache|resume  真的读到了目录，它就是"底座此刻广告了什么"。
//
// 把两者混成一个"空"会在对账上造出两个方向的谎：对 `mate doctor` 是**假红**
// （把"没取到"报成"写了完全没生效"），对 `GET /models` 是**假绿**（把"没取到"报成"两侧一致"）。
type advertisedSample struct {
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
func (a advertisedSample) Known() bool {
	switch a.Source {
	case catalogFromSession, catalogFromCache, catalogFromResume:
		return true
	}
	return false
}

// advertisedSample 取**构建期固定那个底座**的运行时目录（读 `GET /models` 的同一实现），带上来源与说明。
//
// v19 起取样不再经 agent：目录只有一个来源面（`ModelCatalog` 按选中 runtime 读数）。
func (s *Service) advertisedSample(ctx context.Context) (advertisedSample, error) {
	if s.orch == nil {
		return advertisedSample{Runtime: runtime.SelectedRuntime(), Models: []dtomodel.AdvertisedModel{}}, nil
	}
	resp, err := s.ModelCatalog(ctx)
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

// advertisedModels 取运行时目录里的模型候选（读 `GET /models` 的同一实现）。
//
// 只要候选、不要来源的调用方用它；**要判"这份事实可不可信"的调用方必须走 `advertisedSample`**
// （差集与体检都属后者，见 `advertisedSample` 的注释）。
func (s *Service) advertisedModels(ctx context.Context) ([]dtomodel.AdvertisedModel, error) {
	sample, err := s.advertisedSample(ctx)
	if err != nil {
		return nil, err
	}
	return sample.Models, nil
}
