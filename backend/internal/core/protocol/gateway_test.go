package protocol

import (
	"reflect"
	"strings"
	"testing"
)

// TestGatewayBridgeArgsRoundTrip 网关条目参数形态的**往返判据**（决策 v31 的 A1）。
//
// 为什么这条判据必须住在**形状所在的包**：`GatewayBridgeArgs` 与 `ParseScopeArgs` 是同一份
// 契约的两侧，注入端（`sessionruntime` 拼条目）与网关端（`drivers/gateway` + `mate mcp bridge`
// 解参数）各自把这条契约理解错一半时，两边都能编译、各自的测试都能绿——症状只有
// "网关起来了但 scope 不对"，表现成工具面不对，是最难查的一类。判据曾经住在
// `infra/protocol/mcp`（形状搬到 `core/protocol` 之前的旧家），搬家时没跟过来：那种位置
// 在协议包被重组/测试被删时会**静默失去覆盖**，而形状本身看起来仍有测试。
//
// 三段：① 三态往返逐字保真；② 参数漂移必须**报错**而不是静默忽略；③ 条目名的稳定性
// （它是底座会话里可见的标识，改了等于所有既有会话换一个 server）。
func TestGatewayBridgeArgsRoundTrip(t *testing.T) {
	// ① 三态：nil = 该会话没钉（全量）/ `[]` = 本会话一个远程 MCP 都不要 / [ids] = 白名单。
	// 三态退化的后果不对称：`[]` 解成 nil 会把"一个都不要"放大成"全量远程面"（授权放大），
	// 而 nil 解成 `[]` 会让缺省路径的工具面凭空消失（`TestGatewayBridgeArgsRoundTrip` 之外
	// 没有别的地方能发现它）。
	pinned := []string{"gh", "pg"}
	empty := []string{}
	// 白名单的 id 词法不受限（server id 允许空格等字符）：选 JSON 数组而不是逗号分隔，
	// 就是为了让这些值能**无歧义**往返，所以这里直接把歧义字符放进用例。
	tricky := []string{"gh server", "a,b", `quote"d`, "本地-server"}

	for _, tc := range []struct {
		name string
		sc   GatewayScope
	}{
		{name: "未钉", sc: GatewayScope{RuntimeID: "r1"}},
		{name: "显式空集", sc: GatewayScope{RuntimeID: "r1", McpsPinned: &empty}},
		{name: "白名单", sc: GatewayScope{RuntimeID: "r1", McpsPinned: &pinned}},
		{name: "歧义字符", sc: GatewayScope{RuntimeID: "r1", McpsPinned: &tricky}},
	} {
		args := GatewayBridgeArgs(tc.sc)
		// 子命令前缀是 CLI 分发的入口（`mate mcp bridge`），与 cli 侧同形才有意义。
		if len(args) < 2 || args[0] != "mcp" || args[1] != "bridge" {
			t.Fatalf("%s：子命令形态不符: %v", tc.name, args)
		}
		got, err := ParseScopeArgs(args[2:])
		if err != nil {
			t.Fatalf("%s：拼出来的参数解不回来: %v（args=%v）", tc.name, err, args)
		}
		// McpsPinned 是指针，DeepEqual 能区分 nil 与「非 nil 空集」——三态就靠这一句钉住。
		if !reflect.DeepEqual(got, tc.sc) {
			t.Fatalf("%s：往返不符\n got: %#v\nwant: %#v", tc.name, got, tc.sc)
		}
	}

	// ② 参数漂移必须当场失败：静默忽略会让网关"起来了但 scope 不对"。
	for _, bad := range [][]string{
		{},                                      // 缺 scope（--runtime 必填）
		{"--runtime"},                           // 缺取值
		{"--runtime", "r1", "-x"},               // 未知参数
		{"--servers", "a,b"},                    // P-2 的反例：定义集绝不能进条目参数
		{"--agent", "a1", "--runtime", "r1"},    // v19 撤销的 agent 面
		{"--runtime", "r1", "--mcps", "a,b"},    // 非 JSON 数组
		{"--runtime", "r1", "--mcps", `["a",]`}, // 坏 JSON
		{"--runtime", "r1", "--mcps", `[" "]`},  // 空 id（显式空集请写 []）
	} {
		if _, err := ParseScopeArgs(bad); err == nil {
			t.Fatalf("%v 应报错（未知/坏参数不许静默忽略）", bad)
		}
	}

	// ③ 条目名是底座会话里可见的稳定标识。
	if GatewayServerName != "mate-gateway" {
		t.Fatalf("条目名不该改（既有会话认得它）: %q", GatewayServerName)
	}
	// 凭据 cwd 的 env 键名同样两头共用：注入端写、网关端读，改一半就静默拿到空值。
	if GatewayCredCwdEnv != "MATE_MCP_CRED_CWD" {
		t.Fatalf("凭据 cwd 的 env 名两头共用，不该改: %q", GatewayCredCwdEnv)
	}
	// 凭据 cwd 走 Env 而非 Args 的理由：Args 整体进能力指纹，Env 只把键名入指纹
	// （`mcpFingerprintParts` 的 EnvKeys）——**值**变了不该换代际。这里只钉住"不许出现在
	// 参数里"，代价是把这条语义变成一个可执行的断言而不是一段注释。
	for _, a := range GatewayBridgeArgs(GatewayScope{RuntimeID: "r1", McpsPinned: &pinned}) {
		if strings.Contains(a, GatewayCredCwdEnv) {
			t.Fatalf("%s 是条目的 env 键，不该出现在 Args 里（会进能力指纹）: %v", GatewayCredCwdEnv, a)
		}
	}
}
