// Package manager 编排 Agent 生命周期与会话执行：
//
//   - Service：实现 server/handler.Service（列表），数据来自 runtime 注册表 + 进程内会话/Agent 存储
//   - Orchestrator：把「对一次会话的 ACP 编排」（连接获取 → session/new → prompt →
//     事件流 → cancel/close）跑通，并把归一化事件 Publish 进 eventbus（agg = session id）
//   - 能力门控：load/resume/list/fork 依 acl.Capabilities 裁剪（opencode 全会话能力；
//     deepseek-harness 支持 list/resume/close、不支持 load/fork，见 runtime 包 Notes）；
//     连接断开经 Client.Done() 收敛：进行中会话标记失败，持久会话是否可续依 runtime 能力
package manager
