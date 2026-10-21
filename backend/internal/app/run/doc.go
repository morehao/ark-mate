// Package run 编排 Agent 生命周期与会话执行：
//
//   - Service：实现 internal/app/* 声明的领域端口（**断言在装配点**：`app.go` 的
//     `router.Ports` 字面量，判据见下一段），数据来自 runtime 注册表 + 进程内会话存储。
//     P4a 起它**不 import** 用例层（`app/<领域>`）：哪些端口由它实现、哪些用例已经搬走，看装配点一行
//     就够（自检/导出/底座用例的实现已分别在 app/model、app/system、app/runtime）。
//   - Orchestrator：把「对一次会话的 ACP 编排」（连接获取 → session/new → prompt →
//     事件流 → cancel/close）跑通，并把归一化事件 Publish 进 eventbus（agg = session id）
//   - 能力门控：load/resume/list/fork 依 acp.Capabilities 裁剪（各底座声明的能力集不同，
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
//
// **编译期断言去哪了**（原先单独一个 `ports.go`，第五十三刀并进本文件）：这里曾有一组
// `var _ svc<领域>.XxxSvc = (*Service)(nil)`（一度 9 条），P4a 逐个删掉，理由两条——① **断言跟着
// 实现走**：哪一片实现搬进了 `app/<领域>`，断言就跟着搬进去（`system.Exporter` /
// `runtime.Runtime` / `model.Probe` 各自断言自己那片）；② **除控制器层外不许引用用例层
// （`app/<领域>`）**：`manager` 为实现层，只为写一行断言而 import 用例层，等于把一条本该单向的边
// 变成双向。断言本体没有消失，它**上移到了装配点**——`app.go` 的 `router.Ports` 字面量：`a.svc`
// 少实现任何一个端口的方法都是当场编译失败，且报错点就在"谁被交给了谁"那一行（比留在本包更硬：
// 本包的断言只证明"`*Service` 满足某个接口"，装配点证明的是"**被交给路由的那个对象**满足路由要的
// 每个端口"）。本包与端口的其余关系靠**结构满足**维持，改签名时两侧一起编译失败，失败点在装配点。
// （"再长回一行断言"这种事不需要新判据兜：`TestOnlyAssemblyAndControllersImportServicePackages` 已经在
// `usecase_layer_consumer_test.go` 里把"非控制器层引用用例层"钉死了。）
package run
