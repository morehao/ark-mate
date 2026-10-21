package gateway

// ——— M7-2 验收：数据面（取数 → 选址 → 解密钥 → 代理 + 连接池）———
//
// 这一组走**真 store + 真远端 MCP 服务端**（`httptest` 上的 http 传输，帧形状就是 MCP 的
// JSON-RPC），因为 M7-2 的验收条条都是"跨进程事实"：
//   - 停用一台 server → 下一次 tools/list 就不出现，且**它已有的会话**下一次 tools/call 立即失败；
//   - 密钥轮换 → 底座无感（不必重启、不必换代际），下一次调用就用新值；
//   - 有一台引用解析不了 → 只跳过它（看得见的降级），其余照常；
//   - 远端断开/报错 → 错误浮出来，但**不**把网关带走。
//
// 不 mock `Source` 的内部：mock 掉哪一层，就等于放弃那一层的证据。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/adapters/outbound/dsh"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/platform/resource"
	"github.com/morehao/ark-mate/internal/adapters/outbound/mcp"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/testutil"
)

// fakeRemote 一个最小的远端 MCP 服务端（http 传输；JSON 应答而非 SSE）。
type fakeRemote struct {
	srv *httptest.Server

	mu       sync.Mutex
	tools    []string
	calls    []string
	tokens   []string
	listFail bool // tools/list 回 JSON-RPC 错误（模拟"这台远端坏了"）
	callFail bool // tools/call 回 JSON-RPC 错误

	// 并发观测面（M7-5 的"同一会话串行、不同 server 并行"证据）：
	inflight, maxInflight int32
	entered               chan struct{} // 首次进入 tools/call 时关闭（nil = 不关）
	enterOnce             sync.Once
	waitFor               chan struct{} // 非 nil：进入 tools/call 后先等它有信号
	holdForOverlap        time.Duration // >0：等"第二个并发请求"出现的最长时间（自证串行）
}

func newFakeRemote(t *testing.T, tools ...string) *fakeRemote {
	t.Helper()
	f := &fakeRemote{tools: tools}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRemote) handle(w http.ResponseWriter, r *http.Request) {
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &msg)

	f.mu.Lock()
	f.tokens = append(f.tokens, r.Header.Get("Authorization"))
	f.mu.Unlock()

	reply := func(result any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "result": result})
	}
	replyErr := func(code int, text string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": json.RawMessage(msg.ID),
			"error": map[string]any{"code": code, "message": text},
		})
	}

	switch msg.Method {
	case "initialize":
		reply(map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "fake-remote", "version": "1.0"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		f.mu.Lock()
		fail := f.listFail
		tools := append([]string(nil), f.tools...)
		f.mu.Unlock()
		if fail {
			replyErr(-32603, "远端内部错误")
			return
		}
		items := make([]map[string]any, 0, len(tools))
		for _, name := range tools {
			items = append(items, map[string]any{"name": name, "description": "假的 " + name})
		}
		reply(map[string]any{"tools": items})
	case "tools/call":
		n := atomic.AddInt32(&f.inflight, 1)
		for { // 记录并发峰值（compare-and-swap 循环，不用额外锁）
			old := atomic.LoadInt32(&f.maxInflight)
			if n <= old || atomic.CompareAndSwapInt32(&f.maxInflight, old, n) {
				break
			}
		}
		if f.entered != nil {
			f.enterOnce.Do(func() { close(f.entered) })
		}
		if f.waitFor != nil {
			select {
			case <-f.waitFor:
			case <-time.After(5 * time.Second):
			}
		}
		if f.holdForOverlap > 0 {
			// 自证用：如果这条会话被**并行**使用，这期间必然看到第二个同时在处理的请求。
			deadline := time.Now().Add(f.holdForOverlap)
			for time.Now().Before(deadline) && atomic.LoadInt32(&f.inflight) < 2 {
				time.Sleep(5 * time.Millisecond)
			}
		}
		f.mu.Lock()
		f.calls = append(f.calls, msg.Params.Name)
		fail := f.callFail
		f.mu.Unlock()
		atomic.AddInt32(&f.inflight, -1)
		if fail {
			replyErr(-32603, "远端拒绝了这次调用")
			return
		}
		reply(map[string]any{"content": []map[string]any{{"type": "text", "text": "called " + msg.Params.Name}}})
	default:
		replyErr(-32601, "method not found")
	}
}

