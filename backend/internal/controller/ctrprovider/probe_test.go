package ctrprovider

import (
	"context"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
)

// ——— P4a：自检用例的**装配判据**（用例实例由本控制器造） ———
//
// 用例本身的判据在 `internal/service/svcprovider/probe_test.go`（零出网 / 上游四结论 / 目录
// 未知档 / redact 闸门），那里拿的是两个窄端口。本文件验的是**控制器有没有把它们接上**：
//
//	① `NewProviderCtr` 必须真的造出自检用例（`probe == nil` 的表现是 `POST /providers/:id/probe`
//	   直接 500，而只测档案四动作的用例看不见它）；
//	② 造出来的用例必须**用的是交进来的那两个端口**（拿假档案探一次，结论里能看到它的事实）。
//
// 为什么这条判据值得存在：用例实例以前由 `app.go` 造好交进来，现在改由控制器造——"谁造"换地方
// 时最容易出的错不是编译失败（`NewProviderCtr` 少一个入参就会），而是**入参接错**（把 catalog
// 接到 archives 上，类型不同会编译失败；但把两个都传 nil 就静默变成"自检永远说不知道"）。

// fakeProviderArchives 供应商域读面替身（`ProviderArchives`）。
type fakeProviderArchives struct {
	def     *provider.Def
	refName string
}

func (f fakeProviderArchives) Snapshot(string) (*provider.Def, bool) { return f.def, f.def != nil }

func (f fakeProviderArchives) ProviderRefName(string) string { return f.refName }

func (f fakeProviderArchives) ProviderLocator(string) string { return "db:provider/<id>" }

func (f fakeProviderArchives) ProviderModelRefsReadOnly(context.Context, string) []string {
	return []string{"mdl_1"}
}

// fakeProviderCatalog 模型目录读面替身（`ProviderCatalog`）。
type fakeProviderCatalog struct{ called bool }

func (f *fakeProviderCatalog) ModelCatalog(context.Context) (*dtomodel.ModelCatalogResp, error) {
	f.called = true
	return &dtomodel.ModelCatalogResp{}, nil
}

// TestNewProviderCtrWiresProbeToGivenPorts 见文件头。
//
// `actions` 故意交 nil：档案四动作与自检是两条独立的依赖，把自检绑在"必须同时有档案面"上，
// 就会让"只想跑自检"的装配（以及这条用例）无法成立——而这正是这一刀要证明的独立性。
func TestNewProviderCtrWiresProbeToGivenPorts(t *testing.T) {
	const secret = "sk-super-secret-value-1234567890"
	arch := fakeProviderArchives{
		def: &provider.Def{
			ID: "p1", Name: "P1", Protocol: "openai", BaseURL: "https://api.openai.com/v1",
			Key: secret, KeySource: "managed",
		},
		refName: "providers/p1",
	}
	catalog := &fakeProviderCatalog{}
	ctr := NewProviderCtr(nil, arch, catalog)

	pc, ok := ctr.(*providerCtr)
	if !ok {
		t.Fatalf("NewProviderCtr 交出的类型不符: %T", ctr)
	}
	if pc.probe == nil {
		t.Fatal("自检用例没被造出来（`probe` 为 nil）：POST /providers/:id/probe 会直接 500")
	}

	resp, err := pc.probe.ProbeProvider(context.Background(), "p1", dtoprovider.ProviderProbeReq{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(resp.Checks) == 0 {
		t.Fatal("自检一条结论都没有：接进来的端口没被用上")
	}
	// 结论里必须报**引用名**（ADR-16），且绝不出现密钥真值——两条一起才说明"接的是这个域面"。
	var secretCheck string
	for _, c := range resp.Checks {
		if strings.Contains(c.Detail, secret) || strings.Contains(c.Remedy, secret) {
			t.Fatalf("自检文案泄漏了密钥真值: %+v", c)
		}
		if c.Name == "secret" {
			secretCheck = c.Detail
		}
	}
	if !strings.Contains(secretCheck, arch.refName) {
		t.Fatalf("secret 那条应报凭据**引用名** %q（报值或报空都说明端口接错了）: %q", arch.refName, secretCheck)
	}
	if !catalog.called {
		t.Error("模型目录端口没被调用：`catalog` 那条结论会退化成 unknown（装配漏接是静默的）")
	}
}
