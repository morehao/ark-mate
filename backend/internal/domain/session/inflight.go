package session

import (
	"sync"
)

// InFlightTracker 记录每连接上正在执行 run 的会话（§17 方案 B：连接共享、
// 会话独立）。把 Orchestrator 原先直接操作的 busyConn map 簿记显式化封装，语义不变
// （§1.5 决策修订），给 §17 E2 留单一接缝以演进并发语义（conn 级串行/会话级 scope）。
//
// 语义：Mark 在 run 开始登记、Unmark 收尾撤销（幂等）；SoleOwner 仅在「该连接唯一在途」
// 时返回该会话 id，无在途 / 多会话并发在途（归属不唯一）返回空串——调用方不猜测，按
// SDK scope 缺失的既定策略处理（elicitation 归属推断见 orchestrator.go §1.5 修订）。
// nil conn 一律安全 no-op / 空结果。所有方法自持锁，可在不持 Orchestrator.mu 时调用。
//
// 键是**连接缓存键**（`Conn.Key()`）而不是协议句柄：同一把键在池里唯一，而"哪条连接"
// 本来就是编排侧的事实（协议句柄只在适配器里）。nil 连接归到空串这一个桶——测试夹具
// 就是这一类，与改造前用 nil 指针当键同义。
type InFlightTracker struct {
	mu     sync.Mutex
	byConn map[string]map[string]struct{}
}

func NewInFlightTracker() *InFlightTracker {
	return &InFlightTracker{byConn: make(map[string]map[string]struct{})}
}

// connKey 连接的非空标识（nil → 空串：测试夹具与"没有连接"同桶）。
func connKey(conn Conn) string {
	if conn == nil {
		return ""
	}
	return conn.Key()
}

// Mark 登记某会话在该连接上进入在途 run。
func (t *InFlightTracker) Mark(conn Conn, sessionID string) {
	if conn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	key := conn.Key()
	set := t.byConn[key]
	if set == nil {
		set = make(map[string]struct{})
		t.byConn[key] = set
	}
	set[sessionID] = struct{}{}
}

// Unmark 撤销登记（幂等：未登记/连接已清除均为 no-op）。
func (t *InFlightTracker) Unmark(conn Conn, sessionID string) {
	if conn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	key := conn.Key()
	set := t.byConn[key]
	if set == nil {
		return
	}
	delete(set, sessionID)
	if len(set) == 0 {
		delete(t.byConn, key)
	}
}

// SoleOwner 返回该连接上「唯一在途」会话 id；无在途或多会话并发在途返回空串。
func (t *InFlightTracker) SoleOwner(conn Conn) string {
	if conn == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	set := t.byConn[conn.Key()]
	if len(set) != 1 {
		return ""
	}
	for sid := range set {
		return sid
	}
	return ""
}

// Len 返回该连接在途 run 数（reap/StopAgent 忙判定）。
func (t *InFlightTracker) Len(conn Conn) int {
	if conn == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byConn[conn.Key()])
}

// Clear 清除该连接全部登记（连接断开作废；executeRun 收尾的 Unmark 对已清键为 no-op）。
func (t *InFlightTracker) Clear(conn Conn) {
	if conn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.byConn, conn.Key())
}