func (f *fakeRemote) snapshot() (calls, tokens []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), append([]string(nil), f.tokens...)
}

func (f *fakeRemote) setListFail(v bool) {
	f.mu.Lock()
	f.listFail = v
	f.mu.Unlock()
}

// maxConcurrent 峰值并发（同一 server 上同时在处理的 tools/call 数）。
func (f *fakeRemote) maxConcurrent() int { return int(atomic.LoadInt32(&f.maxInflight)) }

// setCallFail 让 tools/call 回 JSON-RPC 错误（M7-4 的调用留痕要能分辨 ok/error 两条分线）。
func (f *fakeRemote) setCallFail(v bool) {
	f.mu.Lock()
	f.callFail = v
	f.mu.Unlock()
}

// sourceRig 一套"真 store + 真数据面"的脚手架。
type sourceRig struct {
	t    *testing.T
	st   *store.Store
	cfg  config.Config
	logs func() string
}

func newSourceRig(t *testing.T) *sourceRig {
	t.Helper()
	dir := t.TempDir()
	st := testutil.OpenStore(t, filepath.Join(dir, "mate.db"))
	t.Cleanup(func() { _ = st.Close() })
	return &sourceRig{
		t:  t,
		st: st,
		cfg: config.Config{
			StoreMode: config.StoreModeSQLite, DBPath: filepath.Join(dir, "mate.db"),
			ProviderDir: filepath.Join(dir, "providers"), CapabilityDir: filepath.Join(dir, "capabilities"),
		},
		logs: testutil.CaptureLogs(t),
	}
}

// seedMcp 往库里写一条 MCP 定义（模拟"用户刚编辑了定义"）。
func (r *sourceRig) seedMcp(id, body string, expectedRev int64) {
	r.t.Helper()
	ok, err := r.st.UpsertMcpServerDefinition(context.Background(), id, body, expectedRev, "test")
	if err != nil || !ok {
		r.t.Fatalf("写 MCP 定义 %s: ok=%v err=%v", id, ok, err)
	}
}

// source 装配数据面（同一库句柄；日志由 testutil.CaptureLogs 收进临时文件供断言"降级有留痕"）。
//
// v19：网关的 scope 只有"哪个底座 + 会话钉住的 MCP 白名单"两项（agent 面已撤销）。
func (r *sourceRig) source(pinned *[]string) *MateSource {
	r.t.Helper()
	src, err := NewSource(context.Background(), SourceOptions{Adapters: []runtime.Adapter{dsh.New()},
		Cfg: r.cfg, McpsPinned: pinned, Store: r.st,
	})
	if err != nil {
		r.t.Fatalf("装配数据面: %v", err)
	}
	r.t.Cleanup(func() { _ = src.Close() })
	return src
}

// httpDef 一条指向假远端的 MCP 定义（密钥型 header 走 env: 引用，免去建 vault）。
func httpDef(id, url, tokenEnv string) string {
	return fmt.Sprintf(
		`{"id":%q,"transport":"http","url":%q,"secret_headers":{"Authorization":"env:%s"}}`,
		id, url, tokenEnv)
}

