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
//
// **「实现搬走了，文件为什么不见」的账目**（P4a 正文第五十二刀把只剩说明头的文件收口到这一段）：
//
//	skills_write.go / mcps_write.go → 技能与 MCP 的**写面**（含载体 CAS、启停 overlay、物化重校）
//	  全在 `domain/skill` / `domain/mcp` 的写面方法里；控制器直接 `h.skills.*` / `h.mcps.*`。
//	mcp_probe.go → 连接器**探测面**（预算、状态记账、`tools/list` 权威快照、启动期自动探测）
//	  在 `domain/mcp.Plane.Probe` / `.AutoProbe`；控制器直接 `h.mcpPlane`。
//	runtime_binding.go / gateway_face.go / definition_forward.go / credentials.go / skills.go
//	  五个**纯转发**文件（第四十九～五十一刀）已整体删除，调用点直连对应的域平面 / 机制 / 事实出口。
//
// 留这一段而不是留一堆空文件：文件列表要能回答「这里还剩什么代码」，而"实现去哪了"是本包注释
// 该回答的问题（每一族的判据都在 `internal/archtest` 里钉着，失锚会红）。
package manager
