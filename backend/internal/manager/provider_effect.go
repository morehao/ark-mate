package manager

import (
	"fmt"
	"sort"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
)

// ——— provider 写面的**生效面回执**（T4 的落地形态） ———
//
// 这张回执回答的是"我改的这一条，谁什么时候拿到"——今天没有任何端点回答它（P4），
// 而它是"用户以为保存了就生效了"这类误解的唯一解药。
//
// 判据必须与代际机制**同源**：`affected_conversations` 与 `deferred` 若各算一遍，
// 迟早会与 `planGenerationSwitch` 的裁决漂移（那种漂移不会让任何测试变红，只会让界面
// 与真实行为说两套话）。所以这里的 deferred 判据**直接调那个只读裁决函数**——
// 它本来就是只读、无副作用的（`generation.go` 的注释把这一点写成了存在理由）。

// providerEffect 计算一条 provider 档案改动的生效面（只读、无副作用）。
//
// 返回 nil 只有一种情况：没有编排器（`demo`/`migrate`/纯列表场景）——那时没有任何活会话，
// "哪些会话会顺延"这个问题本身不成立；而**编不出数就不编**比回一个 0 更诚实。
func (s *Service) providerEffect(providerID string) *dtoprovider.ProviderEffect {
	o := s.orch
	if o == nil {
		return nil
	}
	eff := &dtoprovider.ProviderEffect{
		Channel:         "dial_env",
		Effect:          "next_turn",
		AppliesToAgents: []string{},
	}

	// 受影响 agent：声明了这条凭据的那些（与 GET /providers 的 refs.agents 同一判据）。
	agentSet := map[string]bool{}
	s.mu.Lock()
	recs := make([]ConversationRec, 0, len(s.conversations))
	for _, rec := range s.conversations {
		if rec != nil {
			recs = append(recs, *rec)
		}
	}
	s.mu.Unlock()

	for i := range recs {
		rec := &recs[i]
		agent, ok := s.AgentByID(rec.AgentID)
		if !ok || agent.Provider != providerID {
			continue
		}
		agentSet[agent.ID] = true
		eff.AffectedConversations++
		// 同源判据：有在途轮次 / 有未应答权限或提问 → 顺延到再下一轮。
		// 没有活句柄的会话不算顺延——它下一次 prompt 本来就会按当前配置重开（拿到新密钥）。
		as := o.conversation(rec.ID)
		if as == nil {
			continue
		}
		if plan := o.planGenerationSwitch(as, rec); plan.Action == genActionDefer {
			eff.Deferred++
		}
	}
	for id := range agentSet {
		eff.AppliesToAgents = append(eff.AppliesToAgents, id)
	}
	sort.Strings(eff.AppliesToAgents)

	switch {
	case eff.AffectedConversations == 0:
		eff.Note = "当前没有会话引用这条凭据：改动会在下一次使用它的会话里生效"
	case eff.Deferred > 0:
		eff.Note = fmt.Sprintf("有 %d 条会话当前有在途轮次或有未应答权限/提问，将顺延到再下一轮；本轮行为不变",
			eff.Deferred)
	default:
		eff.Note = "引用它的会话会在下一个轮次边界重建连接并拿到新密钥"
	}
	return eff
}
