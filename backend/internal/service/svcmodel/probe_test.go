package svcmodel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/credential"

	"github.com/morehao/ark-mate/internal/core/redact"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
)

// ——— P4a：供应商自检用例的用例级夹具 ———
//
// 这一批用例原来住在 `internal/manager`（那时自检是 `*Service` 的方法）。搬到本包之后，
// 它们只能拿**用例层声明的两个端口**（`Ports` / `Catalog`）——这正是 P4a 想要的效果：
// 判据不再依赖"一个什么都有的 Service"，也不需要凭据面、库、编排器。
//
// 真接链路的那半（建档 → 密钥真值 → `providerEnvFor` 登记动态密钥 → 自检文案不带真值）
// **刻意留在 manager**：它验的是"装配期登记过的密钥在用例层也被遮住"，那要真的凭据面与域平面。
// 见 `internal/manager/provider_probe_test.go`。两半分工写在两处，改动时别只改一边。

// fakeProbePorts 用例层声明的供应商读面（ADR-6）的最小实现。
type fakeProbePorts struct {
	def       *provider.Def
	refName   string
	locator   string
	modelRefs []string
}

func (f fakeProbePorts) Snapshot(string) (*provider.Def, bool) {
	if f.def == nil {
		return nil, false
	}
	c := *f.def
	return &c, true
}

func (f fakeProbePorts) ProviderRefName(string) string { return f.refName }

func (f fakeProbePorts) ProviderLocator(string) string { return f.locator }

func (f fakeProbePorts) ProviderModelRefsReadOnly(context.Context, string) []string {
	return append([]string(nil), f.modelRefs...)
}

// fakeProbeCatalog 模型目录读面：`err` 非 nil 时模拟"目录读不动"，`resp` 为 nil 时模拟
// "端口回了空结论"（两者都必须记 unknown 而不是崩）。
type fakeProbeCatalog struct {
	resp *provider.CatalogView
	err  error
}

func (f fakeProbeCatalog) ModelCatalog(context.Context) (*provider.CatalogView, error) {
	return f.resp, f.err
}

// probeFixture 造一个自检用例。`def == nil` 表示档案不在册。
func probeFixture(def *provider.Def, cat Catalog) *Probe {
	return NewProbe(fakeProbePorts{
		def: def, refName: "MATE_PROVIDER_p1_KEY", locator: "db:provider/p1",
	}, cat)
}

