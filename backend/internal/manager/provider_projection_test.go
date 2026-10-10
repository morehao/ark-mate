package manager

import (
	"context"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— H1/H2：provider 的逐 runtime 运行时参数声明（`body.projections.<runtime>`） ———

// TestProviderProjectionsRoundTrip H1：声明 → 原样读回（未知键保留）；只改一份不抹另一份。
func TestProviderProjectionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t) // 注册了带 ConfigProjection 的 opencode；另有 stub（无落点）
	key := "sk-x"
	if _, err := r.svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{
		ID: "p1", Type: "deepseek", Key: &key,
		Projections: map[string]map[string]any{
			"opencode": {
				"package": "@ai-sdk/openai-compatible",
				"api":     "openai-completions",
				"baseURL": "https://gateway.example/v1",
				// 未知键（平台不认识）：必须原样保留（与 body 里其它未知键同一条纪律）
				"futureKey": map[string]any{"nested": true},
			},
		},
	}); err != nil {
		t.Fatalf("带运行时参数新建: %v", err)
	}
	row, found, err := r.svc.st.GetProviderDefinition(ctx, "p1")
	if err != nil || !found {
		t.Fatalf("读行: found=%v err=%v", found, err)
	}
	for _, want := range []string{"@ai-sdk/openai-compatible", "openai-completions", "gateway.example", "futureKey"} {
		if !strings.Contains(row.Body, want) {
			t.Fatalf("正文里应保留 %q: %s", want, row.Body)
		}
	}
	// 只改 dsh 那一份 → opencode 那份**不能被抹掉**（逐 runtime 合并，不是整份替换）。
	if _, err := r.svc.UpdateProvider(ctx, "p1", dtoprovider.ProviderPatchReq{
		Projections: map[string]map[string]any{
			"deepseekharness": {"api": "openai-completions", "baseURL": "https://gw.example/v1"},
		},
	}); err != nil {
		t.Fatalf("补 dsh 那一份: %v", err)
	}
	row, _, _ = r.svc.st.GetProviderDefinition(ctx, "p1")
	for _, want := range []string{"opencode", "deepseekharness", "@ai-sdk/openai-compatible"} {
		if !strings.Contains(row.Body, want) {
			t.Fatalf("两份声明都应在: 缺 %q → %s", want, row.Body)
		}
	}
	// 空对象 = 明确删掉这一份。
	if _, err := r.svc.UpdateProvider(ctx, "p1", dtoprovider.ProviderPatchReq{
		Projections: map[string]map[string]any{"deepseekharness": {}},
	}); err != nil {
		t.Fatal(err)
	}
	row, _, _ = r.svc.st.GetProviderDefinition(ctx, "p1")
	if strings.Contains(row.Body, "deepseekharness") {
		t.Fatalf("空对象应删掉那一份: %s", row.Body)
	}
	if !strings.Contains(row.Body, "opencode") {
		t.Fatalf("另一份不该被动: %s", row.Body)
	}
}

// TestProviderProjectionsValidation H1：未注册的 runtime、类型错、未声明落点的 runtime 都要被拒。
func TestProviderProjectionsValidation(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	key := "sk-x"

	cases := []struct {
		name string
		proj map[string]map[string]any
		want string
	}{
		{"unknown_runtime", map[string]map[string]any{"ghost": {"api": "x"}}, "未注册"},
		{"wrong_type", map[string]map[string]any{"opencode": {"api": 123}}, "应为字符串"},
		{"no_projection_declared", map[string]map[string]any{"stub": {"api": "x"}}, "不承载平台模型清单"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{
				ID: "bad-" + tc.name, Type: "deepseek", Key: &key, Projections: tc.proj,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应被拒且文案含 %q，got %v", tc.want, err)
			}
			if errCode(err) != "invalid_param" && errCode(err) != "runtime_not_found" {
				t.Fatalf("应带可判定的码: %q", errCode(err))
			}
		})
	}
	// 正例：opencode + 合法字段。
	if _, err := r.svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{
		ID: "good", Type: "deepseek", Key: &key,
		Projections: map[string]map[string]any{"opencode": {"api": "openai-completions", "baseURL": "https://x/v1"}},
	}); err != nil {
		t.Fatalf("合法声明应通过: %v", err)
	}
}

// TestProviderApplicabilityCarriesProjectionSummary H2：`GET /providers` 的逐 runtime 可达性里
// 要出现"已声明运行时参数（api=…、baseURL=…）"。
func TestProviderApplicabilityCarriesProjectionSummary(t *testing.T) {
	ctx := context.Background()
	r := newModelRig(t)
	key := "sk-x"
	if _, err := r.svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{
		ID: "p1", Type: "deepseek", Key: &key,
		Projections: map[string]map[string]any{"opencode": {"api": "openai-completions", "baseURL": "https://gw/v1"}},
	}); err != nil {
		t.Fatal(err)
	}
	items, _ := r.svc.ListProviders()
	if len(items) != 1 {
		t.Fatalf("列表面不符: %+v", items)
	}
	var oc *string
	for _, a := range items[0].Applicability {
		if a.Runtime == "opencode" {
			reason := a.Reason
			oc = &reason
		}
	}
	if oc == nil {
		t.Fatalf("应有 opencode 那条可达性: %+v", items[0].Applicability)
	}
	if !strings.Contains(*oc, "已声明运行时参数") || !strings.Contains(*oc, "baseURL") {
		t.Fatalf("reason 应带上运行时参数摘要: %q", *oc)
	}
}

// TestProjectionSchemaIsDeclaredByAdapter H3 的数据源：`GET /runtimes` 的 provider_projection
// 由 adapter 声明派生（两个底座字段集不同、加底座不用改前端）。
func TestProjectionSchemaIsDeclaredByAdapter(t *testing.T) {
	svc := NewService(runtime.DefaultRegistry(), eventbus.New())
	items, err := svc.ListRuntimes()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]bool{}
	for _, it := range items {
		if it.ProviderProjection == nil {
			t.Fatalf("%s 应透出 provider_projection schema（它声明了 ConfigProjection）", it.ID)
		}
		set := map[string]bool{}
		for _, f := range it.ProviderProjection.Fields {
			set[f.Name] = true
		}
		byID[it.ID] = set
	}
	// 两者共有的：api / baseURL / compat。
	for _, common := range []string{"api", "baseURL", "compat"} {
		if !byID["opencode"][common] || !byID["deepseekharness"][common] {
			t.Fatalf("两个底座的 schema 都应含 %q: %+v", common, byID)
		}
	}
	// opencode 独有：package / options（dsh 不需要——它走 patch 的 providers.<route>）。
	if !byID["opencode"]["package"] || !byID["opencode"]["options"] {
		t.Fatalf("opencode 的 schema 应含 package/options: %+v", byID["opencode"])
	}
	if byID["deepseekharness"]["package"] {
		t.Fatalf("dsh 的 schema 不该含 package（那是 opencode 侧的加载方式）: %+v", byID["deepseekharness"])
	}
}
