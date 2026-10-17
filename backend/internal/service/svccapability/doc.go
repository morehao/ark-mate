// Package svccapability 承载能力数据面的**用例**：Skill / MCP server / Tool 目录的读模型。
//
// # 为什么这里没有 `CapabilitySvc`
//
// 这个包一度是"能力数据面的服务端口"，接口上最多挂过 16 个方法。它们逐刀归位（P4a 正文
// 第十二刀 ~ 第三十九刀），判据只有一条：**投影在哪一边**。
//
//   - 投影在 `pkg/` 之上（`capability.Tool`/`capability.McpServer` → `dtocapability.*`）⇒ 只能落
//     用例层，因为 `pkg/` 不能 import `internal/dto`：这就是本包的 `Tools`（第十二刀）与
//     `Servers`（第十四刀）——两个真用例，各自声明自己的窄接口。
//   - 投影与错误码**本来就在域平面里**（`skill.Plane.ListSkills` 已经把 `capability.Skill`
//     折成 DTO）⇒ 用例层再包一层只会得到一行转发，那正是 AGENTS.md 第三条硬规矩禁的形状：
//     技能启停/物化（第三十六刀）、定义写面六条（第三十七刀）、探测面（第三十八刀）、
//     MCP live 五条（第三十九刀）都按这条删掉了"声明 + 转发"。
//
// 最后一刀删的是**接口本身**：一个空接口在任何地方都能满足，编译器不会提醒任何调用点，留着
// 只会让人以为"能力面还有一个端口"。同批摘掉的还有 `router.Ports.Capability` 与
// `capabilityCtr.svc` 两格——留下任何一格，这个空接口都还能被"重新填上"。
package svccapability
