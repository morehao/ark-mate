package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtomodel"
)

// ——— `svcmodel.Host` 的过渡端口实现（P4a 正文第五十九刀）———
//
// 行投影已随模型读面进 `domain/provider`（`ModelRow` 只留一行转发）；`ModelEffect` 仍在这里：
// "哪些会话会顺延"要问代际裁决（与连接/会话编排绑在一起），随 `Orchestrator` 在 P5 dissolve。

// ModelRow 读一条模型并投影（`svcmodel.Host`；实现在 `provider.Plane.ModelRow`）。
func (s *Service) ModelRow(ctx context.Context, id string) (*dtomodel.ModelResp, bool, error) {
	return s.providerPlane.ModelRow(ctx, id)
}

// ModelEffect 计算模型清单改动的生效面回执（只读、无副作用；判据与代际裁决同源）。
//
// 导出是**过渡期的端口实现**（`svcmodel.Host`，P4a 正文第五刀）：清单写面已搬进
// `service/svcmodel`，而"哪些会话会顺延"要问代际裁决（与连接/会话编排绑在一起，P5 才 dissolve）。
// 搬过去的那一天，这个方法连同 `svcmodel.Host` 一起消失。
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
