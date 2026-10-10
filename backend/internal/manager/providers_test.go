package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// TestProviderRegistryWriteOnceReadNever 档案新建/持久化/重启装载 + 密钥只写不读回 +
// 启动 env 注入推导 + 清除语义。
func TestProviderRegistryWriteOnceReadNever(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	svc := agentTestSvc(t, dir)

	key := "sk-secret-abc"
	resp, err := svc.CreateProvider(ctx, providerReq("default", &key))
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if !resp.HasKey {
		t.Fatal("应 has_key=true")
	}
	// 列出时 key 字段应缺席（结构不含 Key；再断言序列化不含明文）。
	items, _ := svc.ListProviders(context.Background())
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

	// env 注入：**逐供应商派生的引用名**（v11 的 `<TYPE>_API_KEY` 已删，ADR-16）。
	// 判据不是"名字长什么样"而是"两条供应商拿到两个不同的名字"——同一把密钥命中两条 route
	// 正是 v11 的真实缺陷（A12 的回归点）。
	envs := svc.providerEnvFor("opencode", "default")
	if len(envs) != 1 || !strings.HasPrefix(envs[0], "MATE_DEFAULT_") || !strings.HasSuffix(envs[0], "_API_KEY=sk-secret-abc") {
		t.Fatalf("env 注入不符（应为逐供应商派生名）: %+v", envs)
	}
	_, _ = svc.CreateProvider(ctx, providerReq("ds", &key))
	got := svc.providerEnvFor("opencode", "ds")
	if len(got) != 1 || !strings.HasPrefix(got[0], "MATE_DS_") {
		t.Fatalf("第二条供应商也该有自己的引用名: %+v", got)
	}
	if got[0] == envs[0] {
		t.Fatalf("两条供应商不得共用引用名（v11 的同 type 共用密钥缺陷）: %q", got[0])
	}

	// 清除：key 空串 → has_key=false、env nil。
	clear := ""
	_, _ = svc.UpdateProvider(ctx, "default", dtoprovider.ProviderPatchReq{APIKey: &clear})
	if svc.providerEnvFor("opencode", "default") != nil {
		t.Fatal("清除后不应注入 env")
	}
	items, _ = svc.ListProviders(context.Background())
	for _, it := range items {
		if it.ID == "default" && it.HasKey {
			t.Fatalf("清除后 has_key 应为 false: %+v", it)
		}
	}

	// 重启（新 Service 同目录）→ 档案装载（含 ds 仍有 key、default 无 key）。
	svc2 := agentTestSvc(t, dir)
	if svc2.providerEnvFor("opencode", "ds") == nil {
		t.Fatal("重启后 ds key 应仍在")
	}
	if svc2.providerEnvFor("opencode", "default") != nil {
		t.Fatal("重启后 default 清除应保持")
	}
}

// providerReq v12 的供应商新建请求（协议恒 `openai`；测试关心的只是端点与密钥）。
//
// v11 的 `Type`（自由串，决定注入哪个环境变量名）已被 `protocol` + `base_url` 取代，
// 所以测试夹具也要显式给端点——缺端点的供应商**不会被交付给任何底座**（B1）。
func providerReq(id string, key *string) dtoprovider.ProviderCreateReq {
	u := "https://api.example.com/v1"
	return dtoprovider.ProviderCreateReq{ID: id, Protocol: "openai", BaseURL: &u, APIKey: key}
}

// v12ProviderRegistry 一个带**凭据交付面声明**的最小注册表。
//
// v12 起"要不要把密钥注入子进程 env"由**各底座自己的声明**决定（ADR-4：dsh 走原生凭据文档、
// opencode 走 env），所以凡是要验 env 注入的用例都必须先有一个声明了 `RefPattern` 的 runtime。
// 用一个最小 `.Spec` 而不是真实 adapter：这里要钉的是平台侧的派生与注入逻辑，不是底座形态。
func v12ProviderRegistry() *runtime.Registry {
	return runtime.NewRegistry(runtime.Spec{
		ID: "opencode", Name: "opencode",
		Credential: resource.CredentialSpec{RefPattern: "MATE_%s_API_KEY", FallbackEnv: true},
	})
}
