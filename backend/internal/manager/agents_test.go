package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
)

// agentTestSvc 播 deepseekharness/opencode/echo（内置 agent 播种 + echo 自定义；不拨真实子进程）。
func agentTestSvc(t *testing.T, agentDir string) *Service {
	t.Helper()
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "deepseekharness", Name: "deepseek-harness"},
		runtime.Spec{ID: "opencode", Name: "opencode"},
		runtime.Spec{ID: "echo", Name: "Echo"},
	)
	svc := NewService(reg, eventbus.New())
	if agentDir != "" {
		svc.LoadAgentDefinitions(agentDir)
	}
	return svc
}

// TestAgentRegistryDefinitionsCRUD P2 definitions：注册持久化 + 重启（新 Service）加载 +
// PATCH 热更新 + 删除；内置 agent dsh/opencode 由播种提供（dsh 默认）。
func TestAgentRegistryDefinitionsCRUD(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	svc := agentTestSvc(t, dir)

	// 注册自定义 agent（echo runtime 上的别名，不写代码新增 ACP agent）。
	resp, err := svc.CreateAgent(ctx, handler.AgentCreateReq{ID: "alias-echo", Name: "别名echo", Runtimes: []string{"echo"}, Provider: "openai:gpt-4o"})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if resp.ID != "alias-echo" || resp.Provider != "openai:gpt-4o" {
		t.Fatalf("resp 不符: %+v", resp)
	}
	// 定义文件已落盘。
	if _, err := os.Stat(filepath.Join(dir, "alias-echo.json")); err != nil {
		t.Fatalf("definitions 未落盘: %v", err)
	}

	// 重启（新 Service + 同一目录）→ definitions 加载。
	svc2 := agentTestSvc(t, dir)
	if a, ok := svc2.AgentByID("alias-echo"); !ok || len(a.Runtimes) != 1 || a.Runtimes[0] != "echo" || a.Provider != "openai:gpt-4o" {
		t.Fatalf("重启后未加载 definition: %+v ok=%v", a, ok)
	}

	// PATCH 热更新 name。
	name := "别名echo-v2"
	u, err := svc2.UpdateAgent(ctx, "alias-echo", handler.AgentUpdateReq{Name: &name})
	if err != nil || u.Name != "别名echo-v2" {
		t.Fatalf("UpdateAgent: %v %+v", err, u)
	}
	if a, _ := svc2.AgentByID("alias-echo"); a.Name != "别名echo-v2" {
		t.Fatalf("name 未更新: %+v", a)
	}

	// 内置 Agent 已播种（用户选 Agent 而非 runtime；dsh 为默认）。
	if a, ok := svc2.AgentByID("dsh"); !ok || !a.IsDefault() {
		t.Fatalf("默认 agent dsh 应播种且为默认: %+v ok=%v", a, ok)
	}
	if a, ok := svc2.AgentByID("opencode"); !ok || a.IsDefault() {
		t.Fatalf("内置 agent opencode 应播种且非默认: %+v ok=%v", a, ok)
	}

	// 非法：保留前缀 id 拒绝；重复 id 拒绝；未知 runtime 拒绝。
	if _, err := svc2.CreateAgent(ctx, handler.AgentCreateReq{ID: "agent-x", Runtimes: []string{"echo"}}); err == nil {
		t.Fatal("保留前缀应拒绝")
	}
	if _, err := svc2.CreateAgent(ctx, handler.AgentCreateReq{ID: "alias-echo", Runtimes: []string{"echo"}}); err == nil {
		t.Fatal("重复 id 应拒绝")
	}
	if _, err := svc2.CreateAgent(ctx, handler.AgentCreateReq{ID: "ghost", Runtimes: []string{"no-such-runtime"}}); err == nil {
		t.Fatal("未知 runtime 应拒绝")
	}

	// 删除自定义 + 定义文件清理。
	if err := svc2.DeleteAgent(ctx, "alias-echo"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, ok := svc2.AgentByID("alias-echo"); ok {
		t.Fatal("删除后仍存在")
	}
	if _, err := os.Stat(filepath.Join(dir, "alias-echo.json")); !os.IsNotExist(err) {
		t.Fatalf("定义文件未清理: %v", err)
	}
}

// TestAgentRegistryLoadSkipsBroken 目录加载：坏 JSON / 未知 runtime 定义跳过不 panic。
func TestAgentRegistryLoadSkipsBroken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"id":"good-echo","name":"好","runtimes":["echo"]}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{not json`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "noruntime.json"), []byte(`{"id":"x1","runtimes":["nope"]}`), 0o644)
	svc := agentTestSvc(t, dir)
	if _, ok := svc.AgentByID("good-echo"); !ok {
		t.Fatal("good 应加载")
	}
	if _, ok := svc.AgentByID("x1"); ok {
		t.Fatal("未知 runtime 应跳过")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.json")); err != nil {
		t.Fatalf("bad.json 应保留未动: %v", err)
	}
}

// TestAgentStartStopGuards 无编排器装配（demo/纯列表）时启停返回明确错误。
func TestAgentStartStopGuards(t *testing.T) {
	ctx := context.Background()
	svc := agentTestSvc(t, "")
	if _, err := svc.CreateAgent(ctx, handler.AgentCreateReq{ID: "alias-echo", Runtimes: []string{"echo"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartAgent(ctx, "alias-echo"); err == nil {
		t.Fatal("无编排器应拒绝 StartAgent")
	}
	if err := svc.StopAgent(ctx, "alias-echo"); err == nil {
		t.Fatal("无编排器应拒绝 StopAgent")
	}
	if err := svc.StartAgent(ctx, "nope"); err == nil {
		t.Fatal("未知 agent 应拒绝")
	}
}
