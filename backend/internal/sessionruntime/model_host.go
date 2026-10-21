package sessionruntime

import (
	"context"

	"github.com/morehao/ark-mate/internal/contract/dtomodel"
)

// ——— `model.Host` 的过渡端口实现（P4a 正文第五十九刀）———
//
// 行投影已随模型读面进 `domain/provider`（`ModelRow` 只留一行转发）；`ModelEffect` 仍在这里：
// "哪些会话会顺延"要问代际裁决（与连接/会话编排绑在一起），随 `Orchestrator` 在 P5 dissolve。

// ModelEffect 计算模型清单改动的生效面回执（只读、无副作用；判据与代际裁决同源）。
//
// 导出是**过渡期的端口实现**（`model.Host`，P4a 正文第五刀）：清单写面已搬进
// `service/svcmodel`，而"哪些会话会顺延"要问代际裁决（与连接/会话编排绑在一起，P5 才 dissolve）。
// 搬过去的那一天，这个方法连同 `model.Host` 一起消失。
func (s *Service) ModelEffect(defaultTouched bool) *dtomodel.ModelEffect {
	eff := &dtomodel.ModelEffect{Effect: "next_session"}
	o := s.orch
	// **快照读不持 `s.mu`**（P2b 前置）：`o` 是装配期回挂的引用，循环体只读快照与它；
	// 持锁请求会话快照会把"manager 锁 → session 锁"这条边固化下来（见
	// `manager_lock_order_test.go` 的说明：workspace 状态进域平面后就是锁序反转，表现是挂死）。
	recs := s.sessionPlane.Snapshot()
	for i := range recs {
		eff.AffectedSessions++
		if o == nil {
			continue
		}
		as := o.session(recs[i].ID)
		if as == nil {
			continue
		}
		if plan := o.planGenerationSwitch(as, &recs[i]); plan.Action == genActionDefer {
			eff.Deferred++
		}
	}
	if defaultTouched {
		eff.Note = "已保存：新会话创建时由平台下发这个默认模型（立即）；既有会话在下一个会话进程读到配置。**不是「已生效」**"
	} else {
		eff.Note = "已保存，将在下一个会话进程生效（配置在进程启动时读）；此刻底座的模型目录仍是旧的那份"
	}
	return eff
}

// ——— 派生只读：`GET /agents` 的 `default_model` ———

// ModelFace 把「模型读面（`ModelViews` 端口：清单/目录/同步）」与「生效面回执
// （本层 ModelEffect）」拼成 `model.ModelSvc` / `Host` 需要的形状。
//
// 这是装配点的**组合**，不是转发层：读面与同步各自来自一份真实现，生效面回执要问代际裁决
// （随 Orchestrator 留在这里）。两份事实拼成一个接口实现——这就是它的全部职责。
//
// 阶段 0.3：读面由**端口**注入，不再内嵌 `*registry.ModelViews`——本层的方法集不再由
// "嵌了谁"决定，换一份读面实现也不必改这里。
type ModelFace struct {
	views ModelViews
	*Service
}

// NewModelFace 造一个模型面组合（装配点与测试共用）。views 由装配层用
// `registry.NewModelViews(planes)` 造好交进来。
func NewModelFace(s *Service, views ModelViews) ModelFace {
	return ModelFace{views: views, Service: s}
}

// ModelsView 读面（转发到端口）。
func (f ModelFace) ModelsView(ctx context.Context, runtimeID string, refresh bool) (*dtomodel.ModelsResp, error) {
	return f.views.ModelsView(ctx, runtimeID, refresh)
}

// SyncModels 同步（转发到端口）。
func (f ModelFace) SyncModels(ctx context.Context, runtimeID string) (*dtomodel.ModelSyncResp, error) {
	return f.views.SyncModels(ctx, runtimeID)
}

// TrySyncModels 提交后立即同步（转发到端口）。
func (f ModelFace) TrySyncModels(ctx context.Context) string {
	return f.views.TrySyncModels(ctx)
}

// ——— 对外投影（v3.2 刀 8）———
//
// 领域（`provider.Plane`）返回无 tag 视图；`svcmodel` 的接口面向控制器，要的是 DTO。
// 这里显式遮蔽（shadow）嵌入值提升上来的同名方法，把投影钉在一个地方——否则
// `ModelFace` 会因为嵌入值返回领域类型而不再满足 `model.ModelSvc`。
func (f ModelFace) ModelCatalog(ctx context.Context) (*dtomodel.ModelCatalogResp, error) {
	if f.facts.Providers == nil {
		return nil, errPlaneFactsMissing
	}
	v, err := f.facts.Providers.ModelCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return dtomodel.CatalogRespOf(v), nil
}

// ModelRow 单条模型行（`found=false` = 不存在，不是错误）。
func (f ModelFace) ModelRow(ctx context.Context, id string) (*dtomodel.ModelResp, bool, error) {
	if f.facts.Providers == nil {
		return nil, false, errPlaneFactsMissing
	}
	v, found, err := f.facts.Providers.ModelRow(ctx, id)
	if err != nil || !found {
		return nil, found, err
	}
	return dtomodel.ModelRespOf(v), true, nil
}
