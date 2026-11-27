package session

// SessionWriter 是「**会话级单写者**」的执行面窄端口（SaaS 池化 M2-2 / 决策 C1·D1 / B1）。
//
// 为什么要有它：一个会话**同一时刻只能有一个活跃写者**——两处并发跑同一会话会把同一份仅追加
// 日志写成两条互相覆盖的尾部，上下文就此损坏（失败不可逆，反目标「静默丢上下文」）。
// 这条约束的**判决**必须收在一个可替换的接缝上，而不是散在 runner 的 `busy` 布尔里：
//   - 单机/单进程（本仓现状）：进程内互斥就够——`MemorySessionWriter`。
//   - 池化/多 worker（云端形态）：同一会话可能被路由到不同 worker，需要在**跨进程**维度互斥
//     （Redis 逻辑锁，TTL=心跳；与 `agent-busy` 同源，见 B1）。本端口即那条接缝：换实现只换装配，
//     调用方（执行面 acquire 路径）不动。
//
// 契约（实现必须满足，进程内/分布式一致）：
//   - `TryAcquire` 是**非阻塞**的：拿到独占权返回 true；**已被别人持有**返回 false（调用方据此
//     转成 `run_in_progress`/排队，绝不阻塞等）。
//   - 同一 sessionID 在持有期内反复 `TryAcquire` 都应返回 false（除非由同一持有者——但本端口
//     不区分重入，简化语义：持有时一律 false，调用方用 Release 收尾）。
//   - `Release` 幂等：没持有/已释放均为 no-op。
//   - 并发安全：任意多 goroutine 同时 TryAcquire 同一 id，**恰好一个**得 true。
//
// 为什么不做成 `Acquire(ctx) (release, err)` 的阻塞式：本仓的既有语义是「忙时**立刻**报
// run_in_progress 或走排队投递」（`ActiveSession.beginRun`），从不阻塞等待——阻塞式会让一个挂住的
// 写者把所有并发请求钉死在一个连接上。保持非阻塞，把「要不要等/排队」留给调用方。
type SessionWriter interface {
	// TryAcquire 尝试取得某会话的独占写权；已持有则返回 false。
	TryAcquire(sessionID string) bool
	// Release 释放某会话的独占写权（幂等）。
	Release(sessionID string)
}
