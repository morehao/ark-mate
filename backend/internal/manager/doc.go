// Package manager 编排 Agent 生命周期与会话执行：
//
//   - Service：实现 server/handler.Service（列表），数据来自 runtime 注册表 + 进程内会话/Agent 存储
//   - Orchestrator：把「对一次会话的 ACP 编排」（连接获取 → session/new → prompt →
//     事件流 → cancel/close）跑通，并把归一化事件 Publish 进 eventbus（agg = session id）
//   - 能力门控：load/resume/list/fork 依 acl.Capabilities 裁剪（harness 仅 fresh session），
//     「断连即失效」经 Client.Done() 收敛到会话失效处理
package manager
