package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/vault"
	"github.com/morehao/ark-mate/pkg/config"
)

// TestStartupSyncResolvesStoredProviderKey 装配**顺序**的回归守卫（2026-10-11 实测）。
//
// 用户侧的现场：供应商目录里 `has_key=false` + 一条「尚未设置密钥」，而密文其实好好的
// （`mate doctor` 报 ciphertext 在册、`vault:providers/<id>` 解得开）；再点保存补密钥，
// 写面回 `vault_unavailable`（"本机没有可用的 vault"）——密钥根本存不进去。
//
// 根因是装配顺序：`EnableStore` 里的**装配期 runtime 同步**发生在 `loadDefinitions` 之前，
// 而 vault 的锚点是 AgentDir 的父目录。那一次同步要读 provider 密钥，于是：
//   - `providerVault()` 拿到的是"锚点还不知道"→ 旧实现把它缓存成整个进程的结论（第二个守卫在
//     manager：`TestProviderVaultEarlyCallDoesNotPoisonAnchor`）；
//   - 就算不缓存，那一次同步也已经按"没有 vault"算完了：同步结论写成"密钥不可解"并**撤回**已
//     物化的模型配置（用户侧表现为模型从底座上消失），而 meta 里留下的这份错结论会被界面与
//     doctor 一直复读。
//
// 所以这条守卫**必须走真实 `App.Init`**（不是直接调 manager）：它钉的是"装配先登记锚点"这个
// 顺序本身——把这行 `a.svc.SetAgentDir(cfg.AgentDir)` 删掉，本用例立刻红。
func TestStartupSyncResolvesStoredProviderKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	home := t.TempDir()
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>t</title>"), 0o600); err != nil {
		t.Fatalf("写假资产: %v", err)
	}
	dbPath := filepath.Join(home, "mate.db")
	agentDir := filepath.Join(home, "agents")
	const secret = "sk-seeded-provider-key"

	// ① 造出"上一轮已经存过密钥"的真实状态：资源表一行 + 挂它的一条模型 + vault 里一份密文。
	//    （明文只在 vault 里——v13 的两张表结构上没有能放明文的列，所以这条密文就是唯一副本。）
	seed, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开种子库: %v", err)
	}
	if ok, err := seed.UpsertProviderDefinition(ctx, store.ProviderColumns{
		ID: "yygu", Name: "yygu", Protocol: "openai", BaseURL: "https://llm.example/v1",
	}, 0, "test"); err != nil || !ok {
		t.Fatalf("种子 provider 行: ok=%v err=%v", ok, err)
	}
	if ok, err := seed.CreateModelDefinition(ctx, store.ModelColumns{
		ID: "m1", ProviderID: "yygu", ModelID: "gpt-x", InputModalities: "text",
	}, "test"); err != nil || !ok {
		t.Fatalf("种子 model 行: ok=%v err=%v", ok, err)
	}
	vs, err := vault.Open(home)
	if err != nil {
		t.Fatalf("打开种子 vault: %v", err)
	}
	if err := vs.Put("vault:providers/yygu", vault.KindProviderKey, []byte(secret)); err != nil {
		t.Fatalf("写种子密文: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("关种子库: %v", err)
	}

	// ② 真实装配（sqlite：这条路上 EnableStore 会做一次装配期 runtime 同步）。
	cfg := config.Default()
	cfg.HomeDir = home
	cfg.ConfigDir = filepath.Join(home, "config")
	cfg.TranscriptDir = filepath.Join(home, "transcripts")
	cfg.DBPath = dbPath
	cfg.AgentDir = agentDir
	cfg.StoreMode = config.StoreModeSQLite
	cfg.WorkspaceDir = ""
	cfg.ProcRegistryPath = ""
	cfg.AutomationTick = 0
	cfg.IdleTimeout = 0
	cfg.ReapInterval = 0
	cfg.Addr = "127.0.0.1:0"
	cfg.WebDir = web
	cfg.ProjectionDirs = map[string]string{
		"dsh:skill": filepath.Join(home, "proj-dsh"),
	}
	a := New(cfg, nil)
	if err := a.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if a.srv != nil {
			_ = a.srv.Stop(c)
		}
		a.Close()
	})

	// ③ 用户可见的结论：这条档案"有密钥"，且没有任何待处理项。
	items, err := a.svc.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应有 1 条供应商，得到 %d 条", len(items))
	}
	if !items[0].HasKey {
		t.Fatalf("已存密文应解出明文（has_key=true），得到 has_key=false，problems=%v", items[0].Problems)
	}
	if len(items[0].Problems) != 0 {
		t.Fatalf("密钥可用时不该有待处理项，得到 %v", items[0].Problems)
	}

	// ④ 同步面也不能把"密钥不可解"写进去——那会让模型清单被当成空的**撤回**（比界面文案更坏的后果）。
	states := a.svc.RuntimeConfigStates(ctx)
	st, ok := states["dsh"]
	if !ok {
		t.Fatalf("装配期同步应有 dsh 的结论，得到 %v", states)
	}
	for _, p := range st.Problems {
		if strings.Contains(p, "密钥") || strings.Contains(p, "不会被交付") {
			t.Fatalf("同步结论不该报密钥问题: %v", st.Problems)
		}
	}
}
