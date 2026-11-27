package session

// SessionPlacement 是「**会话→worker 落点**」的执行面窄端口（SaaS 池化 M2-3 / 挑战点 G1+G2 / 决策 D8·D9）。
//
// 它回答一个问题：**这条会话该由哪个 worker 承载、用哪个本地卷 cwd 执行？**
//
// 为什么是端口而不是直接算：本地模式与云端模式的差异**只该收在一个维度**上（双层子方案的三条纪律）。
//   - 本地/单 worker（本仓现状）：所有会话都落在**本进程这一个 worker**，cwd 原样透传——`LocalPlacement`。
//   - 池化/多 worker（云端形态）：会话必须**粘性**钉在「持有它租约的 worker」上，且该 worker 拿到的
//     必须是**它自己本地卷上的 cwd**（绝不能把别的 worker 的绝对路径下发过去——那条路径远端根本不存在，
//     失败方式是 worker 崩/建会话 50001，且不可逆）。这条解析由「会话→端点注册表 + 本地卷映射」给出。
//
// 因此本端口的终局约束（两种实现都必须守）：**下发给 worker 的 cwd 必须是该 worker 本地卷上的路径**，
// 不得跨 worker 下发别人的绝对路径。单 worker 下这条恒真（透传即可），但把它写成契约，是为了让
// 云端实现不会悄悄破坏它（M2-3 的验收闸就是「跨 worker 迁移后 resume 正常、路径不下发远端绝对路径」）。
type SessionPlacement interface {
	// Place 解析某会话的执行落点：返回承载它的 worker 端点 id，以及该 worker 本地卷上的执行 cwd。
	// runtimeID 是构建期固定的底座 id（本仓只有一个）；cwd 是会话记录里的平台 cwd。
	// 返回的 endpointID 为空串表示「就是本进程」（无需跨 worker 路由）。
	Place(runtimeID, cwd string) (endpointID string, localCwd string)
}
