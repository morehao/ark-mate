// Package sessions 是控制面 SaaS 编排的**集中会话登记**（L7，决策 v38、设计 §C2/DM-3）。
//
// 职责单一：把「ark-iam 的 SSO 会话标识 `sid`」映射到「本进程里属于该 sid 的服务端会话 / WS
// 连接」，并在收到 back-channel logout（带 `sid`）时**按 sid 撤销**——即关闭该 sid 名下全部
// WS 连接（断流），承总纲「WS 断连即撤权」。
//
// 为什么需要它（设计 §C2 关键认知）：back-channel logout 是「会话级撤销的推送」，不是「令牌级
// 吊销」。它成立的前提是 RP 本地存在这条 `sid` 的**会话登记**：OP 推 logout_token（带 sid）→
// RP 撤掉自己那份本地会话 / WS 订阅。本仓 WS 连接原先只按事件聚合（会话 id）订阅、**不登记归属**，
// 所以 SLO 收到 sid 也无从下手——本包补的就是这张「sid → 连接」的登记表。
//
// 边界（C2 基线）：本包只覆盖「已打开本平台页面」的即时撤流（A 类）。裸 access_token 的撤权
// 窗口由 access_token 短 TTL（默认 5min）+ 静默续期兜底（B 类），不在本包职责内。
//
// 进程内状态：登记表是**纯内存**的（连接是进程内的，无法跨进程撤销）；多副本部署下每个副本各自
// 撤销自己持有的连接即可——OP 的 logout_token 会推给每个注册的 backchannel URI，各副本撤各自
// 那份，整体一致（jti 去重落表处理重复投递，见 control/slo）。
package sessions

import (
	"context"
	"sync"
)

// Registry 是 sid → 连接撤销回调的集中登记表（并发安全，零值不可用，用 NewRegistry）。
//
// 一条 WS 连接在其整个生命周期内只登记一次（按建立时解析出的 sid）。撤销时调用该 sid 名下
// 所有登记的 `close` 回调（各连接关闭自己的 socket）。连接自然关闭时调用登记返回的 `unregister`
// 注销——因此登记表只持有**仍活着**的连接，撤销不会误伤已关闭的连接（回调幂等，重复关闭安全）。
type Registry struct {
	mu     sync.Mutex
	bySID  map[string]map[uint64]func()
	nextID uint64
}

// NewRegistry 构造空的集中会话登记表。
func NewRegistry() *Registry {
	return &Registry{bySID: make(map[string]map[uint64]func())}
}

// Register 登记一条属于 sid 的连接的撤销回调（close 非 nil）。返回的 unregister 幂等
// （重复调用安全），应在连接关闭时调用以从登记表移除。sid 为空时**不登记**（返回 no-op），
// 避免把无 sid 的连接聚到同一个空键上误撤。
func (r *Registry) Register(sid string, close func()) (unregister func()) {
	if r == nil || sid == "" || close == nil {
		return func() {}
	}
	r.mu.Lock()
	r.nextID++
	id := r.nextID
	set := r.bySID[sid]
	if set == nil {
		set = make(map[uint64]func())
		r.bySID[sid] = set
	}
	set[id] = close
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if s, ok := r.bySID[sid]; ok {
				delete(s, id)
				if len(s) == 0 {
					delete(r.bySID, sid)
				}
			}
		})
	}
}

// RevokeBySID 撤销（关闭）sid 名下全部已登记连接，返回撤销的条数。
//
// 撤销在**锁外**逐个调用 close（close 内部会走 WS 关闭流程，可能回调 unregister 取锁，
// 到锁内就会死锁）；先快照该 sid 的所有回调再出锁执行。同一连接被 close 后再走 unregister
// 也安全（unregister 幂等、且删的是已被快照的 id）。
func (r *Registry) RevokeBySID(_ context.Context, sid string) (int, error) {
	if r == nil || sid == "" {
		return 0, nil
	}
	r.mu.Lock()
	set := r.bySID[sid]
	closes := make([]func(), 0, len(set))
	for _, close := range set {
		closes = append(closes, close)
	}
	// 立即从登记表摘除该 sid 名下全部：这次撤销后再登记的新连接属于"重登"，不该被本次撤掉。
	delete(r.bySID, sid)
	r.mu.Unlock()

	for _, close := range closes {
		close()
	}
	return len(closes), nil
}

// ActiveCount 返回 sid 名下当前存活的连接数（观测/测试用）。
func (r *Registry) ActiveCount(sid string) int {
	if r == nil || sid == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bySID[sid])
}
