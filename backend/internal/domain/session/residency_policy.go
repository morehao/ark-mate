package session

import (
	"sort"
	"time"
)

// ResidencyPolicy 是「**会话驻留调度**」的纯判决（SaaS 池化 M3-1 / 挑战点 C3）。
//
// 它回答：**当前驻留（resume 挂着 ACP Session、空闲保活）的会话超过 `N_resident` 上限时，该把哪几条
// 温→冷驱逐（flush 后释放 Session 句柄，只留本地卷日志，下次请求 resume 再拉起）？**
//
// 这条判决是纯函数（吃一份快照、吐一组要驱逐的会话 id）——刻意不碰 `Handles`/连接/IO：
//   - 决策规则只有一处，任何调用方（调度 goroutine、诊断、测试）拿到同一答案；
//   - 可以在没有真实进程的环境里把「绝不驱逐 executing」「只按 idle LRU」「不超上限」这三条
//     全部证死（见 residency_policy_test.go）。
//
// 规则（逐条对 C3「热/温/冷」与 M3-1 验收闸）：
//  1. **绝不驱逐 executing 会话**（`Executing=true`）——驱逐 executing 等于 cancel 用户正在跑的任务，
//     失败不可逆（C3 明令：LRU 只逐 idle）。因此即使 idle 不够、驻留仍超限，也**宁可暂时超限**，
//     绝不动 executing。
//  2. 只驱逐 **idle**（`Executing=false`）会话，按 `LastActive` **最久未活跃（LRU）优先**。
//  3. 驱逐到「驻留数 == N_resident」为止；若 idle 全驱完仍超限（executing 太多），停手（规则 1 优先）。
//  4. `N_resident <= 0` = 不限制（不驱逐）。
//
// 注意 `N_executing` 不参与**驱逐**判决：它是「单 worker 并发执行槽」的**准入**上限（进 executing 前
// 判），不是驱逐依据——驱逐永远只看驻留的 idle。这里刻意不把两个量混进一个函数（C3 修正点）。
type ResidencyPolicy struct {
	// NResident 单 worker 驻留空闲热会话上限（<=0 = 不限制）。
	NResident int
}

// ResidentSession 是一条驻留会话的调度快照（调用方从句柄表/编排态现采，policy 只读它）。
type ResidentSession struct {
	ID         string
	Executing  bool      // 正在跑 turn（绝不可驱逐）
	LastActive time.Time // 最近活跃时间（LRU 依据；零值视为最久远）
}

// Evictions 返回应温→冷驱逐的会话 id（按「最久未活跃在前」的驱逐顺序）。空 = 无需驱逐。
func (p ResidencyPolicy) Evictions(resident []ResidentSession) []string {
	if p.NResident <= 0 || len(resident) <= p.NResident {
		return nil
	}
	// 收集 idle 候选，按 LastActive 升序（最久远的排最前 = 最该被逐）。
	idle := make([]ResidentSession, 0, len(resident))
	for _, r := range resident {
		if r.Executing {
			continue // 规则 1：executing 永不在候选里
		}
		idle = append(idle, r)
	}
	sort.SliceStable(idle, func(i, j int) bool {
		if idle[i].LastActive.Equal(idle[j].LastActive) {
			return idle[i].ID < idle[j].ID // 稳定次序：同活跃时间按 id，便于测试确定性
		}
		return idle[i].LastActive.Before(idle[j].LastActive)
	})
	need := len(resident) - p.NResident // 需减少的驻留数
	if need > len(idle) {
		need = len(idle) // 规则 1/3：idle 不够就只逐到 idle 用完，绝不动 executing
	}
	out := make([]string, 0, need)
	for i := 0; i < need; i++ {
		out = append(out, idle[i].ID)
	}
	return out
}
