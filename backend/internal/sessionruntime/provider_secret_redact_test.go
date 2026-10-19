package sessionruntime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/redact"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
)

// ——— 动态密钥登记的生产装配判据（P4a：自检用例搬走之后**留下**的那一半） ———
//
// 自检的判据（默认零出网、上游四种结论、目录未知档、redact 闸门）在用例层
// （`internal/service/svcprovider/probe_test.go`），自检用例的**装配**判据在
// `internal/controller/ctrprovider/probe_test.go`，两条合起来才完整。
//
// 本文件验的是用例级夹具验不了的那一段：**真的建档 → 密钥真值落进域平面 → `providerEnvFor`
// 登记动态密钥**。用例层只能自己 `redact.RegisterDynamicSecret` 去模拟这一步（那证明闸门在），
// 但证明不了生产装配真的登记过。改一边时别只改一边。
func TestProviderEnvRegistersDynamicSecret(t *testing.T) {
	ctx := context.Background()
	const secret = "sk-super-secret-value-1234567890"
	// 夹具用**真注册表**（dsh 的凭据投影在里面）：引用名由 adapter 的 `RefPattern` 派生，
	// 空注册表的 `ProviderRefName` 恒为空串——那会让下面那条断言变成"和空串比"。
	svc := providerTestSvc(t, filepath.Join(t.TempDir(), "providers"))

	key := secret
	baseURL := "https://api.openai.com/v1"
	if _, err := svc.planes.Providers.CreateProvider(ctx, dtoprovider.ProviderCreateReq{
		ID: "p1", Protocol: "openai", BaseURL: &baseURL, APIKey: &key,
	}); err != nil {
		t.Fatal(err)
	}
	// 生产路径：注入子进程之前会把生效密钥登记进 redact（`providerEnvFor` 是唯一入口）。
	_ = svc.planes.Providers.ProviderEnvFor(runtimetest.ID, "p1")
	if got := redact.String("出错时带出了 " + secret); strings.Contains(got, secret) {
		t.Fatalf("providerEnvFor 没有登记动态密钥，redact 原样返回了真值: %q", got)
	}
	// 诊断/自检报的是**键名**而不是值（ADR-16）：引用名非空、且与真值不同。
	ref := svc.planes.Providers.ProviderRefName("p1")
	if ref == "" {
		t.Fatal("凭据引用名为空：自检/诊断就没法报出「密钥存在但引用是哪个」——" +
			"adapter 的 `Credential.RefPattern` 没进注册表？")
	}
	if strings.Contains(ref, secret) {
		t.Fatalf("凭据引用名里带上了密钥真值（ADR-16 要求只报键名）: %q", ref)
	}
}
