package platformmcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
)

// stubDomain 一个最小的域 handler：工具名固定前缀 + 记录被调。
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

func TestListToolsMergesDomains(t *testing.T) {
	a := &stubDomain{prefix: "automation"}
	e := &stubDomain{prefix: "expert"}
	h, err := New(map[string]mcp.Handler{"automation": a, "expert": e})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := h.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 4 {
		t.Fatalf("应合并 4 个工具，got %d", len(tools))
	}
	// 顺序按域前缀字典序：automation 在 expert 前。
	if tools[0].Name != "automation_create" || tools[2].Name != "expert_create" {
		t.Fatalf("域顺序应为 automation→expert，got %v", []string{tools[0].Name, tools[1].Name, tools[2].Name, tools[3].Name})
	}
}

func TestCallToolRoutesByPrefix(t *testing.T) {
	a := &stubDomain{prefix: "automation"}
	e := &stubDomain{prefix: "expert"}
	h, _ := New(map[string]mcp.Handler{"automation": a, "expert": e})

	// 多下划线名字必须按**首段**分派（expert_set_enabled → expert）。
	if _, err := h.CallTool(context.Background(), "expert_set_enabled", nil); err != nil {
		t.Fatal(err)
	}
	if e.called != "expert_set_enabled" || a.called != "" {
		t.Fatalf("应路由到 expert 域，got expert=%q automation=%q", e.called, a.called)
	}
	if _, err := h.CallTool(context.Background(), "automation_create", nil); err != nil {
		t.Fatal(err)
	}
	if a.called != "automation_create" {
		t.Fatalf("应路由到 automation 域，got %q", a.called)
	}
}

func TestCallToolUnknownPrefix(t *testing.T) {
	h, _ := New(map[string]mcp.Handler{"expert": &stubDomain{prefix: "expert"}})
	_, err := h.CallTool(context.Background(), "nope_x", nil)
	if err != mcp.ErrToolNotFound {
		t.Fatalf("未知前缀应回 ErrToolNotFound，got %v", err)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("空域集应报错")
	}
	if _, err := New(map[string]mcp.Handler{"a_b": &stubDomain{}}); err == nil {
		t.Fatal("含下划线的域前缀应报错")
	}
	if _, err := New(map[string]mcp.Handler{"expert": nil}); err == nil {
		t.Fatal("nil handler 应报错")
	}
}
