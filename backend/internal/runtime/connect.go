package runtime

import (
	"strings"
)

// Connect 依投影 Spec 拉起 ACP server 子进程（stdio）并完成 initialize 握手。
// 这是「可执行半场」的拨号底座：Runtime 接口（§4.2）落地时各投影复用同一拉起/握手路径，
// 决策钩子（权限/elicitation/事件归集）仍由调用方经 ClientOptions 注入。
//
// 参数：
//   - args：已解析启动参数（调用方先做 {config} 替换；现三投影参数均无该 token）；
//   - env：调用方附加环境（如 provider 密钥注入），内部叠加在 spec.Launch.Env 之后；
//
// LaunchesLocally 报告该 runtime 是否由平台**在本机拉起子进程**（`Launch.Transport` 为空或
// `stdio`）。判据与 `Connect` 的传输分派**同源**（同一个 switch 的两支）：凡是本机起进程的形态
// → true，远程端点形态（http/ws/streamable_http…）→ false。
//
// 为什么需要它：平台侧 MCP 网关条目是一条**本机 stdio 命令**（`<平台可执行文件> mcp bridge …`）。
// 把它下发给远程 ACP 端点，等于让对端按我们机器上的路径去起进程——对端根本没有那个文件。
func LaunchesLocally(spec Spec) bool {
	switch strings.ToLower(strings.TrimSpace(spec.Launch.Transport)) {
	case "", "stdio":
		return true
	default:
		return false
	}
}

// RemoteSpec 构造一条「远程 ACP 端点」runtime 的投影 Spec（A5）。
//
// 与内置 adapter 的区别：不拉本地子进程，Launch.Transport/Endpoint 直接描述远端端点；
// 其余能力（Caps/ManagesOwnContext 等）取保守基线——真实能力仍以 initialize 探测为准。
func RemoteSpec(id, name, transport, endpoint string, headers map[string]string) Spec {
	tr := strings.ToLower(strings.TrimSpace(transport))
	if tr == "" {
		tr = "ws"
	}
	label := "remote " + tr
	return Spec{
		ID:      id,
		Name:    firstNonEmptyStr(name, id),
		Version: label + " " + endpoint,
		Launch: LaunchSpec{
			Transport: tr,
			Endpoint:  endpoint,
			Headers:   headers,
		},
		Description:       "远程 ACP 端点（" + tr + "：" + endpoint + "）",
		McpTransports:     []string{"stdio"}, // MCP 注入面由远端 agent 决定，平台侧保守声明
		ManagesOwnContext: true,
		Notes:             "远程 runtime：由 .ark-mate/config/runtimes.json 声明（A5）",
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ConnectEnv 是交给连接的最终附加环境：**平台声明（`spec.Launch.Env`）优先于调用方增量**。
//
// 为什么这条规则留在契约包：它约束的是 `LaunchSpec.Env` 的语义（设计 §6.6 的配置隔离，M3-3），
// 而 `LaunchSpec` 住在这里；`connectEnv` 原先只在拨号路径用，拨号搬走之后它仍作为
// "Spec 的语义"导出给执行面。语义本身：base 已有的键不被 add 覆盖，且同名只保留一条
// （POSIX `getenv` 取第一条、Node `process.env` 取最后一条，留着两条就是"同一环境两种行为"）。
func ConnectEnv(spec Spec, env []string) []string {
	return mergeEnv(spec.Launch.Env, env)
}
