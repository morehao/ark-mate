package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
)

// brokenRuntimeSpec 一个「起得来但握手必然失败」的 runtime：写 stderr 后立即退出。
func brokenRuntimeSpec() runtime.Spec {
	return runtime.Spec{
		ID:     "broken",
		Name:   "Broken",
		Launch: runtime.LaunchSpec{Command: "sh", Args: []string{"-c", "echo '[warn] broken runtime 起不来' >&2; exit 7"}},
	}
}

// stubRuntimeSpec 一个最小可用的 ACP 桩（python 实现）：stderr 写诊断行，initialize 后驻留。
func stubRuntimeSpec(t *testing.T) runtime.Spec {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "acp_stub.py")
	code := `import json, sys
sys.stderr.write("[warn] live-tail line\n")
sys.stderr.flush()
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        continue
    if msg.get("method") == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": msg.get("id"), "result": {"protocolVersion": 1, "agentCapabilities": {}}}) + "\n")
        sys.stdout.flush()
`
	if err := os.WriteFile(script, []byte(code), 0o600); err != nil {
		t.Fatalf("写桩脚本失败: %v", err)
	}
	return runtime.Spec{ID: "stub", Name: "Stub", Launch: runtime.LaunchSpec{Command: "python3", Args: []string{script}}}
}

// TestAgentDiagnosticsStartFailure 启动失败 → 诊断快照带 stderr 尾巴与最近失败（A3）。
func TestAgentDiagnosticsStartFailure(t *testing.T) {
	reg := runtime.NewRegistry(brokenRuntimeSpec())
	svc := NewService(reg, eventbus.New())
	svc.agents["a-broken"] = &AgentRec{ID: "a-broken", Name: "Broken", Runtimes: []string{"broken"}}
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)

	if err := svc.StartAgent(context.Background(), "a-broken"); err == nil {
		t.Fatal("期望启动失败")
	}
	diag, err := svc.AgentDiagnostics(context.Background(), "a-broken")
	if err != nil {
		t.Fatalf("AgentDiagnostics: %v", err)
	}
	if diag.State != AgentStateStopped || diag.Connected || diag.Runtime != "broken" {
		t.Fatalf("诊断态不符: %+v", diag)
	}
	if diag.LastError == nil || diag.LastError.Class == "" || diag.LastError.Code == "" {
		t.Fatalf("应记录最近失败: %+v", diag.LastError)
	}
	if diag.StderrSource != "last_error" || len(diag.StderrTail) == 0 {
		t.Fatalf("应外露 last_error 尾巴: %+v", diag)
	}
	if !strings.Contains(strings.Join(diag.StderrTail, "\n"), "broken runtime 起不来") {
		t.Fatalf("尾巴内容不符: %v", diag.StderrTail)
	}
	if diag.Command != "sh" || diag.RuntimeName != "Broken" {
		t.Fatalf("runtime 元信息不符: %+v", diag)
	}

	// 未知 agent → 明确错误（handler 侧映射 40001/40401）。
	if _, err := svc.AgentDiagnostics(context.Background(), "nope"); err == nil {
		t.Fatal("未知 agent 应报错")
	} else if ae, ok := acl.As(err); !ok || ae.Code != "agent_not_found" {
		t.Fatalf("错误码不符: %v", err)
	}
}

// TestAgentDiagnosticsLiveTail 已连接 runtime → 外露实时 stderr 尾巴（A3，真实子进程 + 真实 ACP 握手）。
func TestAgentDiagnosticsLiveTail(t *testing.T) {
	reg := runtime.NewRegistry(stubRuntimeSpec(t))
	svc := NewService(reg, eventbus.New())
	svc.agents["a-stub"] = &AgentRec{ID: "a-stub", Name: "Stub", Runtimes: []string{"stub"}}
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)

	if err := svc.StartAgent(context.Background(), "a-stub"); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	diag, err := svc.AgentDiagnostics(context.Background(), "a-stub")
	if err != nil {
		t.Fatalf("AgentDiagnostics: %v", err)
	}
	if !diag.Connected || diag.PID <= 0 || diag.State != AgentStateRunning {
		t.Fatalf("连接态不符: %+v", diag)
	}
	if diag.StderrSource != "live" || len(diag.StderrTail) == 0 {
		t.Fatalf("应外露 live 尾巴: %+v", diag)
	}
	if !strings.Contains(strings.Join(diag.StderrTail, "\n"), "live-tail line") {
		t.Fatalf("尾巴内容不符: %v", diag.StderrTail)
	}
	if diag.LastError != nil {
		t.Fatalf("成功启动不应有 last_error: %+v", diag.LastError)
	}

	// 停掉后连接态归位（尾巴回落到空/none）。
	if err := svc.StopAgent(context.Background(), "a-stub"); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
	diag2, err := svc.AgentDiagnostics(context.Background(), "a-stub")
	if err != nil {
		t.Fatalf("AgentDiagnostics(stopped): %v", err)
	}
	if diag2.Connected || diag2.PID != 0 {
		t.Fatalf("停止后不应再报连接: %+v", diag2)
	}
	if diag2.StderrSource != "none" || len(diag2.StderrTail) != 0 {
		t.Fatalf("停止后尾巴应清空: %+v", diag2)
	}
}

// TestPublishErrorCarriesStderrTail 轮次失败事件携带 stderr 尾巴（WS/历史/前端错误卡数据面）。
func TestPublishErrorCarriesStderrTail(t *testing.T) {
	reg := runtime.NewRegistry()
	bus := eventbus.New()
	svc := NewService(reg, bus)
	svc.agents["a-x"] = &AgentRec{ID: "a-x", Name: "X"}
	svc.registerConversation(&ConversationRec{ID: "c-x", AgentID: "a-x"})
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)

	ae := acl.NewError(acl.ClassConnection, "spawn_failed", "runtime 起不来", nil)
	ae.StderrTail = []string{"line-1", "line-2"}
	orch.publishError("c-x", ae, nil)

	var got map[string]any
	for _, e := range bus.EventsAfter(0) {
		if e.Type == BusTurnError && e.AggregID == "c-x" {
			got, _ = e.Payload.(map[string]any)
		}
	}
	if got == nil {
		t.Fatal("未发布 turn.error")
	}
	tail, _ := got["stderr_tail"].([]string)
	if len(tail) != 2 || tail[1] != "line-2" {
		t.Fatalf("事件载荷应带 stderr 尾巴: %+v", got)
	}
	// 同一失败也应进入 agent 诊断快照（会话 → agent 反查）。
	le, ok := svc.agentLastError("a-x")
	if !ok || le.ConversationID != "c-x" || len(le.StderrTail) != 2 {
		t.Fatalf("诊断快照不符: %+v ok=%v", le, ok)
	}
}
