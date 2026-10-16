// Package manager 编排 Agent 生命周期与会话执行：
//
//   - Service：实现 internal/service/svc* 声明的领域端口（**断言在装配点**：`app.go` 的
//     `router.Ports` 字面量，见 ports.go 的文件头），数据来自 runtime 注册表 + 进程内会话存储。
//     P4a 起它**不 import** `service/svc*`：哪些端口由它实现、哪些用例已经搬走，看装配点一行
//     就够（自检/导出/底座用例的实现已分别在 svcprovider/svcsystem/svcruntime）。
//   - Orchestrator：把「对一次会话的 ACP 编排」（连接获取 → session/new → prompt →
//     事件流 → cancel/close）跑通，并把归一化事件 Publish 进 eventbus（agg = session id）
//   - 能力门控：load/resume/list/fork 依 acl.Capabilities 裁剪（各底座声明的能力集不同，
//     例如 dsh 支持 list/resume/close、不支持 load/fork，见 `pkg/runtime` 各 adapter 的 Notes）；
//     连接断开经 Client.Done() 收敛：进行中会话标记失败，持久会话是否可续依 runtime 能力
package manager
