// Package svcworkspace 定义工作空间面的服务端口（**已清空**：目录枚举与创建、最近使用派生、
// 登记表读写、设置面读写全部归 `domain/workspace` 的平面，控制器直接经 `WorkspacePlane()` 调用）。
package svcworkspace

// WorkspaceSvc 工作空间面的服务端口 —— **已经空了**。
//
// 本域的实现全部落在 `internal/domain/workspace` 的平面里：目录枚举与创建、最近使用派生、
// 登记表十条、策略读写、设置面读写（P4a 正文第十八～三十二刀逐条搬完）。控制器直接持有
// `WorkspacePlane()` 这个事实出口，不需要中间一层。
//
// **留着这个空接口只是为了不把这一刀摊大**：`router.Ports.Workspace` 与
// `ctrworkspace.NewWorkspaceCtr` 的第一个参数还指着它（一个空接口在任何地方都能满足，所以
// 编译器不会提醒任何人）。下一刀把这条端口从 `Ports` 与构造函数里摘掉，然后删掉本文件。
type WorkspaceSvc interface{}
