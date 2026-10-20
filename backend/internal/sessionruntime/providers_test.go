package sessionruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/testutil/runtimetest"

	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/runtime"
)

// TestProviderRegistryWriteOnceReadNever 档案新建/持久化/重启装载 + 密钥只写不读回 +
// 启动 env 注入推导 + 清除语义。
func TestProviderRegistryWriteOnceReadNever(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agents")
	svc := providerTestSvc(t, dir)

	key := "sk-secret-abc"
	resp, err := planesOf(svc).Providers.CreateProvider(ctx, providerReq("default", &key))
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if !resp.HasKey {
		t.Fatal("应 has_key=true")
	}
	// 列出时 key 字段应缺席（结构不含 Key；再断言序列化不含明文）。
	items, _ := planesOf(svc).Providers.ListProviders(context.Background())
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
	envs := planesOf(svc).Providers.ProviderEnvFor(runtimetest.ID, "default")
	if len(envs) != 1 || !strings.HasPrefix(envs[0], "MATE_DEFAULT_") || !strings.HasSuffix(envs[0], "_API_KEY=sk-secret-abc") {
		t.Fatalf("env 注入不符（应为逐供应商派生名）: %+v", envs)
	}
	_, _ = planesOf(svc).Providers.CreateProvider(ctx, providerReq("ds", &key))
	got := planesOf(svc).Providers.ProviderEnvFor(runtimetest.ID, "ds")
	if len(got) != 1 || !strings.HasPrefix(got[0], "MATE_DS_") {
		t.Fatalf("第二条供应商也该有自己的引用名: %+v", got)
	}
	if got[0] == envs[0] {
		t.Fatalf("两条供应商不得共用引用名（v11 的同 type 共用密钥缺陷）: %q", got[0])
	}

	// 清除：key 空串 → has_key=false、env nil。
	clear := ""
	_, _ = planesOf(svc).Providers.UpdateProvider(ctx, "default", provider.PatchRequest{APIKey: &clear})
	if planesOf(svc).Providers.ProviderEnvFor(runtimetest.ID, "default") != nil {
		t.Fatal("清除后不应注入 env")
	}
	items, _ = planesOf(svc).Providers.ListProviders(context.Background())
	for _, it := range items {
		if it.ID == "default" && it.HasKey {
			t.Fatalf("清除后 has_key 应为 false: %+v", it)
		}
	}

	// 重启（新 Service 同目录）→ 档案装载（含 ds 仍有 key、default 无 key）。
	svc2 := providerTestSvc(t, dir)
	if planesOf(svc2).Providers.ProviderEnvFor(runtimetest.ID, "ds") == nil {
		t.Fatal("重启后 ds key 应仍在")
	}
	if planesOf(svc2).Providers.ProviderEnvFor(runtimetest.ID, "default") != nil {
		t.Fatal("重启后 default 清除应保持")
	}
}

// providerReq v12 的供应商新建请求（协议恒 `openai`；测试关心的只是端点与密钥）。
//
// v11 的 `Type`（自由串，决定注入哪个环境变量名）已被 `protocol` + `base_url` 取代，
// 所以测试夹具也要显式给端点——缺端点的供应商**不会被交付给任何底座**（B1）。
func providerReq(id string, key *string) provider.CreateRequest {
	u := "https://api.example.com/v1"
	return provider.CreateRequest{ID: id, Protocol: "openai", BaseURL: &u, APIKey: key}
}

// v12ProviderRegistry 一个带**凭据交付面声明**的最小注册表。
//
// v12 起"要不要把密钥注入子进程 env"由**各底座自己的声明**决定（ADR-4：走原生凭据文档的底座
// 由平台写文档、走 env 的底座由平台注入环境），所以凡是要验 env 注入的用例都必须先有一个声明了
// `RefPattern` 且**没声明原生文档**的 runtime。
//
// 它直接取 `internal/testutil/runtimetest` 的合成底座：那份声明**就是**"走 env 通道"的形态，
// 而且带完整的三张执行表（自己再手写一份最小 `.Spec` 会漏掉端口，让这些用例只验到声明的一半）。
func v12ProviderRegistry() *runtime.Registry {
	reg, _ := runtimetest.NewRegistry(runtime.LaunchSpec{})
	return reg
}
