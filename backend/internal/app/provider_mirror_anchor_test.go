package app

import (
	"context"
	"encoding/json"
	"github.com/morehao/ark-mate/internal/mechanism/credential"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/config"
)

// TestStartupResolvesStoredProviderKeyAndWritesMirror 装配**顺序**的回归守卫（v20 形态）。
//
// v20 把"这台机器上的密钥"从盘上密文搬进了库（`providers.key`），并在装配期做两件事，
// 顺序都不能反：
//
//	① 凭据分层先装配（`SetCredentials`）——下面 sqlite 分支里的第一次 runtime 同步
//	   （`EnableStore`）就要读 provider 密钥；晚装配会让那一次同步按"没有任何密钥"下发，
//	   把它算出来的"密钥不可解"写进 meta，界面与 doctor 之后一直复读（补上之后也仍然复读）；
//	② 导入与搬运之后按库对账镜像（`credentials.json`）——库是权威，镜像只是可重建副本。
//
// 这条守卫**必须走真实 `App.Init`**（不是直接调 manager）：它钉的就是装配顺序本身。
// 判据的失效方式都是静默的：
//
//	has_key=false      → 用户看到"已存密钥没了"，点保存以为能补上（而库那一列本来就有值）；
//	镜像没写/权限放宽   → 面板保存的密钥在 `<MATE_HOME>/credentials.json` 里没有，或别人可读；
//	同步报密钥问题      → 模型清单被当成空的下发**撤回**（比界面文案更坏的后果）。
//
// 旧密文的一次性搬运（以及"它在 EnableStore 之前"这条顺序）由 `internal/store` 侧的冻结工件
// 用例证明：那里有一份由 v19 实现产出的真实密文，且只有那个包能在白名单里写出旧机制的目录名。
func TestStartupResolvesStoredProviderKeyAndWritesMirror(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	home := t.TempDir()
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>t</title>"), 0o600); err != nil {
		t.Fatalf("写假资产: %v", err)
	}
	dbPath := filepath.Join(home, "mate.db")
	providerDir := filepath.Join(home, "providers")
	const secret = "sk-seeded-provider-key"

	// ① 造出"上一轮已经存过密钥"的真实状态：资源表一行（密钥就在这一行上）+ 挂它的一条模型。
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
		ID: "m1", ProviderID: "yygu", Model: "gpt-x", InputModalities: "text",
	}, "test"); err != nil || !ok {
		t.Fatalf("种子 model 行: ok=%v err=%v", ok, err)
	}
	if written, err := seed.SetProviderKey(ctx, "yygu", secret); err != nil || !written {
		t.Fatalf("种子密钥: written=%v err=%v", written, err)
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
	cfg.ProviderDir = providerDir
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
	items, err := a.svc.ProviderPlane().ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应有 1 条供应商，得到 %d 条", len(items))
	}
	if !items[0].HasKey {
		t.Fatalf("库里那一列有值 → has_key 应为 true，得到 false，problems=%v", items[0].Problems)
	}
	if len(items[0].Problems) != 0 {
		t.Fatalf("密钥可用时不该有待处理项，得到 %v", items[0].Problems)
	}
	if items[0].KeySource != credentialsSourceManaged {
		t.Fatalf("密钥来源应是托管那一层，得到 %q", items[0].KeySource)
	}
	if items[0].KeyRef != "providers/yygu" {
		t.Fatalf("key_ref 应是派生量 providers/<id>，得到 %q", items[0].KeyRef)
	}

	// ④ 权威面：库那一列仍然有值（装配不该动它）。
	ro, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("只读开库: %v", err)
	}
	defer ro.Close()
	if v, found, kErr := ro.ProviderKey(ctx, "yygu"); kErr != nil || !found || v != secret {
		t.Fatalf("库里的密钥应原样在: found=%v len=%d err=%v", found, len(v), kErr)
	}

	// ⑤ 镜像面：装配期已按库对账出 `<MATE_HOME>/credentials.json`（0600、含这条引用）。
	assertCredentialsMirror(t, home, "providers/yygu", secret)

	// ⑥ 同步面也不能把"密钥不可解"写进去——那会让模型清单被当成空的**撤回**。
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

// credentialsSourceManaged 是"托管文档那一层"的对外取值（`credentials.Source` 的字符串形）。
// 这里刻意写成字面量而不是 import 那个包：本用例断言的是**回执里的取值**（对外形态），
// 常量改名不该让这条断言悄悄跟着改。
const credentialsSourceManaged = "managed"

// assertCredentialsMirror 断言托管镜像存在、0600、且那一条引用的值与库一致。
func assertCredentialsMirror(t *testing.T, home, ref, want string) {
	t.Helper()
	path := credential.CredentialsPath(home)
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("托管镜像应已生成（%s）: %v", path, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("托管镜像必须是 0600（平台拒读更宽的权限），得到 %04o", perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读镜像: %v", err)
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("镜像应是 `引用 → 值` 的 JSON 对象: %v", err)
	}
	if got, ok := values[ref]; !ok || got != want {
		t.Fatalf("镜像里应有 %q（值相等），得到 ok=%v", ref, ok)
	}
}
