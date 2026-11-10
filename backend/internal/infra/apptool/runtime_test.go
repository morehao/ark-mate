package apptool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
)

// stubHost 是一个运行期 host 的桩：自述固定工具集、记录被调工具、可注入 RPC 错误。
type stubHost struct {
	tools     []ToolDesc
	listErr   error
	invokeErr error
	called    []string
}

func (h *stubHost) ListTools() ([]ToolDesc, error) {
	if h.listErr != nil {
		return nil, h.listErr
	}
	return h.tools, nil
}

func (h *stubHost) InvokeTool(name string, args []byte) ([]byte, bool, error) {
	h.called = append(h.called, name)
	if h.invokeErr != nil {
		return nil, false, h.invokeErr
	}
	content, _ := json.Marshal(map[string]string{"type": "text", "text": "host:" + name})
	return content, false, nil
}

func hostWith(tools ...ToolDesc) *stubHost { return &stubHost{tools: tools} }

// TestResolveRuntimeDomainsSplitsByPrefix 自述里工具按名字首段分域，每域一个 handler。
func TestResolveRuntimeDomainsSplitsByPrefix(t *testing.T) {
	host := hostWith(
		ToolDesc{Name: "foo_create", InputSchema: json.RawMessage(`{}`)},
		ToolDesc{Name: "foo_list", InputSchema: json.RawMessage(`{}`)},
		ToolDesc{Name: "bar_ping", InputSchema: json.RawMessage(`{}`)},
	)
	hs, err := ResolveRuntimeDomains("pkg-a", host)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs.Handlers) != 2 {
		t.Fatalf("应解析出 foo/bar 两个域，got %v", keysOf(hs.Handlers))
	}
	// foo 域 ListTools 只列 foo 的工具。
	tools, err := hs.Handlers["foo"].ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("foo 域应有 2 工具，got %d", len(tools))
	}
}

