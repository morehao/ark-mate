package registry

import (
	"context"
	"sort"

	"github.com/morehao/ark-mate/internal/core/resource"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— `GET /models` 的读模型（P5a 取样面收口）———
//
// 它把两份事实拼在一起：**平台声明 + 运行时目录/同步结论**。两份事实各有主人：
//
//	provider.Plane    清单、目录取样、缓存与拒绝值（域）
//	Resources         同步结论与配置指纹（装配层）
//
// 目录取样的执行细节（活会话 / resume）经域的三个窄端口交下去；本类型只做**组合**与聚合，
// 不碰连接、不碰 ACP。

// ModelViews 模型读面的组合实现：`svcmodel.ModelSvc` 的 ModelsView/SyncModels/ModelCatalog，
// 以及 `svcmodel.Host` 的 ModelRow/TrySyncModels 都由它（的两份真实现）提供。
type ModelViews struct {
	*provider.Plane
	*Resources
}

// NewModelViews 用装配好的平面造模型读面。
func NewModelViews(p *Planes) *ModelViews {
	return &ModelViews{Plane: p.Providers, Resources: p.Resources}
}

// ModelsView 组装 `GET /models` 读面：**平台清单 + 聚合生效结论 + 逐 runtime 机器面明细**。
//
// 它是**无副作用**的：逐 runtime 的结论直接读上一次同步的持久记录（`RuntimeConfigStates`），
// 不在读路径上重跑同步（读一次列表不该写文件、改启动参数）。
//
// `runtimeID` 非空 = 只看那一个 runtime 的明细；`effective`/`effective_at` 恒为**聚合**值
// （ADR-22：用户面只有一个结论）。
func (v *ModelViews) ModelsView(ctx context.Context, runtimeID string, refresh bool) (*dtomodel.ModelsResp, error) {
	models, err := v.Plane.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	defaultID := ""
	if row, ok, derr := v.Plane.DefaultModelRow(ctx); derr == nil && ok {
		defaultID = row.ID
	}

	resp := &dtomodel.ModelsResp{
		Models:       dtomodel.ModelResps(models),
		DefaultModel: defaultID,
		Sync:         []dtomodel.RuntimeSyncResp{},
	}
	states := v.Resources.RuntimeConfigStates(ctx)
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if runtimeID != "" && id != runtimeID {
			continue
		}
		st := states[id]
		// **尽力采样**底座事实（`advertised`/`missing` 是机器面）：取不到就留空，
		// 绝不把"没取到"当成"底座一条都没有"（那是假红），也不当成"两侧一致"（那是假绿）。
		// 采样面只有**构建期固定那个底座**有执行面；别的 runtime 如实留空（unknown）。
		if id != runtime.SelectedRuntime() {
			resp.Sync = append(resp.Sync, st)
			continue
		}
		if sample, serr := v.Plane.AdvertisedSample(ctx); serr == nil && sample.Known() {
			values := make([]string, 0, len(sample.Models))
			for _, m := range sample.Models {
				values = append(values, m.Value)
			}
			sort.Strings(values)
			st.Advertised = values
			st.Missing = v.missingDeclaredModelsFor(ctx, id, values)
		}
		resp.Sync = append(resp.Sync, st)
	}
	resp.Effective, resp.EffectiveAt = aggregateEffect(resp.Sync, len(models))
	if len(models) == 0 {
		resp.Note = "清单为空：平台不下发任何条目，新会话进程只看到它自带的目录"
	}
	return resp, nil
}

// missingDeclaredModelsFor 声明了却没出现在运行时广告里的条目（机器面，人话化）。
//
// 判据是**同一份生成规则**（`ModelValueFor`）：平台声明 → 该 runtime 认得的不透明值，
// 再与广告集合比对。两处各写一遍迟早分叉——"界面说生效了、实际目录里其实没有"正是这条要挡的。
func (v *ModelViews) missingDeclaredModelsFor(ctx context.Context, runtimeID string, advertised []string) []string {
	declared, err := v.Plane.ListModels(ctx)
	if err != nil {
		return nil
	}
	have := make(map[string]bool, len(advertised))
	for _, a := range advertised {
		have[a] = true
	}
	out := []string{}
	for i := range declared {
		val, verr := v.Plane.ModelValueFor(runtimeID, declared[i].ProviderID, declared[i].Model)
		if verr != nil || val == "" {
			continue
		}
		if !have[val] {
			out = append(out, declared[i].ProviderID+"/"+declared[i].Model)
		}
	}
	sort.Strings(out)
	return out
}

// aggregateEffect 把逐 runtime 的结论折成用户面的**聚合**结论（ADR-22）。
//
// 三条判据，从差到好：
//
//	failed   任一 runtime 交付失败；
//	pending  交付成功但**没有被观测到**（advertised 采样为空、或有声明条目不在目录里、
//	         或该 runtime 根本没声明交付面）——「已保存」只说明写进了平台清单；
//	applied  某次采样里**声明的条目全都出现在底座目录里**（`missing` 为空且确实取到了样本）。
//
// 为什么 `applied` 必须以**观测**为准而不是"交付成功"：那正是本仓反复要防的那类谎——
// 平台写完文件就说"已生效"，而底座要等新进程才读它。`sync[].pending` 就是这个事实的载体；
// 用户面不许出现底座名，所以聚合结论只能取所有 runtime 里**最保守**的那一档。
// 也正因为如此，`effective_at` 取所有 runtime 里**最晚**的那个（同样保守）。
func aggregateEffect(sync []dtomodel.RuntimeSyncResp, modelCount int) (effective, effectiveAt string) {
	if modelCount == 0 {
		// 清单为空：没有要交付的东西，聚合结论就是"已生效"（撤回路径也走这里）。
		return "applied", "now"
	}
	effective, effectiveAt = "applied", "now"
	worst := -1
	for i := range sync {
		st := sync[i]
		// 清单与凭据两条通道各报各的时机（ADR-14），取更晚的那条。
		rank := resource.EffectRank(st.Effect)
		if r := resource.EffectRank(st.Credential.Effect); r > rank {
			rank = r
		}
		if rank > worst {
			worst = rank
			effectiveAt = effectToUserWord(rank)
		}
		switch {
		case st.Status == resource.StatusFailed:
			effective = "failed"
		case st.Status == resource.StatusUnsupported, st.Status == resource.StatusUntested:
			if effective != "failed" {
				effective = "pending"
			}
		case len(st.Advertised) == 0 || len(st.Missing) > 0:
			// 交付成功但没被观测到（或观测到缺口）：**不许**说"已生效"。
			if effective != "failed" {
				effective = "pending"
			}
		}
	}
	if len(sync) == 0 {
		// 一个可用底座都没有：如实报"待生效"而不是"已生效"。
		effective = "pending"
	}
	return effective, effectiveAt
}

// effectToUserWord 把机器面的生效时刻折成用户词（`now` | `next_session` | `next_launch`）。
func effectToUserWord(rank int) string {
	switch rank {
	case 0:
		return "now"
	case 1, 2:
		return "next_session"
	default:
		return "next_launch"
	}
}
