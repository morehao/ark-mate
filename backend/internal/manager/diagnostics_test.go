package manager

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/session"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
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
    elif msg.get("method") == "session/new":
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": msg.get("id"), "result": {"sessionId": "stub-1"}}) + "\n")
        sys.stdout.flush()
`
	if err := os.WriteFile(script, []byte(code), 0o600); err != nil {
		t.Fatalf("写桩脚本失败: %v", err)
	}
	return runtime.Spec{ID: "stub", Name: "Stub", Launch: runtime.LaunchSpec{Command: "python3", Args: []string{script}}}
}

// TestRuntimeDiagnosticsStartFailure 底座起不来 → 诊断快照带 stderr 尾巴与最近失败（A3）。
//
// v19：诊断对象是底座本身（构建期固定），触发方式也随之从"启动某个 agent"变成"建一个会话"——
// 底座是被会话拉起来的，那才是它唯一的启动路径。
func TestRuntimeDiagnosticsStartFailure(t *testing.T) {
	reg := runtime.NewRegistry(brokenRuntimeSpec())
	svc := NewService(reg, eventbus.New())
	setBuildRuntime(t, "broken") // 夹具注册表只有 broken
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)

	if _, err := orch.startSessionOn(context.Background(), StartOptions{Cwd: t.TempDir()}); err == nil {
		t.Fatal("期望底座起不来")
	}
	diag, err := svc.RuntimeDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("RuntimeDiagnostics: %v", err)
	}
	if diag.State != session.AgentStateStopped || diag.Connected || diag.Runtime != "broken" {
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
	// 诊断面不再收 id：路径里带 id 只会暗示"可以选底座"，而底座是构建期固定的。
}

// TestRuntimeDiagnosticsLiveTail 已连接 → 外露实时 stderr 尾巴（A3，真实子进程 + 真实 ACP 握手）。
func TestRuntimeDiagnosticsLiveTail(t *testing.T) {
	reg := runtime.NewRegistry(stubRuntimeSpec(t))
	svc := NewService(reg, eventbus.New())
	setBuildRuntime(t, "stub") // 夹具注册表只有 stub
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)

	if _, err := orch.startSessionOn(context.Background(), StartOptions{Cwd: t.TempDir()}); err != nil {
		t.Fatalf("建会话: %v", err)
	}
	diag, err := svc.RuntimeDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("RuntimeDiagnostics: %v", err)
	}
	if !diag.Connected || diag.PID <= 0 || diag.State != session.AgentStateRunning {
		t.Fatalf("连接态不符: %+v", diag)
	}
	if diag.StderrSource != "live" {
		t.Fatalf("已连接的 runtime 应报 live 尾巴: %+v", diag)
	}
	// live 尾巴是**流**，不是终态快照：子进程写 stderr 与主线程读它有真实竞态，握手完成
	// 不代表那一行已被读走（夹具脚本先写 stderr 再握手，但两个方向各自排队）。
	// 这里给一个有界等待而不是放松断言——断言仍然是"那一行必须出现"。
	waitForLiveTail(t, svc, "live-tail line")
	if diag.LastError != nil {
		t.Fatalf("成功启动不应有 last_error: %+v", diag.LastError)
	}

	// 停机后连接态归位（尾巴回落到空/none）。主动拆除不是失败 → 不写 last_error。
	orch.Close()
	diag2, err := svc.RuntimeDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("RuntimeDiagnostics(stopped): %v", err)
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
	svc.registerSession(&SessionRec{ID: "c-x"})
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)

	ae := acl.NewError(acl.ClassConnection, "spawn_failed", "runtime 起不来", nil)
	ae.StderrTail = []string{"line-1", "line-2"}
	orch.publishError("c-x", "run_test0000000000000000000001", ae, nil)

	var got map[string]any
	for _, e := range bus.EventsAll() {
		if e.Type == session.BusRunError && e.AggregID == "c-x" {
			got, _ = e.Payload.(map[string]any)
		}
	}
	if got == nil {
		t.Fatal("未发布 run.error")
	}
	tail, _ := got["stderr_tail"].([]string)
	if len(tail) != 2 || tail[1] != "line-2" {
		t.Fatalf("事件载荷应带 stderr 尾巴: %+v", got)
	}
	// 同一失败也应进入底座诊断快照（单底座：留痕只有一份，不再有"会话 → agent 反查"）。
	le, ok := svc.runtimeLastError()
	if !ok || le.SessionID != "c-x" || len(le.StderrTail) != 2 {
		t.Fatalf("诊断快照不符: %+v ok=%v", le, ok)
	}
}

// TestRuntimeDiagnosticsRemediation 诊断面外露「怎么修」：命令不可执行 / provider 档案悬空 /
// 底座 SetupSteps 都转为可执行步骤；runtime 未注册单独成一类；健康且已连接时不掺安装说明。
func TestRuntimeDiagnosticsRemediation(t *testing.T) {
	ctx := context.Background()

	// ① 已注册但启动命令不在 PATH + 悬空 provider 档案 + 声明了 SetupSteps（未连接）→ 三类步骤齐出。
	reg := runtime.NewRegistry(runtime.Spec{
		ID: "rtx", Name: "rtx",
		Launch:     runtime.LaunchSpec{Command: "definitely-not-installed-cmd"},
		SetupSteps: []string{"安装 rtx CLI 并确认它在 PATH 中"},
	})
	svc := NewService(reg, eventbus.New())
	setBuildRuntime(t, "rtx") // 夹具注册表只有 rtx
	// 「供应商档案悬空」那一类事实在 v19 的落点是**默认模型所指的供应商**（agent 上的 provider
	// 字段已随 agent 面撤销）：置一条默认模型、让它指向一个不存在的供应商档案。
	// 这就是装配期 `CredentialProblem` 会拦下的那种半配置状态，诊断面必须给得出出路。
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer st.Close()
	svc.EnableStore(st)
	if created, err := st.CreateModelDefinition(ctx, store.ModelColumns{
		ID: "mdl_gone", ProviderID: "gone", Model: "m-gone", InputModalities: "text",
	}, "t"); err != nil || !created {
		t.Fatalf("造默认模型行: created=%v err=%v", created, err)
	}
	if err := st.SetDefaultModelID(ctx, "mdl_gone"); err != nil {
		t.Fatalf("置默认模型: %v", err)
	}

	diag, err := svc.RuntimeDiagnostics(ctx)
	if err != nil {
		t.Fatalf("RuntimeDiagnostics: %v", err)
	}
	joined := strings.Join(diag.Remediation, "\n")
	for _, want := range []string{"不可执行", "不存在", "安装 rtx CLI 并确认它在 PATH 中"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("remediation 应含 %q: %v", want, diag.Remediation)
		}
	}
	if len(diag.Remediation) != 3 {
		t.Fatalf("期望 3 条（命令不可执行/档案悬空/SetupSteps），got %v", diag.Remediation)
	}

	// ② 构建期底座未装配 → 单独的「未装配」步骤，不误报命令不可执行。
	//
	// 历史：这里种的是"agent 的候选里有未注册的 runtime"。候选面下线后，唯一能让诊断报
	// 「未装配」的形态是**构建期选定的那个不在注册表里**（注册表是空的）。
	reg2 := runtime.NewRegistry()
	svc2 := NewService(reg2, eventbus.New())
	setBuildRuntime(t, "ghost")
	diag2, err := svc2.RuntimeDiagnostics(ctx)
	if err != nil {
		t.Fatalf("RuntimeDiagnostics(ghost): %v", err)
	}
	joined2 := strings.Join(diag2.Remediation, "\n")
	if !strings.Contains(joined2, `runtime "ghost" 未装配`) || strings.Contains(joined2, "不可执行") {
		t.Fatalf("未装配应单独成一类: %v", diag2.Remediation)
	}

	// ③ 健康且已连接（桩 runtime 真握手成功）→ 不带任何 remediation（诊断快照不掺噪音）。
	stub := stubRuntimeSpec(t)
	stub.SetupSteps = []string{"装 stub（仅在有问题时才该出现）"}
	reg3 := runtime.NewRegistry(stub)
	svc3 := NewService(reg3, eventbus.New())
	setBuildRuntime(t, "stub")
	orch3 := NewOrchestrator(svc3, reg3)
	t.Cleanup(orch3.Close)
	if _, err := orch3.startSessionOn(ctx, StartOptions{Cwd: t.TempDir()}); err != nil {
		t.Fatalf("建会话: %v", err)
	}
	diag3, err := svc3.RuntimeDiagnostics(ctx)
	if err != nil {
		t.Fatalf("RuntimeDiagnostics(ok): %v", err)
	}
	if !diag3.Connected || len(diag3.Remediation) != 0 {
		t.Fatalf("健康快照不应带 remediation: %+v", diag3)
	}
}

// waitForLiveTail 轮询等 live stderr 尾巴出现某行（最多 2s）。
// 不用固定 sleep：竞态是"早晚会到"，有界轮询既不掩盖真失败也不靠猜时长。
func waitForLiveTail(t *testing.T, svc *Service, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last []string
	for {
		diag, err := svc.RuntimeDiagnostics(context.Background())
		if err == nil {
			last = diag.StderrTail
			if strings.Contains(strings.Join(diag.StderrTail, "\n"), want) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("live 尾巴里始终没有 %q（最后拿到 %v）", want, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
