package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/pkg/config"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// newIsolationRig 装配一台真实 App（内存持久层 + 假资产目录 + 一个远程 runtime 声明）。
//
// 为什么走真实 Init 而不是直接构造注册表：接线被删掉时**没有任何东西会报错**——底座只是又回去
// 读写用户自己的底座 home 目录，表现为"平台会话能看到我手装底座的配置"；而
// "装配的是哪一个底座"同理：换成 ProductRegistry 之后，漏改某个装配点不会有编译错误。
func newIsolationRig(t *testing.T) (*App, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	home := t.TempDir()
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>t</title>"), 0o600); err != nil {
		t.Fatalf("写假资产: %v", err)
	}

	cfg := config.Default()
	cfg.HomeDir = home
	cfg.ConfigDir = filepath.Join(home, "config")
	cfg.TranscriptDir = filepath.Join(home, "transcripts")
	cfg.DBPath = filepath.Join(home, "mate.db")
	cfg.StoreMode = config.StoreModeMemory
	cfg.WorkspaceDir = ""     // 关闭代建（本测试不建会话）
	cfg.ProcRegistryPath = "" // 不写进程注册表
	cfg.AutomationTick = 0    // 关闭调度
	cfg.IdleTimeout = 0       // 关闭空闲回收
	cfg.ReapInterval = 0
	cfg.Addr = "127.0.0.1:0"
	cfg.WebDir = web
	// 投影根也收进临时目录：默认根是 ~/.agents/skills，测试不该往开发机家目录写东西
	// （skill 物化重校会建根目录，哪怕本轮一条 skill 都没有）。
	cfg.ProjectionDirs = map[string]string{
		"rtx:skill":      filepath.Join(home, "proj-rtx"),
		"dsh:skill":      filepath.Join(home, "proj-dsh"),
		"remote-x:skill": filepath.Join(home, "proj-remote-x"),
	}
	cfg.RemoteRuntimes = []config.RemoteRuntime{
		{ID: "remote-x", Transport: "ws", Endpoint: "ws://127.0.0.1:1/acp"},
	}

	a := New(cfg, nil)
	if err := a.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if a.srv != nil {
			_ = a.srv.Stop(ctx)
		}
		a.Close()
	})
	return a, home
}

// TestInitPinsBuildRuntime 装配路径真的把**构建期选定的那一个**底座装上（默认 = dsh）。
//
// 三件事一起断言，缺一条都会留下静默缺口：
//   - 选中的那个在注册表里；
//   - 另外两个候选（另一个内置 + runtimes.json 里的远程）**不在**——它们是候选，不是装配结果；
//   - 选中那个的隔离环境真的接上了（否则会话会去读写用户自己的 ~/.dsh）。
func TestInitPinsBuildRuntime(t *testing.T) {
	a, home := newIsolationRig(t)

	if got := runtime.SelectedRuntime(); got != "dsh" {
		t.Fatalf("默认构建期底座应是 dsh，得到 %q", got)
	}
	if _, ok := a.Result().Runtime.Get("dsh"); !ok {
		t.Fatal("构建期选中的 dsh 未装配")
	}
	for _, id := range []string{"rtx", "remote-x"} {
		if _, ok := a.Result().Runtime.Get(id); ok {
			t.Fatalf("%s 不该出现在装配结果里（构建期固定在 dsh）", id)
		}
	}
	spec, _ := a.Result().Runtime.Get("dsh")
	if want := runtime.IsolationEnv("dsh", home); !reflect.DeepEqual(spec.Launch.Env, want) {
		t.Fatalf("dsh 的隔离环境未接上:\n got %v\nwant %v", spec.Launch.Env, want)
	}
	if got := a.Meta().Home; got != home {
		t.Fatalf("/meta 的 home 不符: got %q want %q", got, home)
	}
}

// TestInitPinsSelectedRemoteRuntime 构建期选中的 id 命中 runtimes.json 声明 → 装配结果就是那条远程端点
// （A5 不因"构建期固定"变成死码），且本地底座一份都不进注册表、也就一份 env 都不注入。
func TestInitPinsSelectedRemoteRuntime(t *testing.T) {
	old := runtime.BuildRuntime
	runtime.BuildRuntime = "remote-x"
	t.Cleanup(func() { runtime.BuildRuntime = old })

	a, _ := newIsolationRig(t)

	spec, ok := a.Result().Runtime.Get("remote-x")
	if !ok {
		t.Fatal("构建期选中的 remote-x 未装配")
	}
	if len(spec.Launch.Env) != 0 {
		t.Fatalf("远程端点不应注入 env: %v", spec.Launch.Env)
	}
	for _, id := range []string{"rtx", "dsh"} {
		if _, ok := a.Result().Runtime.Get(id); ok {
			t.Fatalf("%s 不该出现在装配结果里（构建期固定在 remote-x）", id)
		}
	}
}

// TestInitFailsOnUnregisteredBuildRuntime 构建期注入一个谁都不是的 id → 装配期**失败**。
//
// 这是 fail-closed 的判据：返回空注册表的话进程照样起、REST 照样答，只是每个会话各自报
// "runtime 未注册"——构建期的一个错字会伪装成运行期的一堆故障。
func TestInitFailsOnUnregisteredBuildRuntime(t *testing.T) {
	old := runtime.BuildRuntime
	runtime.BuildRuntime = "bogus"
	t.Cleanup(func() { runtime.BuildRuntime = old })

	gin.SetMode(gin.TestMode)
	home := t.TempDir()
	cfg := config.Default()
	cfg.HomeDir = home
	cfg.ConfigDir = filepath.Join(home, "config")
	cfg.StoreMode = config.StoreModeMemory
	cfg.WorkspaceDir = ""
	cfg.ProcRegistryPath = ""
	cfg.AutomationTick = 0
	cfg.IdleTimeout = 0
	cfg.ReapInterval = 0
	cfg.Addr = "127.0.0.1:0"

	a := New(cfg, nil)
	if err := a.Init(context.Background()); err == nil {
		a.Close()
		t.Fatal("构建期指定的 runtime 未注册时应拒绝启动")
	}
}
