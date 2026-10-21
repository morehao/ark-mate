// Package registry 是**装配层**：它拥有本次进程里跨域的服务实例（领域平面与资源同步面），
// 并把"按什么顺序启动、失败算不算致命、给多久、关闭按什么顺序"从 `internal/bootstrap/app` 的散文里
// 收成一条**声明序列**（P5a）。
//
// 为什么要有这一层：改造前 `app.Init` 是 380 行的顺序散文——启动顺序、critical 与否、
// 关闭顺序全部藏在"读下来就懂了"的控制流里。它的失效方式是静默的：
//
//	① 少起一个后台循环（空闲回收 / 自动化调度）⇒ 功能"看起来正常"，只是永远不触发；
//	② 关闭顺序与启动不对称 ⇒ 退出时偶发丢数据（transcript 尾部、在途轮次），而单测看不见；
//	③ 把只该 WARN 的步骤写成 critical ⇒ 用户机器上某个可选目录缺失就起不来。
//
// 三种都不会让既有测试变红。所以本包只做两件事：
//
//   - `Planes`：领域平面（能力 / 技能 / MCP / 供应商 / 工作空间 / 自动化 + 凭据与定义机制）
//     的**唯一构造点**。核心进程、网关子进程与 `mate doctor` 都从这里拿同一套事实，而不是
//     各自 `manager.NewService` 一遍（那正是"核心能注入的服务器，网关注入不了"的来源）。
//   - `Sequence`：启动/关闭各一条声明序列。Step 带阶段、critical 与超时；`Start` 在 critical
//     失败时按逆序回滚**已启动**的步骤，`Stop` 永远按启动的逆序执行。
//
// 本包同时是**窄端口适配体**的落点：`workspaceStore` / `skillDefs` / `mcpDefs` /
// `AutomationDefinitionFiles` / `providerSecrets`。它们只做「把机制的宽面收窄成域声明的端口」，
// 而这件事只有同时看得见两侧的装配点写得了（机制层禁止 import 域）。
//
// 本包**不认识 manager**：manager 反过来 import 它（过渡期），P4b 随 manager 解散后这里就是
// 装配面的最终落点。依赖方向单一：registry → domain/mechanism/store/pkg，绝不反向。
package registry