// wantCheckNames 断言检查项的名字与顺序。
func wantCheckNames(t *testing.T, checks []dtomodel.ProviderCheckResp, want ...string) {
	t.Helper()
	got := make([]string, 0, len(checks))
	for _, c := range checks {
		got = append(got, c.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("检查项不符: got %v want %v", got, want)
	}
}

// countModelsCheck 摘出某一条结论（不存在即失败）。
func findCheck(t *testing.T, checks []dtomodel.ProviderCheckResp, name string) dtomodel.ProviderCheckResp {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("没有名为 %q 的结论：%+v", name, checks)
	return dtomodel.ProviderCheckResp{}
}

// ——— M4 连接自检：默认零出网、上游段四种状态、未登记端点记 unknown、错误文本过 redact ———

// countingTransport 记录请求次数的假 transport（A7 的判据就是"默认 0 次"）。
type countingTransport struct {
	n     atomic.Int64
	inner http.RoundTripper
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.inner.RoundTrip(r)
}

// TestProbeDefaultMakesZeroUpstreamRequests A7：不带 `{"upstream":true}` 时**一次请求都不发**。
//
// 这条是安全判据而不是性能判据：默认出网意味着"打开设置页"就等于把用户密钥发给第三方。
func TestProbeDefaultMakesZeroUpstreamRequests(t *testing.T) {
	ctx := context.Background()
	baseURL := "https://api.openai.com/v1"
	p := probeFixture(&provider.Def{
		ID: "p1", Protocol: "openai", BaseURL: baseURL,
		Key: "sk-probe", KeySource: credential.SourceManaged,
	}, fakeProbeCatalog{})

	tr := &countingTransport{inner: http.DefaultTransport}
	resp, err := p.probeProvider(ctx, "p1", dtomodel.ProviderProbeReq{}, probeOptions{transport: tr})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if n := tr.n.Load(); n != 0 {
		t.Fatalf("默认自检必须零出网，实际 %d 次请求", n)
	}
	// 本地段六项都要在（顺序固定：exists/protocol/base_url/secret/refs/delivery/catalog）。
	wantCheckNames(t, resp.Checks, "exists", "protocol", "base_url", "secret", "refs", "delivery", "catalog")
	if resp.Upstream {
		t.Fatal("回执应如实说明本次没有跑上游段")
	}
	// 没有模型条目引用 → refs 是 warn（事实，不是错误）；ok 仍然 true。
	if !resp.OK {
		t.Fatalf("只有 warn/unknown 时 ok 应为 true: %+v", resp.Checks)
	}
	if got := findCheck(t, resp.Checks, "exists").Detail; !strings.Contains(got, "db") {
		t.Fatalf("exists 应报**用户词**载体（`locatorKind` 折算后的 db/file），实际 %q", got)
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

	t.Run("no_endpoint", func(t *testing.T) {
		// 空 URL = **没有端点**（"无法校验"才是这条用例的输入）。
		resp := probeUpstreamAgainst(t, "", "my-custom-gateway", "sk-x")
		if resp.Status != probeUnknown {
			t.Fatalf("没有端点必须记 unknown（无法校验，不是 fail）: %+v", resp)
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
func probeUpstreamAgainst(t *testing.T, url, protocol, key string) dtomodel.ProviderCheckResp {
	t.Helper()
	p := probeFixture(nil, nil)
	return p.probeUpstream(context.Background(),
		&provider.Def{ID: "probe-target", Protocol: protocol, BaseURL: url, Key: key}, probeOptions{})
}

// TestProbeRedactsSecretInFailureText 上游失败文案必须先过 redact：密钥真值不能出现在响应里。
//
// 这一条挡的是"错误文本恰好把 URL/头拼了进去"这类泄漏。自检段**自己**要过这道闸门的原因：
// 动态密钥的登记发生在 `providerEnvFor`（注入子进程那条路），而自检是一个**不注入**也能被
// 触发的读面——面板上点一下就会走。管理器侧的端到端版本（真的建档 → 真的登记 → 真的自检）
// 在 `internal/manager/provider_probe_test.go`，两条一起才覆盖"闸门在、且这条路上真的经过它"。
func TestProbeRedactsSecretInFailureText(t *testing.T) {
	const secret = "sk-super-secret-value-1234567890"
	unregister := redact.RegisterDynamicSecret(secret)
	defer unregister()

	tr := &countingTransport{inner: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &leakyError{msg: "Get \"https://api.openai.com/v1/models?key=" + secret + "\": dial tcp: timeout"}
	})}
	p := probeFixture(nil, nil)
	up := p.probeUpstream(context.Background(),
		&provider.Def{ID: "p1", Protocol: "openai", BaseURL: "https://api.openai.com/v1", Key: secret},
		probeOptions{transport: tr})

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
	p := probeFixture(nil, nil)
	resp, err := p.ProbeProvider(context.Background(), "nope", dtomodel.ProviderProbeReq{})
	if err != nil {
		t.Fatalf("档案不存在不该报错（应交出一条 fail 的 check）: %v", err)
	}
	if resp.OK || len(resp.Checks) != 1 || resp.Checks[0].Name != "exists" || resp.Checks[0].Status != probeFail {
		t.Fatalf("不存在时应只有一条 exists=fail 且 ok=false: %+v", resp)
	}
}

// TestProbeCatalogUnknownPathsStayUnknown 目录段的三种"没有结论"都不许变成 fail、也不许崩：
//
//	catalog == nil       —— 本进程没执行面（demo/migrate 形态）；
//	端口回 err           —— 目录读不动（运行期事实）；
//	端口回 (nil, nil)    —— 装配缺陷（同一形状的另一个接口被交了下来）。
//
// 三种都记 unknown（**无法校验**不是失败），且**各有一条**出路——把三者混成一句的后果是
// 用户照着一条与他情况无关的指引排查（2026-10-15 那次"下拉里没有已配置的模型"就是这么来的）。
func TestProbeCatalogUnknownPathsStayUnknown(t *testing.T) {
	base := func() *provider.Def {
		return &provider.Def{ID: "p1", Protocol: "openai", BaseURL: "https://api.openai.com/v1",
			Key: "sk-x", KeySource: credential.SourceManaged}
	}

	t.Run("nil_catalog", func(t *testing.T) {
		p := NewProbe(fakeProbePorts{def: base(), refName: "R"}, nil)
		resp, err := p.ProbeProvider(context.Background(), "p1", dtomodel.ProviderProbeReq{})
		if err != nil {
			t.Fatal(err)
		}
		c := findCheck(t, resp.Checks, "catalog")
		if c.Status != probeUnknown || c.Remedy == "" {
			t.Fatalf("没有执行面应记 unknown 且带出路: %+v", c)
		}
	})

	t.Run("catalog_error", func(t *testing.T) {
		p := probeFixture(base(), fakeProbeCatalog{err: errors.New("boom")})
		resp, err := p.ProbeProvider(context.Background(), "p1", dtomodel.ProviderProbeReq{})
		if err != nil {
			t.Fatal(err)
		}
		c := findCheck(t, resp.Checks, "catalog")
		if c.Status != probeUnknown || !strings.Contains(c.Detail, "boom") {
			t.Fatalf("读不动应记 unknown 并如实带上原因: %+v", c)
		}
	})

	t.Run("nil_response", func(t *testing.T) {
		p := probeFixture(base(), fakeProbeCatalog{resp: nil})
		resp, err := p.ProbeProvider(context.Background(), "p1", dtomodel.ProviderProbeReq{})
		if err != nil {
			t.Fatal(err)
		}
		c := findCheck(t, resp.Checks, "catalog")
		if c.Status != probeUnknown || !strings.Contains(c.Detail, "内部装配问题") {
			t.Fatalf("回空结论应记 unknown 并说清是进程内部问题: %+v", c)
		}
	})

	t.Run("readable", func(t *testing.T) {
		p := probeFixture(base(), fakeProbeCatalog{resp: &provider.CatalogView{
			Runtime: "dsh", Source: "session",
			Options: []provider.CatalogOption{{ID: "model", Name: "模型"}},
		}})
		resp, err := p.ProbeProvider(context.Background(), "p1", dtomodel.ProviderProbeReq{})
		if err != nil {
			t.Fatal(err)
		}
		c := findCheck(t, resp.Checks, "catalog")
		if c.Status != probePass || !strings.Contains(c.Detail, "source=session") {
			t.Fatalf("目录可读应 pass: %+v", c)
		}
	})
}

// TestProbeUpstreamTimeoutIsBounded 上游段**不重试、有上限**：一个不响应的上游必须在
// `probeUpstreamTimeout` 内以 fail 收场，而不是把用户按在按钮上无限等。
//
// 判据取**真值读秒**（而不是"代码里写了 5s"）：常量被改大/被去掉超时，这条立刻从"秒级"
// 变成"挂死"，而 `go test` 的超时只会在整包层面报出来（看不出是哪一条）。
func TestProbeUpstreamTimeoutIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-time.After(3 * probeUpstreamTimeout)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Now()
	resp := probeUpstreamAgainst(t, srv.URL, "openai", "sk-x")
	elapsed := time.Since(start)
	if resp.Status != probeFail {
		t.Fatalf("上游不响应必须 fail: %+v", resp)
	}
	if elapsed > 3*probeUpstreamTimeout {
		t.Fatalf("上游段没有上限（耗时 %v）：单次请求、5s 超时是安全判据——把用户按在按钮上"+
			"无限等与「默认出网」是同一类问题", elapsed)
	}
}
