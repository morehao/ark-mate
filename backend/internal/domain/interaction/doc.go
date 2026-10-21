// Package interaction 是**权限与提问**这个域：自持生命周期的孤岛，不随会话存亡。
//
// 它**负责**：挂起的权限请求与提问（各自的 `sync.Mutex` + pending 表 + 超时）、权限决策的
// 策略与动作映射（`PermissionPolicy`）、可能被记住的权限（内存 + `permission_memories` 表的
// 持久化挂钩）、以及原生权限模式到环境变量的投影（`ValidNativeModes` / `nativePermissionEnv`）。
//
// 它**不负责**：会话生命周期（那是 `domain/session`）、ACP 事件流的编排（`permission_flow.go`
// 仍在执行面 `app/run`，它驱动的是会话）、对外 DTO 与错误码（那在用例层 `app/*` 与 `contract/dto*`）。
//
// 为什么它单独成包：它是本仓唯一**已经**长出"自持生命周期"形状的部分——两个管理器各有自己的
// 锁，所有对外副作用都经**注入的函数字段**（`onResolved` / `memAdd` / `memDel` / `memDelAll`）
// 出去，构造函数只收一个 timeout。也就是说它对 `*Service` 的依赖是 0，抽出来不需要新的端口。
package interaction
