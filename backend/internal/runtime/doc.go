// Package runtime defines the adapter layer over multiple agent runtimes:
//
//   - contracts:   统一能力/启动描述（Capabilities / LaunchSpec / Spec）
//   - registry:    注册表（map[string]Spec），供控制面列出 runtime 与按名取启动配置
//   - adapters/{dsh,...}: 各 runtime 的启动命令/参数/能力基线声明（纯数据，不依赖父包）；
//     接一个新底座 = 新建一个子包 + 在 adapters.go 汇入一份 Spec + 挂端口实现
//
// 真实能力以 ACP initialize 的探测结果为准（internal/adapters/outbound/acp.Capabilities）；
// 此处声明仅用于「不拉起进程也能回答能力基线」（REST /runtimes 与 M2 的 fingerprint 校验）。
package runtime
