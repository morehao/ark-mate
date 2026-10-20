package session

import (
	"sync"

	"github.com/morehao/ark-mate/internal/infra/acp"
)

// InFlightTracker 记录每连接（acp.Client）上正在执行 run 的会话（§17 方案 B：连接共享、
// 会话独立）。把 Orchestrator 原先直接操作的 busyConn map 簿记显式化封装，语义不变
// （§1.5 决策修订），给 §17 E2 留单一接缝以演进并发语义（conn 级串行/会话级 scope）。
//
// 语义：Mark 在 run 开始登记、Unmark 收尾撤销（幂等）；SoleOwner 仅在「该连接唯一在途」
// 时返回该会话 id，无在途 / 多会话并发在途（归属不唯一）返回空串——调用方不猜测，按
// SDK scope 缺失的既定策略处理（elicitation 归属推断见 orchestrator.go §1.5 修订）。
// nil client 一律安全 no-op / 空结果。所有方法自持锁，可在不持 Orchestrator.mu 时调用。
type InFlightTracker struct {
	mu       sync.Mutex
	byClient map[*acp.Client]map[string]struct{}
}

func NewInFlightTracker() *InFlightTracker {
	return &InFlightTracker{byClient: make(map[*acp.Client]map[string]struct{})}
}

// Mark 登记某会话在该连接上进入在途 run。
func (t *InFlightTracker) Mark(client *acp.Client, sessionID string) {
	if client == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	set := t.byClient[client]
	if set == nil {
		set = make(map[string]struct{})
		t.byClient[client] = set
	}
	set[sessionID] = struct{}{}
}

// Unmark 撤销登记（幂等：未登记/连接已清除均为 no-op）。
func (t *InFlightTracker) Unmark(client *acp.Client, sessionID string) {
	if client == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	set := t.byClient[client]
	if set == nil {
		return
	}
	delete(set, sessionID)
	if len(set) == 0 {
		delete(t.byClient, client)
	}
}

// SoleOwner 返回该连接上「唯一在途」会话 id；无在途或多会话并发在途返回空串。
func (t *InFlightTracker) SoleOwner(client *acp.Client) string {
	if client == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	set := t.byClient[client]
	if len(set) != 1 {
		return ""
	}
	for sid := range set {
		return sid
	}
	return ""
}

// Len 返回该连接在途 run 数（reap/StopAgent 忙判定）。
func (t *InFlightTracker) Len(client *acp.Client) int {
	if client == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byClient[client])
}

// Clear 清除该连接全部登记（连接断开作废；executeRun 收尾的 Unmark 对已清键为 no-op）。
func (t *InFlightTracker) Clear(client *acp.Client) {
	if client == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.byClient, client)
}
