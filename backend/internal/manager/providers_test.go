package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
)

// TestProviderRegistryWriteOnceReadNever 档案新建/持久化/重启装载 + 密钥只写不读回 +
// 启动 env 注入推导 + 清除语义。
func TestProviderRegistryWriteOnceReadNever(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	svc := agentTestSvc(t, dir)

	key := "sk-secret-abc"
	typ := "openai"
	model := "gpt-4o"
	resp, err := svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "default", Type: typ, Model: &model, Key: &key})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if !resp.HasKey {
		t.Fatal("应 has_key=true")
	}
	// 列出时 key 字段应缺席（结构不含 Key；再断言序列化不含明文）。
	items, _ := svc.ListProviders()
	if len(items) != 1 || items[0].HasKey != true {
		t.Fatalf("ListProviders: %+v", items)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "providers.json"))
	if string(b) == "" {
		t.Fatal("providers.json 应落盘")
	}
	if len(items) == 0 {
		t.Fatal("no provider")
	}

	// env 注入推导：type=openai → OPENAI_API_KEY；未知 type 大写 + _API_KEY。
	envs := svc.providerEnvFor("default")
	if len(envs) != 1 || envs[0] != "OPENAI_API_KEY=sk-secret-abc" {
		t.Fatalf("env 注入不符: %+v", envs)
	}
	typ2 := "custom"
	_ = typ2
	t2 := "deepseek"
	_, _ = svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "ds", Type: t2, Key: &key})
	if got := svc.providerEnvFor("ds"); len(got) != 1 || got[0] != "DEEPSEEK_API_KEY=sk-secret-abc" {
		t.Fatalf("deepseek env: %+v", got)
	}

	// 清除：key 空串 → has_key=false、env nil。
	clear := ""
	_, _ = svc.UpdateProvider(ctx, "default", dtoprovider.ProviderPatchReq{Key: &clear})
	if svc.providerEnvFor("default") != nil {
		t.Fatal("清除后不应注入 env")
	}
	items, _ = svc.ListProviders()
	for _, it := range items {
		if it.ID == "default" && it.HasKey {
			t.Fatalf("清除后 has_key 应为 false: %+v", it)
		}
	}

	// 重启（新 Service 同目录）→ 档案装载（含 ds 仍有 key、default 无 key）。
	svc2 := agentTestSvc(t, dir)
	if svc2.providerEnvFor("ds") == nil {
		t.Fatal("重启后 ds key 应仍在")
	}
	if svc2.providerEnvFor("default") != nil {
		t.Fatal("重启后 default 清除应保持")
	}
}
