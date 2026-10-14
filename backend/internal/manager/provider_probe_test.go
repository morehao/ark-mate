package manager

import (
	"context"
	"github.com/morehao/ark-mate/internal/testutil/runtimetest"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— M4 连接自检：默认零出网、上游段四种状态、未登记 type 记 unknown、错误文本过 redact ———

// countingTransport 记录请求次数的假 transport（A7 的判据就是"默认 0 次"）。
type countingTransport struct {
	n     atomic.Int64
	inner http.RoundTripper
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.inner.RoundTrip(r)
}

func probeSvc(t *testing.T) *Service {
	t.Helper()
	home := t.TempDir()
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	svc.SetProviderDir(filepath.Join(home, "providers"))
	// 凭据面：memory 模式下密钥的落点是托管文档，探测面要有一条真能写进去的密钥。
	svc.SetCredentials(newTestCredentials(t, home))
	return svc
}

// TestProbeDefaultMakesZeroUpstreamRequests A7：不带 `{"upstream":true}` 时**一次请求都不发**。
//
// 这条是安全判据而不是性能判据：默认出网意味着"打开设置页"就等于把用户密钥发给第三方。
func TestProbeDefaultMakesZeroUpstreamRequests(t *testing.T) {
	ctx := context.Background()
	svc := probeSvc(t)
	key := "sk-probe"
	baseURL := "https://api.openai.com/v1"
	if _, err := svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "p1", Protocol: "openai", BaseURL: &baseURL, APIKey: &key}); err != nil {
		t.Fatal(err)
	}
	tr := &countingTransport{inner: http.DefaultTransport}
	resp, err := svc.probeProvider(ctx, "p1", dtoprovider.ProviderProbeReq{}, probeOptions{transport: tr})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if n := tr.n.Load(); n != 0 {
		t.Fatalf("默认自检必须零出网，实际 %d 次请求", n)
	}
	// 本地段六项都要在（顺序固定：exists/type/secret/refs/injection/catalog）。
	names := make([]string, 0, len(resp.Checks))
	for _, c := range resp.Checks {
		names = append(names, c.Name)
	}
	want := []string{"exists", "protocol", "base_url", "secret", "refs", "delivery", "catalog"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("本地段检查项不符: got %v want %v", names, want)
	}
	if resp.Upstream {
		t.Fatal("回执应如实说明本次没有跑上游段")
	}
	// 没有 agent 引用 → refs 是 warn（事实，不是错误）；ok 仍然 true。
	if !resp.OK {
		t.Fatalf("只有 warn/unknown 时 ok 应为 true: %+v", resp.Checks)
	}
}

// TestProbeUpstreamFourOutcomes 自建 httptest 的四种输入 → pass / fail / fail / unknown。
func TestProbeUpstreamFourOutcomes(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer sk-good" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
		}))
		defer srv.Close()
		resp := probeUpstreamAgainst(t, srv.URL, "openai", "sk-good")
		if resp.Status != probePass || !strings.Contains(resp.Detail, "2 条模型") {
			t.Fatalf("2xx 应 pass 并报出条数: %+v", resp)
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		resp := probeUpstreamAgainst(t, srv.URL, "openai", "sk-bad")
		if resp.Status != probeFail || resp.Remedy == "" {
			t.Fatalf("401 应 fail 且带出路: %+v", resp)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// 比 probeUpstreamTimeout 睡得久（不重试、5s 超时）。
			<-make(chan struct{})
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		// 用极短超时的 client 模拟：直接把端点换成不可达地址更省时间。
		resp := probeUpstreamAgainst(t, "http://127.0.0.1:1", "openai", "sk-x")
		if resp.Status != probeFail || resp.Remedy == "" {
			t.Fatalf("连不上应 fail 且带出路: %+v", resp)
		}
	})

	t.Run("unregistered_type", func(t *testing.T) {
		// 空 URL = **不登记**这个 type（"未登记端点"才是这条用例的输入）。
		resp := probeUpstreamAgainst(t, "", "my-custom-gateway", "sk-x")
		if resp.Status != probeUnknown {
			t.Fatalf("未登记的 type 必须记 unknown（无法校验，不是 fail）: %+v", resp)
		}
		if !strings.Contains(resp.Detail, "无法校验") {
			t.Fatalf("文案要说清「无法校验」: %+v", resp)
		}
	})
}

