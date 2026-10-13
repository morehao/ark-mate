package runtime

import "testing"

// TestAdapterProjectionDeclarations §6.2 投影声明值守门：传输能力集合、ManagesOwnContext、
// Description 非空；ModeMapping 本轮留空（精确映射待实测，见 §12）。
func TestAdapterProjectionDeclarations(t *testing.T) {
	cases := []struct {
		id     string
		spec   Spec
		has    []string
		notHas []string
	}{
		{id: altID, spec: altSpec(), has: []string{"stdio", "sse", "http"}, notHas: []string{"streamable_http"}},
		{id: "dsh", spec: dshSpec(), has: []string{"stdio", "streamable_http"}, notHas: []string{"sse", "http"}},
	}
	for _, c := range cases {
		s := c.spec
		if s.ID != c.id {
			t.Fatalf("%s spec id 不符: %q", c.id, s.ID)
		}
		if !s.ManagesOwnContext {
			t.Fatalf("%s 应声明自管上下文（§6.2）", c.id)
		}
		if s.Description == "" {
			t.Fatalf("%s 缺投影描述", c.id)
		}
		got := make(map[string]bool, len(s.McpTransports))
		for _, tr := range s.McpTransports {
			got[tr] = true
		}
		for _, tr := range c.has {
			if !got[tr] {
				t.Fatalf("%s 应支持传输 %q，got %v", c.id, tr, s.McpTransports)
			}
		}
		for _, tr := range c.notHas {
			if got[tr] {
				t.Fatalf("%s 不应支持传输 %q，got %v", c.id, tr, s.McpTransports)
			}
		}
		if s.ModeMapping != nil {
			t.Fatalf("%s ModeMapping 本轮应留空（待实测），got %v", c.id, s.ModeMapping)
		}
	}
}
