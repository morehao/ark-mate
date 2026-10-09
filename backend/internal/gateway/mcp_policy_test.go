package gateway

// ——— M7-4 验收：命名策略 fail-closed、探测面/真实面对拍、调用留痕 ———

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/manager"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// TestToolsCollisionIsFailClosed 跨 server 的重名**整组丢弃**，不保留任何一个。
//
// 构造：server `a` 报一个叫 `b__x` 的工具（合名 `a__b__x`），server `a__b` 报一个叫 `x` 的工具
// （合名同样是 `a__b__x`）。名字与来源不再是双射：保留任何一个，都会让"调 a__b__x"的语义取决于
// 你问的是谁——故列举期直接不给这个名字。
func TestToolsCollisionIsFailClosed(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	t.Setenv("MCP_TOKEN_A", "tok-a")
	t.Setenv("MCP_TOKEN_B", "tok-b")
	srvA := newFakeRemote(t, "b__x", "safe_a")
	srvB := newFakeRemote(t, "x", "safe_b")
	r.seedMcp("a", httpDef("a", srvA.srv.URL, "MCP_TOKEN_A"), 0)
	r.seedMcp("a__b", httpDef("a__b", srvB.srv.URL, "MCP_TOKEN_B"), 0)
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"]}`)
	src := r.source("a1", nil)

	tools, err := src.Tools(ctx)
	if err != nil {
		t.Fatalf("取工具面: %v", err)
	}
	if got := toolNames(tools); strings.Join(got, ",") != "a__safe_a,a__b__safe_b" {
		t.Fatalf("冲突名 a__b__x 应整组丢弃、其余保留，got %v", got)
	}
	logs := r.log.String()
	if !strings.Contains(logs, "工具名跨 server 冲突") || !strings.Contains(logs, "a__b__x") {
		t.Fatalf("冲突必须响亮留痕（否则用户只看到'工具少了'）: %s", logs)
	}
	for _, want := range []string{"a→b__x", "a__b→x"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("冲突留痕应写清双方来源 %q：%s", want, logs)
		}
	}

	// 冲突名不在面里，所以模型**不会**从 tools/list 学到它。若它仍被调用（陈旧缓存/手工构造），
	// 解析走"最长 id 前缀"这个**唯一解**（较长的那台 server 优先）——确定性收敛，不是随机。
	// 这里如实钉住这条语义：它落到 `a__b` 的 `x` 上，而不是"两边都可能"。
	if _, err := src.Call(ctx, "a__b__x", nil); err != nil {
		t.Fatalf("最长前缀解读应给出确定结果: %v", err)
	}
	if calls, _ := srvB.snapshot(); len(calls) != 1 || calls[0] != "x" {
		t.Fatalf("应落到 a__b 的 x 上，got %v", calls)
	}
	if calls, _ := srvA.snapshot(); len(calls) != 0 {
		t.Fatalf("不该落到 a 上，got %v", calls)
	}
}

// TestProbeAndGatewayFacesMatch 对拍：同一个 remote server，探测面（`ProbeMcpServer` → Tool 目录）
// 与网关真实暴露面（`tools/list`）逐项一致——名字、描述、数量。
//
// 这是 M7-④ 的核心断言：两个面各写一套命名策略，"面板上看到的工具"与"模型能调的工具"就会再次
// 漂移，M7-③ 的同源收益白费。故两边都走 `capability.GatewayToolName`（一个实现）。
func TestProbeAndGatewayFacesMatch(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	t.Setenv("MCP_TOKEN_PARITY", "tok-parity")
	remote := newFakeRemote(t, "search", "fetch")
	r.seedMcp("gh", httpDef("gh", remote.srv.URL, "MCP_TOKEN_PARITY"), 0)
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"]}`)

	// 探测面：管理器装载同一份定义后探测（真实入口，走真远端）。
	svc := manager.NewService(runtime.DefaultRegistry(), nil)
	svc.EnableStore(r.st)
	if n := svc.LoadCapabilityDefinitionsFromStore(ctx, ""); n == 0 {
		t.Fatal("装载能力定义：应读到 1 条")
	}
	probe, err := svc.ProbeMcpServer(ctx, "gh")
	if err != nil {
		t.Fatalf("探测: %v", err)
	}
	probeNames := make([]string, 0, len(probe.Tools))
	probeDesc := map[string]string{}
	for _, tl := range probe.Tools {
		probeNames = append(probeNames, tl.Name)
		probeDesc[tl.Name] = tl.Description
	}

	// 真实面：网关同一 scope 的 tools/list。
	src := r.source("a1", nil)
	tools, err := src.Tools(ctx)
	if err != nil {
		t.Fatalf("取工具面: %v", err)
	}
	gatewayNames := toolNames(tools)
	// 比**集合**不比顺序：Tool 目录按 name 排序（`ListTools` 的既有口径），而网关面保持远端的
	// 服务端顺序——顺序不是契约，"哪些工具、叫什么、什么描述"才是。
	sort.Strings(probeNames)
	sort.Strings(gatewayNames)
	if strings.Join(probeNames, ",") != strings.Join(gatewayNames, ",") {
		t.Fatalf("探测面与网关面必须逐项一致：\n探测 %v\n网关 %v", probeNames, gatewayNames)
	}
	if len(gatewayNames) != 2 {
		t.Fatalf("应有两条工具（search/fetch），got %v", gatewayNames)
	}
	// 描述也要对得上（同一份定义、同一次 tools/list 的产物）。
	for _, tl := range tools {
		if probeDesc[tl.Name] != tl.Description {
			t.Fatalf("工具 %s 的描述不一致：探测 %q / 网关 %q", tl.Name, probeDesc[tl.Name], tl.Description)
		}
	}
}

// TestCallLeavesEvidence 每次 tools/call 留一条 (server, tool, 结果, 耗时) 的日志。
//
// 它是"底座何时重列工具面 / 收窄是否即时生效"（设计 §10.6-⑤）唯一的一手证据：网关无状态、
// 不落库，所以日志就是证据面。三条分线都要可分辨：ok / error / deny。
func TestCallLeavesEvidence(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	t.Setenv("MCP_TOKEN_EV", "tok-ev")
	remote := newFakeRemote(t, "echo")
	r.seedMcp("gh", httpDef("gh", remote.srv.URL, "MCP_TOKEN_EV"), 0)
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"]}`)
	src := r.source("a1", nil)

	if _, err := src.Call(ctx, "gh__echo", nil); err != nil {
		t.Fatalf("正常调用应成功: %v", err)
	}
	remote.setCallFail(true)
	if _, err := src.Call(ctx, "gh__echo", nil); err == nil {
		t.Fatal("远端拒绝时应回带错误")
	}
	// 未知来源（前缀对不上任何一台 scoped server）→ 直接否决，不落到远端。
	if _, err := src.Call(ctx, "nope__echo", nil); err == nil {
		t.Fatal("未知来源应报错")
	}

	logs := r.log.String()
	for _, want := range []string{
		"server=gh", "tool=echo", "outcome=ok", "outcome=error", "latency_ms=",
		"tool=nope__echo", "outcome=deny",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("调用留痕缺 %q：\n%s", want, logs)
		}
	}
}