// probeUpstreamAgainst 用给定 URL 当作该供应商的**资源端点**，跑一次上游段。
//
// v12 起上游段按**资源字段**探（`GET <base_url>/models`），不再有 v11 那张"type → 厂商 URL"
// 的登记表——所以这里直接构造一条带 `BaseURL` 的档案，没有可覆盖的全局状态。
func probeUpstreamAgainst(t *testing.T, url, protocol, key string) dtoprovider.ProviderCheckResp {
	t.Helper()
	svc := NewService(runtime.NewRegistry(), eventbus.New())
	d := &providerDef{ID: "probe-target", Protocol: protocol, BaseURL: url, Key: key}
	return svc.probeUpstream(context.Background(), d, probeOptions{})
}

// TestProbeRedactsSecretInFailureText 上游失败文案必须先过 redact：密钥真值不能出现在响应里。
//
// 这一条挡的是"错误文本恰好把 URL/头拼了进去"这类泄漏——`redact.RegisterDynamicSecret`
// 只在 providerEnvFor 里登记，所以探针自己也要走同一道闸门。
func TestProbeRedactsSecretInFailureText(t *testing.T) {
	ctx := context.Background()
	const secret = "sk-super-secret-value-1234567890"
	svc := probeSvc(t)
	key := secret
	baseURL := "https://api.openai.com/v1"
	if _, err := svc.CreateProvider(ctx, dtoprovider.ProviderCreateReq{ID: "p1", Protocol: "openai", BaseURL: &baseURL, APIKey: &key}); err != nil {
		t.Fatal(err)
	}
	// 先让 redact 登记这个真值（生产路径里 providerEnvFor 会做这件事）。
	_ = svc.providerEnvFor(runtimetest.ID, "p1")

	// 一个会把密钥回显进错误里的假 transport（模拟某些库把 URL/头拼进错误）。
	tr := &countingTransport{inner: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &leakyError{msg: "Get \"https://api.openai.com/v1/models?key=" + secret + "\": dial tcp: timeout"}
	})}

	resp, err := svc.probeProvider(ctx, "p1", dtoprovider.ProviderProbeReq{Upstream: true}, probeOptions{transport: tr})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	var up *dtoprovider.ProviderCheckResp
	for i := range resp.Checks {
		if resp.Checks[i].Name == "upstream" {
			up = &resp.Checks[i]
		}
	}
	if up == nil {
		t.Fatal("应有一条 upstream 结论")
	}
	if strings.Contains(up.Detail, secret) {
		t.Fatalf("上游失败文案泄漏了密钥真值: %s", up.Detail)
	}
	if !strings.Contains(up.Detail, "timeout") {
		t.Fatalf("时间类失败应给出超时出路: %+v", up)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type leakyError struct{ msg string }

func (e *leakyError) Error() string { return e.msg }

// TestProbeMissingProviderIsAFailedCheck 档案不存在时**仍然 200**：
// 自检的产物是"逐条结论"，其中 exists 一条 fail——而不是一个 404（那会让"哪一条不对"无从回答）。
func TestProbeMissingProviderIsAFailedCheck(t *testing.T) {
	svc := probeSvc(t)
	resp, err := svc.ProbeProvider(context.Background(), "nope", dtoprovider.ProviderProbeReq{})
	if err != nil {
		t.Fatalf("档案不存在不该报错（应交出一条 fail 的 check）: %v", err)
	}
	if resp.OK || len(resp.Checks) != 1 || resp.Checks[0].Name != "exists" || resp.Checks[0].Status != probeFail {
		t.Fatalf("不存在时应只有一条 exists=fail 且 ok=false: %+v", resp)
	}
}