func toolNames(tools []mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

// ——— v20 裁决：凭据 cwd 由核心传入（`<cwd>/.env` 那一层） ———

// TestResolveCredCwd 优先级：**显式入参 > 环境变量 > 网关自己的 cwd**。
//
// 最后那一支不是"随便兜一个"：它是唯一会让两个进程对同一个引用给出不同答案的一支，所以它必须
// 可被识别（来源常量）——调用方据此打 WARN，而不是让它静默发生。
func TestResolveCredCwd(t *testing.T) {
	const envValue = "/from/core-env"

	// ① 显式入参最优先（常规生产路径留空，但调用方与测试可以显式给）。
	if cwd, src := resolveCredCwd("/from/option", func(string) string { return envValue }); cwd != "/from/option" || src != credCwdSourceOption {
		t.Fatalf("显式入参应最优先：cwd=%q src=%q", cwd, src)
	}
	// ② 环境变量（核心经网关条目的 env 交下来的那一条）。
	cwd, src := resolveCredCwd("", func(k string) string {
		if k == mcp.GatewayCredCwdEnv {
			return envValue
		}
		return ""
	})
	if cwd != envValue || src != credCwdSourceEnv {
		t.Fatalf("环境变量应生效：cwd=%q src=%q", cwd, src)
	}
	// ③ 谁都没给 → 网关自己的 cwd，且来源必须是可识别的"回落"值。
	own, err := os.Getwd()
	if err != nil {
		t.Skipf("取不到本进程 cwd，跳过回落分支: %v", err)
	}
	cwd, src = resolveCredCwd("", func(string) string { return "" })
	if cwd != own || src != credCwdSourceOwnGetwd {
		t.Fatalf("应回落到自己的 cwd：cwd=%q（want %q）src=%q", cwd, own, src)
	}
	// ④ 空白值不算"给了"：核心传一个空串/空格不能把回落变成静默。
	cwd, src = resolveCredCwd("   ", func(string) string { return "  " })
	if cwd != own || src != credCwdSourceOwnGetwd {
		t.Fatalf("空白值不得算作有效输入：cwd=%q src=%q", cwd, src)
	}
}

// TestSourceUsesCoreCredCwd 装配层证据：核心交下来的 cwd **真的**是凭据分层里那一层的 cwd。
//
// 判据落在行为上（不是"字段被赋值了"）：同一个 provider 引用，只有 cwd 里那份 `.env` 提供了值——
// 换成另一个目录装配就必须取不到。它正是"两个进程对同一个引用给同一个答案"的含义。
func TestSourceUsesCoreCredCwd(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	cwdWithEnv := filepath.Join(dir, "proj")
	cwdWithout := filepath.Join(dir, "elsewhere")
	for _, d := range []string{home, cwdWithEnv, cwdWithout} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st := testutil.OpenStore(t, filepath.Join(home, "mate.db"))
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	// 一条 provider 档案（值**只**放在 cwd 的 `.env` 里，库/托管/继承环境都没有）。
	const id = "cwd-cand"
	if ok, err := st.UpsertProviderDefinition(ctx, store.ProviderColumns{
		ID: id, Protocol: "openai", BaseURL: "https://cwd.invalid/v1",
	}, 0, "test"); err != nil || !ok {
		t.Fatalf("写 provider 行: ok=%v err=%v", ok, err)
	}
	// 平台注入/解析用的变量名逐供应商派生（`resource.RefFor`，与 manager 侧同一份算法）。
	reg, err := runtime.ProductRegistry([]runtime.Adapter{dsh.New()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := reg.Get(runtime.SelectedRuntime())
	if !ok || spec.Credential.RefPattern == "" {
		t.Skipf("构建期底座没有凭据引用模板，跳过: ok=%v", ok)
	}
	envName := resource.RefFor(spec.Credential.RefPattern, id)
	if envName == "" {
		t.Skip("派生不出环境变量名，跳过")
	}
	const secret = "sk-cwd-layer-fixture-0123"
	if err := os.WriteFile(filepath.Join(cwdWithEnv, ".env"), []byte(envName+"="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		StoreMode: config.StoreModeSQLite, DBPath: filepath.Join(home, "mate.db"),
		HomeDir: home, ProviderDir: filepath.Join(dir, "providers"),
		CapabilityDir: filepath.Join(dir, "capabilities"),
	}
	hasKey := func(credCwd string) bool {
		t.Helper()
		src, err := NewSource(ctx, SourceOptions{Adapters: []runtime.Adapter{dsh.New()}, Cfg: cfg, Store: st, CredCwd: credCwd})
		if err != nil {
			t.Fatalf("装配数据面: %v", err)
		}
		t.Cleanup(func() { _ = src.Close() })
		if src.credCwd != credCwd {
			t.Fatalf("凭据 cwd 未按显式入参生效：%q（want %q）", src.credCwd, credCwd)
		}
		items, err := src.planes.Providers.ListProviders(ctx)
		if err != nil {
			t.Fatalf("读 provider 面: %v", err)
		}
		for _, it := range items {
			if it.ID == id {
				return it.HasKey
			}
		}
		t.Fatalf("库里应有 %s：%+v", id, items)
		return false
	}

	if !hasKey(cwdWithEnv) {
		t.Fatal("核心交下来的 cwd 里有 .env ⇒ 这一层必须解析出密钥（has_key=true）")
	}
	if hasKey(cwdWithout) {
		t.Fatal("换一个没有 .env 的 cwd ⇒ 不得再解析出密钥（否则说明用的不是交下来的那个目录）")
	}
	// 密钥值**不得**出现在任何对外表示里（回归面）。
	src, err := NewSource(ctx, SourceOptions{Adapters: []runtime.Adapter{dsh.New()}, Cfg: cfg, Store: st, CredCwd: cwdWithEnv})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	items, err := src.planes.Providers.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(items)
	if strings.Contains(string(blob), secret) {
		t.Fatalf("密钥明文不得出现在读面：%s", blob)
	}
}

// TestSourceToolsNamespacesAndCalls 取数 + 命名空间化 + 代理调用（含密钥由网关就地解析）。
// TestSourceSharesDBWithCore 网关与核心各自持有**同一个库**的句柄时必须能共存：
// 生产形态就是这样（核心常驻持有 store，网关是它拉起的另一个进程/句柄），而两个 SQLite 连接
// 互相踩（锁等待、迁移竞争）会表现成"工具面时有时无"。本用例刻意**不注入** store 句柄，
// 让 `NewSource` 自己开一个——这正是 `mate mcp bridge` 在真实运行时的样子。
func TestSourceSharesDBWithCore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mate.db")
	core := testutil.OpenStore(t, dbPath) // 核心侧常驻句柄（不关，模拟 serve 存活期）
	t.Cleanup(func() { _ = core.Close() })
	cfg := config.Config{
		StoreMode: config.StoreModeSQLite, DBPath: dbPath,
		ProviderDir: filepath.Join(dir, "providers"), CapabilityDir: filepath.Join(dir, "capabilities"),
	}
	// 用核心句柄写入定义（真实写入路径），再让网关自己开库读。
	seedVia := func(id, body string) {
		t.Helper()
		if ok, err := core.UpsertMcpServerDefinition(context.Background(), id, body, 0, "test"); err != nil || !ok {
			t.Fatalf("写定义 %s: ok=%v err=%v", id, ok, err)
		}
	}
	remote := newFakeRemote(t, "echo")
	t.Setenv("MCP_SHARED_DB_TOKEN", "tok-shared") // 密钥能解析，才谈得上"看得到工具面"
	seedVia("web", httpDef("web", remote.srv.URL, "MCP_SHARED_DB_TOKEN"))

	logs := testutil.CaptureLogs(t)
	src, err := NewSource(context.Background(), SourceOptions{Adapters: []runtime.Adapter{dsh.New()},
		Cfg: cfg,
	})
	if err != nil {
		t.Fatalf("装配数据面（自开库）: %v", err)
	}
	defer func() { _ = src.Close() }()
	tools, err := src.Tools(context.Background())
	if err != nil {
		t.Fatalf("取工具面: %v", err)
	}
	if got := toolNames(tools); strings.Join(got, ",") != "web__echo" {
		t.Fatalf("两个句柄共用一个库时应看到同一份定义，got %v（日志：%s）", got, logs())
	}
}

func TestSourceToolsNamespacesAndCalls(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	remote := newFakeRemote(t, "search", "fetch")
	t.Setenv("MCP_TOKEN_GH", "tok-1")
	r.seedMcp("gh", httpDef("gh", remote.srv.URL, "MCP_TOKEN_GH"), 0)

	src := r.source(nil)
	tools, err := src.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	got := strings.Join(toolNames(tools), ",")
	if got != "gh__search,gh__fetch" {
		t.Fatalf("工具面应带来源前缀（否则多台 server 的工具无法分辨）: %v", got)
	}

	res, err := src.Call(ctx, "gh__search", json.RawMessage(`{"q":"x"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(res.TextOf(), "called search") {
		t.Fatalf("调用结果应透传（且工具名已去掉前缀）: %q", res.TextOf())
	}
	calls, tokens := remote.snapshot()
	if strings.Join(calls, ",") != "search" {
		t.Fatalf("远端应收到**原始**工具名: %v", calls)
	}
	if len(tokens) == 0 || tokens[len(tokens)-1] != "tok-1" {
		t.Fatalf("远端应看到网关就地解析出的明文密钥（env: 引用原样取值）: %v", tokens)
	}

	// 未知工具（含"已停用 server 的工具"的形态）→ 未知工具，不是内部错误。
	if _, err := src.Call(ctx, "gone__search", nil); err != mcp.ErrToolNotFound {
		t.Fatalf("未知工具应回 ErrToolNotFound: %v", err)
	}
	// 名字里没有前缀的调用同样落空（模型可能凭记忆调用旧名字）。
	if _, err := src.Call(ctx, "search", nil); err != mcp.ErrToolNotFound {
		t.Fatalf("无前缀名应落空: %v", err)
	}
}

// TestSourceTakesEffectWithoutGenerationSwitch 停用一台 server：**下一次取数**即消失，
// 且原先可调用的工具立刻变成未知工具（不必换代际、不必重启网关）。
func TestSourceTakesEffectWithoutGenerationSwitch(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	remote := newFakeRemote(t, "search")
	t.Setenv("MCP_TOKEN_GH", "tok-1")
	r.seedMcp("gh", httpDef("gh", remote.srv.URL, "MCP_TOKEN_GH"), 0)
	src := r.source(nil)

	if tools, err := src.Tools(ctx); err != nil || len(tools) != 1 {
		t.Fatalf("前置：应有 1 个工具: %v %v", toolNames(tools), err)
	}
	if _, err := src.Call(ctx, "gh__search", nil); err != nil {
		t.Fatalf("前置调用应成功: %v", err)
	}

	// 停用（enabled=false）：定义仍在，只是不注入。
	r.seedMcp("gh", `{"id":"gh","transport":"http","url":"`+remote.srv.URL+`","enabled":false}`, 1)
	tools, err := src.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("停用后不该再出现: %v", toolNames(tools))
	}
	if _, err := src.Call(ctx, "gh__search", nil); err != mcp.ErrToolNotFound {
		t.Fatalf("停用后调用应立即失败（收窄即时生效）: %v", err)
	}

	// 改 URL 换到另一台假远端：定义变化下一次取数即生效（连接池不得复用旧连接）。
	other := newFakeRemote(t, "other")
	r.seedMcp("gh", httpDef("gh", other.srv.URL, "MCP_TOKEN_GH"), 2)
	tools, err = src.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if strings.Join(toolNames(tools), ",") != "gh__other" {
		t.Fatalf("改 URL 后应看到新远端的工具: %v", toolNames(tools))
	}
}

// TestSourceSecretRotation 密钥轮换：底座无感（网关进程没重启、注入条目没变），
// 下一次调用就用新值——池按"定义快照"作废旧会话是这条的落点。
func TestSourceSecretRotation(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	remote := newFakeRemote(t, "search")
	t.Setenv("MCP_TOKEN_PG", "old-token")
	r.seedMcp("pg", httpDef("pg", remote.srv.URL, "MCP_TOKEN_PG"), 0)
	src := r.source(nil)

	if _, err := src.Call(ctx, "pg__search", nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_TOKEN_PG", "new-token")
	if _, err := src.Call(ctx, "pg__search", nil); err != nil {
		t.Fatal(err)
	}
	_, tokens := remote.snapshot()
	if len(tokens) < 2 || tokens[len(tokens)-1] != "new-token" {
		t.Fatalf("轮换后应立即用新密钥（不得复用旧连接）: %v", tokens)
	}
}

// TestSourceUnresolvableSecretDegrades 一台引用解析不了 → **只跳过它**（WARN 留痕），其余照常。
func TestSourceUnresolvableSecretDegrades(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	good := newFakeRemote(t, "ok")
	t.Setenv("MCP_TOKEN_GH", "tok-1")
	r.seedMcp("gh", httpDef("gh", good.srv.URL, "MCP_TOKEN_GH"), 0)
	// 引用一个不存在的环境变量：解析必失败。
	r.seedMcp("bad", httpDef("bad", good.srv.URL, "MCP_TOKEN_ABSENT"), 0)
	src := r.source(nil)

	tools, err := src.Tools(ctx)
	if err != nil {
		t.Fatalf("一台坏 server 不该让整次取数失败: %v", err)
	}
	if strings.Join(toolNames(tools), ",") != "gh__ok" {
		t.Fatalf("坏 server 应被跳过、好的照常: %v", toolNames(tools))
	}
	if !strings.Contains(r.logs(), "密钥引用解析失败") {
		t.Fatalf("降级必须留痕（否则表现为工具莫名消失）: %s", r.logs())
	}
	if strings.Contains(r.logs(), "tok-1") {
		t.Fatalf("日志绝不能出现密钥值: %s", r.logs())
	}
}

// TestSourceRemoteFailureIsContained 一台远端坏掉（tools/list 报错）→ 它这一轮没有工具，
// 其余照常，错误浮到日志，网关本身不受影响。
func TestSourceRemoteFailureIsContained(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	broken := newFakeRemote(t, "x")
	broken.setListFail(true)
	ok := newFakeRemote(t, "fine")
	t.Setenv("MCP_TOKEN_GH", "tok-1")
	r.seedMcp("broken", httpDef("broken", broken.srv.URL, "MCP_TOKEN_GH"), 0)
	r.seedMcp("ok", httpDef("ok", ok.srv.URL, "MCP_TOKEN_GH"), 0)
	src := r.source(nil)

	tools, err := src.Tools(ctx)
	if err != nil {
		t.Fatalf("单台远端故障不该让工具面整体失败: %v", err)
	}
	if strings.Join(toolNames(tools), ",") != "ok__fine" {
		t.Fatalf("坏远端不该贡献工具: %v", toolNames(tools))
	}
	if !strings.Contains(r.logs(), "列举远端工具失败") {
		t.Fatalf("远端故障必须留痕: %s", r.logs())
	}
}

// TestSourcePinnedWhitelist 会话钉住的白名单决定网关给出哪些远程 server（含显式空集 = 一个都不要）。
//
// v19：没有"agent 声明"这一层了——未钉（`McpsPinned == nil`）= 全量，钉过 `["gh"]` 只看到 gh，
// 钉过 `[]` 一个都不要。
func TestSourcePinnedWhitelist(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	gh := newFakeRemote(t, "search")
	pg := newFakeRemote(t, "query")
	t.Setenv("MCP_TOKEN_GH", "tok-1")
	r.seedMcp("gh", httpDef("gh", gh.srv.URL, "MCP_TOKEN_GH"), 0)
	r.seedMcp("pg", httpDef("pg", pg.srv.URL, "MCP_TOKEN_GH"), 0)

	// 缺省（会话没钉）→ 全量远程面。
	all, err := r.source(nil).Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(toolNames(all), ",") != "gh__search,pg__query" {
		t.Fatalf("未钉住时应给全量远程面: %v", toolNames(all))
	}

	pinned := []string{"gh"}
	only, err := r.source(&pinned).Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(toolNames(only), ",") != "gh__search" {
		t.Fatalf("钉住的白名单应生效: %v", toolNames(only))
	}

	empty := []string{}
	none, err := r.source(&empty).Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("显式空集 = 本会话一个远程 MCP 都不要: %v", toolNames(none))
	}
}

// TestSourceServerTimeoutIsBounded 一台远端不回话 → 该 server 超时跳过，整次取数仍有界。
func TestSourceServerTimeoutIsBounded(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	// 一个"接了但不回"的远端。**刻意不靠 req.Context() 收尾**：服务端要等客户端断开才能
	// 取消它，而 httptest 的 Close 又会等 handler 返回——两边互等就是死锁（本用例第一版
	// 正是这么挂满 5 分钟的）。改用一个显式 release 通道，且**注册顺序**保证先放行再关服务。
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		<-release
	}))
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) }) // LIFO：先于 hang.Close 跑
	ok := newFakeRemote(t, "fine")
	t.Setenv("MCP_TOKEN_GH", "tok-1")
	r.seedMcp("hang", httpDef("hang", hang.URL, "MCP_TOKEN_GH"), 0)
	r.seedMcp("ok", httpDef("ok", ok.srv.URL, "MCP_TOKEN_GH"), 0)

	src, err := NewSource(ctx, SourceOptions{Adapters: []runtime.Adapter{dsh.New()},
		Cfg: r.cfg, Store: r.st, PerServerTimeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	start := time.Now()
	tools, err := src.Tools(ctx)
	if err != nil {
		t.Fatalf("单台挂住不该让取数失败: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("取数应有界（单台超时 %v），实际 %v", 300*time.Millisecond, elapsed)
	}
	if strings.Join(toolNames(tools), ",") != "ok__fine" {
		t.Fatalf("挂住的远端不该贡献工具: %v", toolNames(tools))
	}
}
