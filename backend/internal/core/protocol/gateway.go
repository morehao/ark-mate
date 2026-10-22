package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ——— 平台侧 MCP 网关条目的参数形态（A1，2026-10-22）———
//
// 这些是**平台自己拼 bridge 命令**的形状，不是 MCP 客户端协议的一部分：注入端
// （`sessionruntime` 的网关条目合成）与网关端（`drivers/gateway` + `mate mcp bridge`）
// 必须共用同一份定义，否则两侧各自"照注释实现"迟早漂移——症状是"网关起来了但 scope 不对"，
// 表现为工具面不对，最难查的一类。
//
// 为什么住在 `core/protocol`（原先在 `infra/protocol/mcp`）：形状的**两端**都是内圈代码
// （`sessionruntime` 拼条目、`drivers/gateway` 解参数），而 `infra` 的协议包是"跟外面说话的
// 实现"——把平台装配形状挂在它身上，等于让内圈为了一份参数约定去认识协议客户端
// （分层方案 §0.3 的 A1）。`infra/protocol/mcp` 保留别名，协议侧代码读起来照旧。
//
// 判据：本文件是**纯值 + 纯计算**（只有标准库），不 import 任何 `infra/*`。

// GatewayServerName 网关条目在 ACP `mcpServers` 里的 name。
//
// 常量放这里而不是 manager：注入端要用它拼条目、底座会在会话里显示它，网关自己
// （instructions/日志）也要用；两份字面量迟早漂移。
const GatewayServerName = "mate-gateway"

// GatewayCredCwdEnv 核心经网关条目的 `env` 下发的**凭据 cwd**（平台装配值，不是密钥）。
//
// 语义：`<cwd>/.env` 那一层的 `cwd` = **用户启动 `mate serve` 的那个目录**。
//
// 为什么必须由核心显式交下去，而不是网关自己 `os.Getwd()`：网关是**底座 spawn 的子进程**，
// 它的 cwd（通常是会话工作区）是用户没选过的目录；两个进程各自取自己的 cwd，同一个引用就会
// 解析出不同的值——而这件事**完全静默**（两边各自的测试都绿）。同一个引用必须给同一个答案。
//
// 为什么走条目的 `env` 而不是命令行参数：`Args` 整体进能力指纹（`mcpFingerprintParts` 逐字带上
// `Command`/`Args`），而 cwd 是**值**——换个目录启动 `mate serve` 不该让所有会话换一次底座进程；
// `Env` 只把**键名**入指纹（`EnvKeys`），键名恒定、取值不入，正好是"改指向不换代际"这条语义。
//
// 刻意**不进** `runtime/isolation.go` 的隔离表：那张表管的是"别让底座的子进程去读写用户
// 自己的 home"，而这是我们自己的装配值，随条目一对一携带（也因此不依赖进程环境继承是否完整）。
const GatewayCredCwdEnv = "MATE_MCP_CRED_CWD"

// GatewayScope 网关条目的 scope：注入端拼参数、网关端解参数，**两侧共用这一个类型**。
type GatewayScope struct {
	RuntimeID string
	// McpsPinned 会话钉住的 MCP 白名单（`sessions.capability_json` 的 `mcps`）：
	//
	//	nil    = 该会话**没有**钉（全量；缺省路径就是这一支）
	//	非 nil = 以它为准（空切片 = 本会话一个远程 MCP 都不要）
	//
	// 为什么要带上它：会话钉住的白名单是一条**用户约束**（可能比 agent 声明更窄，也可能更宽）。
	// 不带它的后果不是"少几个工具"，而是**按 agent 默认面把授权放大**——钉过 `["a"]` 的会话会
	// 看到全部远程 server。它与 P-2 的反例（`--servers a,b,c`）不是一回事：那条禁的是把
	// **定义集**写进条目（增删一个 server 就换条目/换代际）；这里带的是**用户的选定**，
	// 它不因定义集变化而变化，钉住同一份选定的多个会话得到逐字相同的条目。
	McpsPinned *[]string
}

// GatewayBridgeArgs 拼网关子命令的参数（不含可执行文件名）。
//
// **不含定义集**——这是本计划最关键的一条（P-2）：条目一旦只由 scope（runtime [+ 会话钉住的
// 白名单]）决定，"加一个远程 MCP server"就不改变条目、也就不换代际。反例 `--servers a,b,c`
// 会让每次增删都换一次代际，直接毁掉网关的头号收益。
//
// v19 起 scope 里没有 `--agent`：会话不再有 agent 身份，底座由构建期固定（`--runtime` 就是它）。
func GatewayBridgeArgs(sc GatewayScope) []string {
	args := []string{"mcp", "bridge", "--runtime", sc.RuntimeID}
	if sc.McpsPinned != nil {
		// JSON 数组：能**无歧义**表达"显式空"（`[]` = 一个都不要）与任意 id 词法
		// （server id 允许空格等字符，逗号分隔会歧义）。这里不走 shell，故无需转义。
		b, err := json.Marshal(*sc.McpsPinned)
		if err != nil {
			// []string 的 Marshal 不会失败；真失败了也不能拼出半截参数——回退成显式空集
			// （更窄的一支，宁可少给工具也不放大授权）。
			b = []byte("[]")
		}
		args = append(args, "--mcps", string(b))
	}
	return args
}

// ParseScopeArgs 解析 GatewayBridgeArgs 拼出的那串参数（两侧同形，见 GatewayBridgeArgs）。
//
// 未知参数**报错而不是忽略**：参数漂移（比如将来加了 `--servers` 而网关端还没实现）必须
// 当场失败——静默忽略会让网关"起来了但 scope 不对"，症状是"工具面不对"，最难查的一类。
func ParseScopeArgs(args []string) (GatewayScope, error) {
	var sc GatewayScope
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--runtime", "--mcps":
			if i+1 >= len(args) {
				return GatewayScope{}, fmt.Errorf("%s 缺少取值", args[i])
			}
			v := args[i+1]
			i++
			switch args[i-1] {
			case "--runtime":
				sc.RuntimeID = v
			case "--mcps":
				var pinned []string
				if err := json.Unmarshal([]byte(v), &pinned); err != nil {
					return GatewayScope{}, fmt.Errorf("--mcps 须为 JSON 数组（未知取值 %q）：%w", v, err)
				}
				if pinned == nil {
					pinned = []string{} // `[]` 必须解成"非 nil 的空集"，否则三态退化
				}
				sc.McpsPinned = &pinned
			}
		default:
			return GatewayScope{}, fmt.Errorf("未知参数 %q", args[i])
		}
	}
	if sc.RuntimeID == "" {
		return GatewayScope{}, errors.New("--runtime 必填（网关条目的 scope：注入时由平台拼出）")
	}
	for _, id := range derefPinned(sc.McpsPinned) {
		if strings.TrimSpace(id) == "" {
			return GatewayScope{}, errors.New("--mcps 里不得有空 id（显式空集请写 []）")
		}
	}
	return sc, nil
}

// derefPinned 空安全取值（nil → nil）。
func derefPinned(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}
