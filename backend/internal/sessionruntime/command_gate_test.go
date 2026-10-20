package sessionruntime

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/session/event"
	"github.com/morehao/ark-mate/internal/infra/acp"
)

func gateRules(t *testing.T) []interaction.PrefixRule {
	t.Helper()
	rules, err := interaction.LoadCommandRules([]interaction.PrefixRule{
		{Name: "deny rm", Pattern: [][]string{{"rm"}}, Decision: interaction.CommandForbidden},
		{Name: "allow git status", Pattern: [][]string{{"git"}, {"status"}}, Decision: interaction.CommandAllow},
	})
	if err != nil {
		t.Fatalf("LoadCommandRules: %v", err)
	}
	return rules
}

func gateOptions() []interaction.PermissionOption {
	return []interaction.PermissionOption{
		{OptionID: "allow-once", Kind: "allow_once"},
		{OptionID: "allow-always", Kind: "allow_always"},
		{OptionID: "reject-once", Kind: "reject_once"},
		{OptionID: "reject-always", Kind: "reject_always"},
	}
}

// TestCommandGateDecision 门禁的三种终局与"不参与"。
func TestCommandGateDecision(t *testing.T) {
	o := &Orchestrator{commandPolicy: gateRules(t)}
	opts := gateOptions()

	d, rule, decided := o.commandGateDecision(event.ToolCall{
		ID: "c1", Input: map[string]any{"command": "rm -rf /tmp/x"},
	}, opts)
	if !decided || !d.Rejected || d.OptionID != "reject-once" || rule != "deny rm" {
		t.Fatalf("forbidden: got (%+v, %q, %v)，want rejected + reject-once", d, rule, decided)
	}

	d, rule, decided = o.commandGateDecision(event.ToolCall{
		ID: "c2", Input: map[string]any{"command": "git status"},
	}, opts)
	if !decided || !d.Allowed || d.OptionID != "allow-once" || rule != "allow git status" {
		t.Fatalf("allow: got (%+v, %q, %v)，want allowed + allow-once", d, rule, decided)
	}

	if _, _, decided := o.commandGateDecision(event.ToolCall{
		ID: "c3", Input: map[string]any{"command": "ls -la"},
	}, opts); decided {
		t.Fatal("没命中规则必须交回既有流程（不能自己放行）")
	}
	if _, _, decided := o.commandGateDecision(event.ToolCall{
		ID: "c4", Input: map[string]any{"command": "git status; rm -rf /"},
	}, opts); decided {
		t.Fatal("复杂命令必须交回人工审批（不能按空白切分猜）")
	}
	if _, _, decided := o.commandGateDecision(event.ToolCall{
		ID: "c5", Input: map[string]any{"path": "/x"},
	}, opts); decided {
		t.Fatal("非 shell 命令不归门禁管")
	}

	// 没配置规则 = 不参与（行为与改前逐字相同）。
	plain := &Orchestrator{tools: session.NewToolCallMemory()}
	if _, _, decided := plain.commandGateDecision(event.ToolCall{
		ID: "c6", Input: map[string]any{"command": "rm -rf /"},
	}, opts); decided {
		t.Fatal("未配置规则时门禁不得参与")
	}
}

// TestCommandGatePrecedesPermissionMemory 门禁排在会话级记忆之前：
// 同一键已被"始终允许"记住，规则说 forbidden 的命令仍然被拒。
func TestCommandGatePrecedesPermissionMemory(t *testing.T) {
	o := &Orchestrator{
		commandPolicy: gateRules(t),
		perm:          interaction.NewPermissionManager(interaction.DefaultPermissionTimeout),
		handles:       session.NewHandles(),
		tools:         session.NewToolCallMemory(),
	}
	tool := event.ToolCall{ID: "c1", Title: "bash", Kind: "execute",
		Input: map[string]any{"command": "rm -rf /tmp/x"}}
	o.tools.Remember(tool)
	// 记忆里先放一条"这类工具始终允许"。
	o.perm.Remember("ses_1", tool, "allow", "allow-always")

	req := acp.PermissionRequest{
		SessionID: "ses_1",
		ToolCall:  acp.ToolCallInfo{ID: "c1", Title: "bash", Kind: "execute"}, // 请求只带 id/标题
		Options:   acpGateOptions(),
	}
	d := o.requestPermission(context.Background(), req)
	if !d.Rejected || d.Allowed {
		t.Fatalf("门禁拒绝必须先于记忆放行：got %+v（记忆若先裁决，这条 rm 会被静默放行）", d)
	}
}

// TestToolCallMemoryMergesAndForgets 缓存的三条语义：合并非空字段 / 终态删除 / 有界。
func TestToolCallMemoryMergesAndForgets(t *testing.T) {
	o := &Orchestrator{tools: session.NewToolCallMemory()}
	o.tools.Remember(event.ToolCall{ID: "c1", Title: "bash", Kind: "execute",
		Input: map[string]any{"command": "ls"}})
	// 后续 update 只带 status/正文：不得把 Input 抹掉。
	o.tools.Remember(event.ToolCall{ID: "c1", Status: acp.ToolStatusInProgress, ContentText: "out"})

	got, ok := o.tools.Get("c1")
	if !ok || got.Title != "bash" || got.ContentText != "out" {
		t.Fatalf("合并结果不符: %+v", got)
	}
	if _, ok := got.Input.(map[string]any); !ok {
		t.Fatalf("Input 被 status-only update 抹掉了: %#v", got.Input)
	}

	o.tools.Forget("c1")
	if _, ok := o.tools.Get("c1"); ok {
		t.Fatal("forget 之后仍能查到")
	}

	for i := 0; i < session.MaxRememberedToolCalls+20; i++ {
		o.tools.Remember(event.ToolCall{ID: "bulk_" + strconv.Itoa(i)})
	}
	if o.tools.Len() > session.MaxRememberedToolCalls {
		t.Fatalf("缓存无界: %d > %d", o.tools.Len(), session.MaxRememberedToolCalls)
	}
	if _, ok := o.tools.Get("bulk_0"); ok {
		t.Fatal("最旧的条目应被淘汰")
	}
	if _, ok := o.tools.Get("bulk_" + strconv.Itoa(session.MaxRememberedToolCalls+19)); !ok {
		t.Fatal("最新的条目必须还在")
	}
}

// TestToolCallMemoryConcurrent 并发写/读/删：事件回调与权限裁决在不同 goroutine。
// 判据在 `-race` 下（`make test-race` 含本包）；无 `-race` 时验证不 panic、不丢最终状态。
func TestToolCallMemoryConcurrent(t *testing.T) {
	o := &Orchestrator{tools: session.NewToolCallMemory()}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				id := "c" + strconv.Itoa(w) + "_" + strconv.Itoa(i%20)
				o.tools.Remember(event.ToolCall{ID: id, Title: "bash",
					Input: map[string]any{"command": "git status"}})
				_, _ = o.tools.Get(id)
				if i%7 == 0 {
					o.tools.Forget(id)
				}
			}
		}(w)
	}
	wg.Wait()
	if _, ok := o.tools.Get(""); ok {
		t.Fatal("空 id 永远不该命中缓存")
	}
}

// acpGateOptions 是同一份选项夹具的**协议形状**：`requestPermission` 的入参仍是 ACP 请求
// （翻译在函数内部做），所以这条用例需要两份同值的选项。
func acpGateOptions() []acp.PermissionOption {
	out := make([]acp.PermissionOption, 0, 4)
	for _, o := range gateOptions() {
		out = append(out, acp.PermissionOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}
	return out
}
