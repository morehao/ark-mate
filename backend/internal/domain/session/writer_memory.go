package session

import "sync"

// MemorySessionWriter 是 `SessionWriter` 的**进程内**实现：一把会话级的互斥集合。
//
// 适用：本仓现状（单进程、单 worker）。它的正确性就是「同一进程里同 id 恰好一个持有者」。
// 分布式部署需要跨进程互斥时，换一个 Redis 实现（TTL=心跳、与 agent-busy 同源，B1），
// 调用方不动——这正是把它做成端口的理由（见 writer_port.go）。
type MemorySessionWriter struct {
	mu    sync.Mutex
	owned map[string]struct{}
}

// NewMemorySessionWriter 构造进程内单写者。
func NewMemorySessionWriter() *MemorySessionWriter {
	return &MemorySessionWriter{owned: make(map[string]struct{})}
}

// TryAcquire 取得独占写权；已被持有返回 false（非阻塞）。空 id 视为无效，恒 false。
func (w *MemorySessionWriter) TryAcquire(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, busy := w.owned[sessionID]; busy {
		return false
	}
	w.owned[sessionID] = struct{}{}
	return true
}

// Release 释放独占写权（幂等）。
func (w *MemorySessionWriter) Release(sessionID string) {
	if sessionID == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.owned, sessionID)
}
