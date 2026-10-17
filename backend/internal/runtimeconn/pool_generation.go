package runtimeconn

import (
	"context"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/golib/glog"
)

// ——— P4a 正文第五十四刀：代际上限准入与回收（随连接池搬入） ———

// DefaultGenerationLimit 同一配置下的能力代际上限默认值（风险表第 12 行；env MATE_GENERATION_LIMIT）。
const DefaultGenerationLimit = 4

// SetGenerationLimit 设置同一配置下的能力代际上限（0 = 不限制）。见 AdmitGeneration。
//
// 是**软上限**：达到上限时先显式淘汰「无会话引用且空闲」的代际，只有全都还在用时才拒绝新建。
// 拒绝而不是复用：复用错的能力集是安全 bug（底座会给 A 会话的能力投给 B），宁可报错。
func (pl *Pool) SetGenerationLimit(n int) {
	if n < 0 {
		n = 0
	}
	pl.mu.Lock()
	pl.generationLimit = n
	pl.mu.Unlock()
}

// GenerationLimit 当前上限（0 = 不限制；诊断/测试用）。
func (pl *Pool) GenerationLimit() int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.generationLimit
}

// AdmitGeneration 代际上限准入：在**拨号之前**判定"能不能再起一个代际"。
//
// 语义（风险表第 12 行）：上限是"猜粗了只是多花内存"这一侧的护栏，所以达到上限时**两步走**——
//  1. 先显式淘汰「无在途 run 且无任何会话引用」的代际（= 计划里的"显式淘汰空闲代际"，
//     不等空闲阈值：那些进程此刻已经没有任何人在用，收掉它们不损失任何东西）；
//  2. 淘汰后仍达上限 → **带码拒绝**（40901 `generation_limit_reached`），并在文案里给出出路
//     （复用现有能力集 / 等空闲代际回收 / 调大 MATE_GENERATION_LIMIT）。
//
// 绝不"找个差不多的代际塞进去"：能力集是进程级的，塞错了底座会给错的那一套且**不报错**
// ——那正是本仓 archtest 第 10 条守卫要防的事（猜细了是安全 bug）。
//
// 上限按**基础键**分组（同一 runtime + 同权限 + 同 provider 的进程池）：不同权重的进程池
// 本来就互不共享，拿一个全局数字去卡它们只会让"合法的多 agent"互相挤掉。
func (pl *Pool) AdmitGeneration(baseKey string) error {
	limit := pl.GenerationLimit()
	if limit <= 0 {
		return nil // 0 = 不限制（显式关掉护栏）
	}
	if n := len(pl.GenerationKeysFor(baseKey)); n < limit {
		return nil
	}
	// 达到上限：先淘汰该基础键下无人引用的代际，再复查。
	if n := pl.ReclaimUnreferenced(baseKey); n > 0 {
		glog.Infow(context.Background(), "代际上限：淘汰空闲代际", "base", baseKey, "reclaimed", n, "limit", limit)
	}
	keys := pl.GenerationKeysFor(baseKey)
	if len(keys) < limit {
		return nil
	}
	return acl.NewError(acl.ClassLimit, "generation_limit_reached",
		fmt.Sprintf("该配置下的能力代际已达上限 %d（%s）：请复用现有能力集（用同一套技能/MCP 组合）、或等空闲代际被回收后重试；确需更多并发能力集时调大 MATE_GENERATION_LIMIT",
			limit, strings.Join(ShortGenerationKeys(keys), ", ")), nil)
}

// ReclaimUnreferenced 回收该基础键下「无在途 run 且无任何会话引用」的代际（`onlyBase` 空 = 全部），
// **不等空闲阈值**——判据是"此刻没有任何人在用"，不是"闲了多久"。用于代际上限准入时腾位置。
//
// 与 ReapIdle 的分工：那个是**周期性**的暖进程回收（判据含 idle 阈值，服务于"StartAgent
// 常驻但没人用"）；本函数是**事件驱动**的立即回收（正是计划里的"显式淘汰空闲代际"）。
// 回收动作都走 Drop + client.Close，不另写回收器。返回回收数量。
func (pl *Pool) ReclaimUnreferenced(onlyBase string) int {
	return pl.reclaim(func(c *Conn) bool {
		return onlyBase == "" || c.BaseKey == onlyBase
	})
}

// ReclaimGeneration 回收**指定的那一条**代际（若它已无人引用且空闲）。迁移离开旧代际后调用：
// 只动"自己刚离开的那一代"，不波及该基础键下别的无人引用进程。返回是否真的回收了。
func (pl *Pool) ReclaimGeneration(target *Conn) bool {
	if target == nil {
		return false
	}
	return pl.reclaim(func(c *Conn) bool { return c == target }) > 0
}

// reclaim 回收满足 pick 且「无在途 run + 无任何会话引用」的连接。
//
// 判定分两段：池锁内只挑候选（保护连接表），"有没有人在用"由编排侧在池锁**之外**回答
// （`DropSink.InUseOn`——它会去取会话锁）。在池锁下问会话锁就是锁序反转的雏形。
func (pl *Pool) reclaim(pick func(*Conn) bool) int {
	pl.mu.Lock()
	victims := make([]*Conn, 0, len(pl.conns))
	for _, c := range pl.conns {
		if pick(c) {
			victims = append(victims, c)
		}
	}
	pl.mu.Unlock()
	n := 0
	for _, c := range victims {
		if pl.inUse(c) {
			continue
		}
		c.Intentional.Store(true) // 主动回收，不是连接故障
		pl.Drop(c)                // 原子摘除 + 状态回落（wasCurrent 保护新连接 running）
		if c.Client != nil {
			_ = c.Client.Close() // watch 收尾（Drop 幂等）
		}
		n++
	}
	return n
}

// inUse 在池锁之外问编排侧"这条连接还有人用吗"。
//
// **没有 sink 时保守当作在用**：不知道有没有人在用就不回收——回收错的对象是「在线会话的
// 句柄被作废」，而它只会在下一次 prompt 时才被用户感知（重建一次），代价比多留一个暖进程大。
func (pl *Pool) inUse(c *Conn) bool {
	if pl.sink == nil {
		return true
	}
	return pl.sink.InUseOn(c)
}
