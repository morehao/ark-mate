// Package runtimeconn 是**执行面**：把会话挂到「本进程认的那个底座」上。
//
// 它**负责**（随 P3a-2 分批搬入）：底座身份与进程状态（`Binding`）、连接池与代际键、子进程回收、
// 会话到连接的挂载（`Orchestrator`）、以及运行期诊断（stderr 尾巴 / 最近失败 / 归属歧义留痕）。
//
// 它**不负责**：会话记录与轮次（`domain/session`——那边持**记录**，这边持**连接**）、
// 能力定义与投放（`domain/{skill,mcp,provider}`）、事件的落库与铸号（`domain/session/publish.go`
// 是唯一铸号点；本域只把 ACP update 翻成领域事件、并交由会话域发布）。
//
// **切法与 P3b 相反**：P3b 先把"记录"搬进会话域，这边搬的是"连接与进程"——
// `ActiveSession` 活动集留本域，会话域只按 id 查记录。两边的锁互不嵌套：本域**绝不在自己的锁下**
// 调会话域（`manager_lock_order_test.go` 钉的是同一族纪律的另一半）。
package runtimeconn