// TestRuntimeDomainHandlerForwardsInvoke 调用转发到 host.InvokeTool，content 透传。
func TestRuntimeDomainHandlerForwardsInvoke(t *testing.T) {
	host := hostWith(ToolDesc{Name: "foo_create", InputSchema: json.RawMessage(`{}`)})
	hs, _ := ResolveRuntimeDomains("pkg-a", host)
	res, err := hs.Handlers["foo"].CallTool(context.Background(), "foo_create", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(host.called) != 1 || host.called[0] != "foo_create" {
		t.Fatalf("应转发 foo_create 到 host，got %v", host.called)
	}
	if res.IsError || len(res.Content) != 1 {
		t.Fatalf("应透传 content，got %+v", res)
	}
}

// TestRuntimeDomainHandlerRejectsForeignTool 非本域工具 → ErrToolNotFound（不越界转发）。
func TestRuntimeDomainHandlerRejectsForeignTool(t *testing.T) {
	host := hostWith(ToolDesc{Name: "foo_create", InputSchema: json.RawMessage(`{}`)})
	hs, _ := ResolveRuntimeDomains("pkg-a", host)
	if _, err := hs.Handlers["foo"].CallTool(context.Background(), "bar_ping", nil); err != mcp.ErrToolNotFound {
		t.Fatalf("非本域工具应返回 ErrToolNotFound，got %v", err)
	}
	if len(host.called) != 0 {
		t.Fatalf("不该把非本域工具转发到 host，got %v", host.called)
	}
}

// TestCheckDeclaredDomains 声明了域但自述没有 → 报缺失（§13 FAILED 依据）。
func TestCheckDeclaredDomains(t *testing.T) {
	host := hostWith(ToolDesc{Name: "foo_create", InputSchema: json.RawMessage(`{}`)})
	hs, _ := ResolveRuntimeDomains("pkg-a", host)

	if miss := CheckDeclaredDomains([]string{"foo"}, hs); len(miss) != 0 {
		t.Fatalf("foo 已兑现，不应报缺失，got %v", miss)
	}
	if miss := CheckDeclaredDomains([]string{"foo", "nope"}, hs); len(miss) != 1 || miss[0] != "nope" {
		t.Fatalf("nope 未兑现应报缺失，got %v", miss)
	}
	if miss := CheckDeclaredDomains(nil, hs); len(miss) != 0 {
		t.Fatalf("未声明域不应报缺失，got %v", miss)
	}
}

// TestSelectRuntimeDomains 以清单声明为准收窄；声明未兑现域 → fail-closed 报错。
func TestSelectRuntimeDomains(t *testing.T) {
	host := hostWith(
		ToolDesc{Name: "foo_create", InputSchema: json.RawMessage(`{}`)},
		ToolDesc{Name: "bar_ping", InputSchema: json.RawMessage(`{}`)},
	)
	hs, _ := ResolveRuntimeDomains("pkg-a", host)

	if got, err := SelectRuntimeDomains(hs, nil); err != nil || got != nil {
		t.Fatalf("声明为空应 (nil,nil)，got %v %v", got, err)
	}
	got, err := SelectRuntimeDomains(hs, []string{"foo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["foo"]; !ok || len(got) != 1 {
		t.Fatalf("应只收窄到 foo，got %v", keysOf(got))
	}
	// 声明了未兑现的域 → fail-closed。
	if _, err := SelectRuntimeDomains(hs, []string{"foo", "nope"}); err == nil {
		t.Fatal("声明未兑现域必须 fail-closed 报错")
	}
}

// TestResolveRuntimeDomainsListError 读不到自述 → error（整个运行期 app 工具面 FAILED）。
func TestResolveRuntimeDomainsListError(t *testing.T) {
	host := &stubHost{listErr: errListBoom{}}
	if _, err := ResolveRuntimeDomains("pkg-a", host); err == nil {
		t.Fatal("listTools 失败应返回 error")
	}
}

type errListBoom struct{}

func (errListBoom) Error() string { return "listTools RPC 超时" }

// TestMergeDomainsCompiledAndRuntime 编译期 ∥ 运行期并列合并；同域前缀撞车 fail-closed。
func TestMergeDomainsCompiledAndRuntime(t *testing.T) {
	compiled := map[string]mcp.Handler{"foo": &stubDomain{prefix: "foo"}}
	runtime := map[string]mcp.Handler{"bar": &stubDomain{prefix: "bar"}}

	merged, err := MergeDomains(compiled, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 2 || merged["foo"] == nil || merged["bar"] == nil {
		t.Fatalf("应并列合并 foo+bar，got %v", keysOf(merged))
	}

	// 同域前缀撞车 → fail-closed。
	clash := map[string]mcp.Handler{"foo": &stubDomain{prefix: "foo"}}
	if _, err := MergeDomains(compiled, clash); err == nil {
		t.Fatal("同域前缀同时被编译期与运行期占用必须 fail-closed")
	}

	// 两边都空 → (nil,nil)（默认零注入）。
	if m, err := MergeDomains(nil, nil); err != nil || m != nil {
		t.Fatalf("两边都空应 (nil,nil)，got %v %v", m, err)
	}
}

// TestAggregateCompiledAndRuntime 合并后经 platformmcp 前缀分派：编译期与运行期工具都能被路由。
func TestAggregateCompiledAndRuntime(t *testing.T) {
	compiledHost := &stubDomain{prefix: "foo"}
	rtHost := hostWith(ToolDesc{Name: "bar_ping", InputSchema: json.RawMessage(`{}`)})
	hs, _ := ResolveRuntimeDomains("pkg-a", rtHost)

	agg, err := AggregateCompiledAndRuntime(map[string]mcp.Handler{"foo": compiledHost}, hs.Handlers)
	if err != nil {
		t.Fatal(err)
	}
	if agg == nil {
		t.Fatal("有域时应返回聚合器")
	}
	if got := strings.Join(agg.Domains(), ","); got != "bar,foo" {
		t.Fatalf("域应升序 bar,foo，got %q", got)
	}
	// 工具合并：编译期 foo 域 2 工具 + 运行期 bar 域 1 工具。
	tools, err := agg.Handler().ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 {
		t.Fatalf("应合并 3 工具，got %d", len(tools))
	}
	// 编期域分派到编译期 handler。
	if _, err := agg.Handler().CallTool(context.Background(), "foo_create", nil); err != nil {
		t.Fatal(err)
	}
	if compiledHost.called != "foo_create" {
		t.Fatalf("foo_create 应分派到编译期 handler，got %q", compiledHost.called)
	}
	// 运行期域分派到运行期 handler（转发 host）。
	if _, err := agg.Handler().CallTool(context.Background(), "bar_ping", nil); err != nil {
		t.Fatal(err)
	}
	if len(rtHost.called) != 1 || rtHost.called[0] != "bar_ping" {
		t.Fatalf("bar_ping 应转发到运行期 host，got %v", rtHost.called)
	}
	// 两边都空 → 聚合器为 nil（默认零注入）。
	if a, err := AggregateCompiledAndRuntime(nil, nil); err != nil || a != nil {
		t.Fatalf("空集合应 (nil,nil)，got %v %v", a, err)
	}
}

// keysOf 返回 map 的键（排序后，调试用）。
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
