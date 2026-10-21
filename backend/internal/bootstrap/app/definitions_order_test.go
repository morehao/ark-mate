package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/config"
)

// TestInitOrderingImportLoadSchedule M6-9：装配期三条时序（都必须"先 store、再导入、再装载、
// 最后起调度"）一次钉住——这三条各自被违反时**都不会报错**，只会表现成"东西看不见"或
// "启动恢复没发生"：
//
//  1. store 打开 / EnableStore 在装载之前：装载只从 DB 读，库没开就没得读（sqlite 模式下面为空）；
//  2. 存量导入在装载之前：导入晚一步，这一轮启动的**面**就是空的（用户看到"我的技能/
//     定时任务/供应商档案全没了"，哪怕下一轮启动就回来）；
//  3. automation 调度启动（含同步 startup pass）在装载之后：pass 扫的是内存注册表，
//     先起调度就会对着空表收敛一次——过期的 `once` 不会被置 inactive，而且**没有任何后续动作
//     会补做**（它只在启动时跑一次），表现为"任务永远停在 active 却再也不触发"。
//
// 用**行为**而不是读源码来钉：三条的后果都是可观察的（面是否为空 / 注册表是否已收敛）。
func TestInitOrderingImportLoadSchedule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	home := t.TempDir()
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>t</title>"), 0o600); err != nil {
		t.Fatalf("写假资产: %v", err)
	}
	caps := filepath.Join(home, "capabilities")
	providers := filepath.Join(home, "providers")
	autos := filepath.Join(home, "automations")
	for _, d := range []string{filepath.Join(caps, "skills"), filepath.Join(caps, "mcp"), providers, autos} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(caps, "skills", "hello.json"),
		`{"id":"hello","name":"Hello","description":"d","body":"# Hello\n","runtimes":["rtx"]}`)
	// 过期的 once：startup pass 会把它置 inactive 并留一条 skipped 痕迹——它是"调度确实在
	// 装载**之后**起了"的唯一可观察证据（先起调度的话它扫到的是空表）。
	mustWrite(t, filepath.Join(autos, "stale.json"),
		`{"id":"stale","name":"过期闹钟","enabled":true,`+
			`"schedule":{"kind":"once","timezone":"UTC","at":"2020-01-01T00:00:00Z"},`+
			`"target":{"cwd":"`+cwd+`","prompt":"x"}}`)

	cfg := config.Default()
	cfg.HomeDir = home
	cfg.ConfigDir = filepath.Join(home, "config")
	cfg.TranscriptDir = filepath.Join(home, "transcripts")
	cfg.DBPath = filepath.Join(home, "mate.db")
	cfg.StoreMode = config.StoreModeSQLite
	cfg.CapabilityDir = caps
	cfg.ProviderDir = providers
	cfg.AutomationDir = autos
	cfg.WorkspaceDir = ""
	cfg.ProcRegistryPath = ""
	cfg.AutomationTick = 30 * time.Second // 起调度：startup pass 必须在这一轮跑掉
	cfg.IdleTimeout = 0
	cfg.ReapInterval = 0
	cfg.Addr = "127.0.0.1:0"
	cfg.WebDir = web
	cfg.ProjectionDirs = map[string]string{
		"rtx:skill": filepath.Join(home, "proj-rtx"),
		"dsh:skill": filepath.Join(home, "proj-dsh"),
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

	// ① 能力面：库里的行 + 内存注册表都可见（"导入先于装载"的合成结果）。
	skills, err := a.Result().Session.Planes().Skills.ListSkills()
	if err != nil {
		t.Fatalf("能力面应可用: %v", err)
	}
	found := false
	for _, sk := range skills {
		if sk.ID == "hello" {
			found = true
		}
	}
	if !found {
		t.Fatalf("存量 skill 应在本轮启动就可见（导入必须先于装载）: %+v", skills)
	}
	// ③ 调度：startup pass 已经对**装载进来的**定义收敛过（过期 once → inactive + skipped）。
	resp, ok := a.Result().Session.Planes().Automation.AutomationByID("stale")
	if !ok {
		t.Fatal("automation 面应可见（导入必须先于装载）")
	}
	if resp.State != "inactive" {
		t.Fatalf("过期的 once 应被 startup pass 置 inactive（说明调度起在装载之后），got %q（status=%q）",
			resp.State, resp.LastStatus)
	}
	if resp.LastStatus != "skipped" {
		t.Fatalf("过期 once 应留一条 skipped 痕迹，got %q", resp.LastStatus)
	}
}

// mustWrite 写一个夹具文件（父目录由调用方建好）。
func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写 %s: %v", path, err)
	}
}
