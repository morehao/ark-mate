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

// TestInitInjectsRuntimeIsolation 装配路径真的接上了隔离（M3-3）。
//
// 为什么光测 pkg/runtime 不够：接线被删掉时**没有任何东西会报错**——底座只是又回去读写用户自己的
// ~/.dsh / ~/.config/opencode，表现为"平台会话能看到我手装底座的配置"。这里走一遍真实 Init
// （内存持久层 + 假资产目录），断言注册表里本地 runtime 拿到的正是隔离环境、远程端点一份都没有，
// 且 /meta 的 home 直接反映数据目录键。
func TestInitInjectsRuntimeIsolation(t *testing.T) {
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
	cfg.DBPath = filepath.Join(home, "forge.db")
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
		"opencode:skill":        filepath.Join(home, "proj-opencode"),
		"deepseekharness:skill": filepath.Join(home, "proj-dsh"),
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

	for _, id := range []string{"opencode", "deepseekharness", "remote-x"} {
		if _, ok := a.reg.Get(id); !ok {
			t.Fatalf("%s 未注册", id)
		}
	}
	for _, id := range []string{"opencode", "deepseekharness"} {
		spec, _ := a.reg.Get(id)
		if want := runtime.IsolationEnv(id, home); !reflect.DeepEqual(spec.Launch.Env, want) {
			t.Fatalf("%s 的隔离环境未接上:\n got %v\nwant %v", id, spec.Launch.Env, want)
		}
	}
	if spec, _ := a.reg.Get("remote-x"); len(spec.Launch.Env) != 0 {
		t.Fatalf("远程端点不应注入 env: %v", spec.Launch.Env)
	}
	if got := a.metaInfo().Home; got != home {
		t.Fatalf("/meta 的 home 不符: got %q want %q", got, home)
	}
}
