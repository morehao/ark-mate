// Package session 是**会话**这个域：会话生命周期、轮次、输入队列、消息投影与读模型。
//
// 它**负责**：会话记录（内存 + `sessions` 表）、轮次的物化与收口、输入队列、消息的白名单投影
// （`messages`）、读模型的重建、流式文本的合并（避免把每个增量都推成一帧）、以及会话的
// **wire transcript**（旁路 JSONL 事实源：落盘、启动回读、检索，外加 handoff 摘要）。
//
// 它**不负责**：底座连接与子进程（执行面 `app/run` 与 `adapters/outbound/runtimeconn`）、技能/连接器/供应商的定义与
// 生效（`domain/{skill,mcp,provider}`）、权限与提问（`domain/interaction`）、工作空间登记
// （`domain/workspace`）、以及对外 DTO 与错误码出口（用例层 `app/*` 与 `contract/dto*`）。
//
// 它是本计划**最大的一块**（约 3000 行、20 个文件），分三批搬：
//
//	P3b-0：`chunk_coalescer.go` + `inflight.go`——两块**零 `s.*` 依赖**的自足件；
//	P3b-1：会话核心（`sessions` / `persist` / `messages_*` / `usage`…）；
//	P3b-2：输入队列与出口（`inputqueue` / `publish` / `event_sink`）；
//	P3b-2b：transcript 整块（`transcript*` / `messages_search`）+ `handoff`——临时端口
//	    `TranscriptSink` 随装载/检索面搬进来后**删除**（recorder 变成域内类型）；
//	P3c：事件契约常量（`events.go`）与注入面/代际键。
package session
