package sessionruntime

import (
	"context"
	"fmt"

	"github.com/morehao/ark-mate/internal/domain/provider"

	"github.com/morehao/ark-mate/internal/platform/object/objdelivery"
)

// ——— provider 写面的**生效面回执**（T4 的落地形态） ———
//
// 这张回执回答的是"我改的这一条，谁什么时候拿到"——今天没有任何端点回答它（P4），
// 而它是"用户以为保存了就生效了"这类误解的唯一解药。
//
// 判据必须与代际机制**同源**：`affected_sessions` 与 `deferred` 若各算一遍，
// 迟早会与 `planGenerationSwitch` 的裁决漂移（那种漂移不会让任何测试变红，只会让界面
// 与真实行为说两套话）。所以这里的 deferred 判据**直接调那个只读裁决函数**——
// 它本来就是只读、无副作用的（`generation.go` 的注释把这一点写成了存在理由）。

// providerEffect 计算一条 provider 档案改动的生效面（只读、无副作用）。
//
// 返回 nil 只有一种情况：没有编排器（`demo`/`migrate`/纯列表场景）——那时没有任何活会话，
// "哪些会话会顺延"这个问题本身不成立；而**编不出数就不编**比回一个 0 更诚实。
func (s *Service) providerEffect(providerID string) *provider.ProviderEffect {
	o := s.orch
	if o == nil {
		return nil
	}
	// 通道/时刻**取自静态交付表**（`objdelivery`），不在这里再写一份字面量：
	// 两处各写一遍迟早漂移，而漂移的表现是"回执说的通道与真实交付不一致"。
	delivery, _ := objdelivery.For(objdelivery.KindProvider)
	eff := &provider.ProviderEffect{
		Channel: delivery.Channel,
		Effect:  delivery.Effect,
	}

	// 受影响面：v19 起一条供应商"在不在环上"只由它有没有模型条目挂靠决定（agent 面撤销后
	// 没有"某个 agent 选了它"这回事）；不在环上，下发给底座的声明里就没有它，改了什么都不会发生。
	if len(s.facts.Providers.ProviderModelRefsReadOnly(context.Background(), providerID)) == 0 {
		eff.Note = "当前没有模型条目挂靠在这条供应商上：改动不会下发给底座（先在「设置 · 模型」里让它承载模型条目）"
		return eff
	}

	// 在环上 → 这份声明变了，本机所有会话下一条命轮都会按新声明重开连接（逐会话判是否顺延）。
	// **不持 `s.mu`**（P2b 守卫）：快照读与下面的循环都只碰副本与会话域自己的动作。
	recs := s.sessionPlane.Snapshot()

	for i := range recs {
		rec := &recs[i]
		eff.AffectedSessions++
		// 同源判据：有在途轮次 / 有未应答权限或提问 → 顺延到再下一轮。
		// 没有活句柄的会话不算顺延——它下一次 prompt 本来就会按当前配置重开（拿到新密钥）。
		as := o.session(rec.ID)
		if as == nil {
			continue
		}
		if plan := o.planGenerationSwitch(as, rec); plan.Action == genActionDefer {
			eff.Deferred++
		}
	}

	switch {
	case eff.AffectedSessions == 0:
		eff.Note = "当前没有会话：改动会在下一次建会话时生效"
	case eff.Deferred > 0:
		eff.Note = fmt.Sprintf("有 %d 条会话当前有在途轮次或有未应答权限/提问，将顺延到再下一轮；本轮行为不变",
			eff.Deferred)
	default:
		eff.Note = "引用它的会话会在下一个轮次边界重建连接并拿到新密钥"
	}
	return eff
}
