package apptool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
)

// stubDomain 最小域 handler：工具名固定前缀 + 记录被调（与 platformmcp 测试同款，验证"复用前缀分派"）。
type stubDomain struct {
	prefix string
	called string
}

func (d *stubDomain) ListTools(context.Context) ([]mcp.Tool, error) {
	return []mcp.Tool{
		{Name: d.prefix + "_create", InputSchema: json.RawMessage(`{}`)},
		{Name: d.prefix + "_list", InputSchema: json.RawMessage(`{}`)},
	}, nil
}

func (d *stubDomain) CallTool(_ context.Context, name string, _ json.RawMessage) (mcp.CallResult, error) {
	d.called = name
	blk, _ := json.Marshal(map[string]string{"type": "text", "text": d.prefix + ":" + name})
	return mcp.CallResult{Content: []json.RawMessage{blk}}, nil
}

// TestNewAggregatorRejectsUnderscoreDomain 域键含下划线 fail-closed（前缀分派约束，§5.3）。
func TestNewAggregatorRejectsUnderscoreDomain(t *testing.T) {
	_, err := NewAggregator(map[string]mcp.Handler{"app_foo": &stubDomain{prefix: "app_foo"}})
	if err == nil {
		t.Fatal("域键含下划线必须 fail-closed 拒绝（前缀分派要求单段无下划线）")
	}
}

// TestNewAggregatorRejectsEmpty 空域 map 报错（无注入意义，掩盖调用方 bug）。
func TestNewAggregatorRejectsEmpty(t *testing.T) {
	if _, err := NewAggregator(nil); err == nil {
		t.Fatal("空域集合必须报错")
	}
}

// TestAggregatorPrefixDispatch 复用 platformmcp 的前缀分派：按工具名首段路由到对应域。
func TestAggregatorPrefixDispatch(t *testing.T) {
	a, b := &stubDomain{prefix: "foo"}, &stubDomain{prefix: "bar"}
	agg, err := NewAggregator(map[string]mcp.Handler{"foo": a, "bar": b})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(agg.Domains(), ","); got != "bar,foo" {
		t.Fatalf("域列表应升序稳定 bar,foo，got %q", got)
	}
	tools, err := agg.Handler().ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 4 {
		t.Fatalf("两域各 2 工具应合并为 4，got %d", len(tools))
	}
	// 分派到 foo 域。
	if _, err := agg.Handler().CallTool(context.Background(), "foo_create", nil); err != nil {
		t.Fatal(err)
	}
	if a.called != "foo_create" {
		t.Fatalf("foo_create 应分派到 foo 域，got called=%q", a.called)
	}
	if b.called != "" {
		t.Fatalf("bar 域不该被调用，got %q", b.called)
	}
	// 未知域 → ErrToolNotFound（协议层错误）。
	if _, err := agg.Handler().CallTool(context.Background(), "baz_create", nil); err != mcp.ErrToolNotFound {
		t.Fatalf("未知域应返回 ErrToolNotFound，got %v", err)
	}
}

// TestSelectDomains 从全量注册表按 want 收窄；缺失域 fail-closed 报错；空 want → nil（零注入）。
func TestSelectDomains(t *testing.T) {
	all := map[string]mcp.Handler{"foo": &stubDomain{prefix: "foo"}, "bar": &stubDomain{prefix: "bar"}}

	if got, err := SelectDomains(all, nil); err != nil || got != nil {
		t.Fatalf("want 为空应返回 (nil,nil)（零注入），got %v %v", got, err)
	}

	got, err := SelectDomains(all, []string{"foo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["foo"]; !ok || len(got) != 1 {
		t.Fatalf("应只收窄到 foo，got %v", got)
	}

	if _, err := SelectDomains(all, []string{"foo", "missing"}); err == nil {
		t.Fatal("want 里有未登记域必须 fail-closed 报错（不静默丢弃）")
	}
}
