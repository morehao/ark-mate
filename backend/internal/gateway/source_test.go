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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/ark-forge/internal/testutil"
	"github.com/morehao/ark-forge/pkg/config"
	"github.com/morehao/ark-forge/pkg/mcp"
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
		f.mu.Lock()
		f.calls = append(f.calls, msg.Params.Name)
		fail := f.callFail
		f.mu.Unlock()
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

// setCallFail 让 tools/call 回 JSON-RPC 错误（M7-4 的调用留痕要能分辨 ok/error 两条分线）。
func (f *fakeRemote) setCallFail(v bool) {
	f.mu.Lock()
	f.callFail = v
	f.mu.Unlock()
}

// sourceRig 一套"真 store + 真数据面"的脚手架。
type sourceRig struct {
	t   *testing.T
	st  *store.Store
	cfg config.Config
	log *strings.Builder
}

func newSourceRig(t *testing.T) *sourceRig {
	t.Helper()
	dir := t.TempDir()
	st := testutil.OpenStore(t, filepath.Join(dir, "forge.db"))
	t.Cleanup(func() { _ = st.Close() })
	return &sourceRig{
		t:  t,
		st: st,
		cfg: config.Config{
			StoreMode: config.StoreModeSQLite, DBPath: filepath.Join(dir, "forge.db"),
			AgentDir: filepath.Join(dir, "agents"), CapabilityDir: filepath.Join(dir, "capabilities"),
		},
		log: &strings.Builder{},
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

// seedAgent 往库里写一条 agent 定义。
func (r *sourceRig) seedAgent(id, body string) {
	r.t.Helper()
	ok, err := r.st.UpsertAgentDefinition(context.Background(), id, body, 0, "test")
	if err != nil || !ok {
		r.t.Fatalf("写 agent 定义 %s: ok=%v err=%v", id, ok, err)
	}
}

// source 装配数据面（同一库句柄；Logger 收进 buffer 供断言"降级有留痕"）。
func (r *sourceRig) source(agentID string, pinned *[]string) *ForgeSource {
	r.t.Helper()
	src, err := NewSource(context.Background(), SourceOptions{
		Cfg: r.cfg, AgentID: agentID, McpsPinned: pinned, Store: r.st,
		Logger: slog.New(slog.NewTextHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
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

// TestSourceToolsNamespacesAndCalls 取数 + 命名空间化 + 代理调用（含密钥由网关就地解析）。
// TestSourceSharesDBWithCore 网关与核心各自持有**同一个库**的句柄时必须能共存：
// 生产形态就是这样（核心常驻持有 store，网关是它拉起的另一个进程/句柄），而两个 SQLite 连接
// 互相踩（锁等待、迁移竞争）会表现成"工具面时有时无"。本用例刻意**不注入** store 句柄，
// 让 `NewSource` 自己开一个——这正是 `forge mcp bridge` 在真实运行时的样子。
func TestSourceSharesDBWithCore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "forge.db")
	core := testutil.OpenStore(t, dbPath) // 核心侧常驻句柄（不关，模拟 serve 存活期）
	t.Cleanup(func() { _ = core.Close() })
	cfg := config.Config{
		StoreMode: config.StoreModeSQLite, DBPath: dbPath,
		AgentDir: filepath.Join(dir, "agents"), CapabilityDir: filepath.Join(dir, "capabilities"),
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
	if ok, err := core.UpsertAgentDefinition(context.Background(), "a1",
		`{"id":"a1","name":"A1","runtimes":["opencode"]}`, 0, "test"); err != nil || !ok {
		t.Fatalf("写 agent: ok=%v err=%v", ok, err)
	}

	var buf strings.Builder
	src, err := NewSource(context.Background(), SourceOptions{
		Cfg: cfg, AgentID: "a1",
		Logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
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
		t.Fatalf("两个句柄共用一个库时应看到同一份定义，got %v（日志：%s）", got, buf.String())
	}
}

func TestSourceToolsNamespacesAndCalls(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	remote := newFakeRemote(t, "search", "fetch")
	t.Setenv("MCP_TOKEN_GH", "tok-1")
	r.seedMcp("gh", httpDef("gh", remote.srv.URL, "MCP_TOKEN_GH"), 0)
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"]}`)

	src := r.source("a1", nil)
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
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"]}`)
	src := r.source("a1", nil)

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
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"]}`)
	src := r.source("a1", nil)

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
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"]}`)
	src := r.source("a1", nil)

	tools, err := src.Tools(ctx)
	if err != nil {
		t.Fatalf("一台坏 server 不该让整次取数失败: %v", err)
	}
	if strings.Join(toolNames(tools), ",") != "gh__ok" {
		t.Fatalf("坏 server 应被跳过、好的照常: %v", toolNames(tools))
	}
	if !strings.Contains(r.log.String(), "密钥引用解析失败") {
		t.Fatalf("降级必须留痕（否则表现为工具莫名消失）: %s", r.log.String())
	}
	if strings.Contains(r.log.String(), "tok-1") {
		t.Fatalf("日志绝不能出现密钥值: %s", r.log.String())
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
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"]}`)
	src := r.source("a1", nil)

	tools, err := src.Tools(ctx)
	if err != nil {
		t.Fatalf("单台远端故障不该让工具面整体失败: %v", err)
	}
	if strings.Join(toolNames(tools), ",") != "ok__fine" {
		t.Fatalf("坏远端不该贡献工具: %v", toolNames(tools))
	}
	if !strings.Contains(r.log.String(), "列举远端工具失败") {
		t.Fatalf("远端故障必须留痕: %s", r.log.String())
	}
}

// TestSourcePinnedWhitelist 会话钉住的白名单**覆盖** agent 声明（含显式空集 = 一个都不要）。
//
// 这是"网关不能只认 agent"的证据：不带钉住项时网关会把 agent 的全量远程面给出去，
// 而钉过 `["gh"]` 的会话必须只看到 gh。
func TestSourcePinnedWhitelist(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	gh := newFakeRemote(t, "search")
	pg := newFakeRemote(t, "query")
	t.Setenv("MCP_TOKEN_GH", "tok-1")
	r.seedMcp("gh", httpDef("gh", gh.srv.URL, "MCP_TOKEN_GH"), 0)
	r.seedMcp("pg", httpDef("pg", pg.srv.URL, "MCP_TOKEN_GH"), 0)
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"]}`)

	// 缺省（会话没钉）：agent 未声明白名单 → 全量远程面。
	all, err := r.source("a1", nil).Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(toolNames(all), ",") != "gh__search,pg__query" {
		t.Fatalf("未钉应回落 agent 声明（此处为全量）: %v", toolNames(all))
	}

	pinned := []string{"gh"}
	only, err := r.source("a1", &pinned).Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(toolNames(only), ",") != "gh__search" {
		t.Fatalf("钉住的白名单应覆盖 agent 声明: %v", toolNames(only))
	}

	empty := []string{}
	none, err := r.source("a1", &empty).Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("显式空集 = 本会话一个远程 MCP 都不要: %v", toolNames(none))
	}
}

// TestSourceUnknownAgentFailsClosed agent 查不到（条目与库不一致）→ **错误**，不回落成全量。
func TestSourceUnknownAgentFailsClosed(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	remote := newFakeRemote(t, "search")
	t.Setenv("MCP_TOKEN_GH", "tok-1")
	r.seedMcp("gh", httpDef("gh", remote.srv.URL, "MCP_TOKEN_GH"), 0)
	// 有活着的 agent，但网关的 scope 指向一个不存在的：不能"查不到就给全部"。
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"]}`)
	src := r.source("ghost", nil)

	if _, err := src.Tools(ctx); err == nil {
		t.Fatal("未知 agent 必须失败（授权判据不能反着用）")
	} else if !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("错误应说清楚原因: %v", err)
	}
	if _, err := src.Call(ctx, "gh__search", nil); err == nil {
		t.Fatal("未知 agent 的调用同样必须失败")
	}
}

// TestSourceAgentWhitelistApplies agent 声明的白名单在网关侧同样生效（与注入面同一语义）。
func TestSourceAgentWhitelistApplies(t *testing.T) {
	ctx := context.Background()
	r := newSourceRig(t)
	gh := newFakeRemote(t, "search")
	pg := newFakeRemote(t, "query")
	t.Setenv("MCP_TOKEN_GH", "tok-1")
	r.seedMcp("gh", httpDef("gh", gh.srv.URL, "MCP_TOKEN_GH"), 0)
	r.seedMcp("pg", httpDef("pg", pg.srv.URL, "MCP_TOKEN_GH"), 0)
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"],"mcps":["pg"]}`)
	src := r.source("a1", nil)

	tools, err := src.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(toolNames(tools), ",") != "pg__query" {
		t.Fatalf("agent 白名单应生效: %v", toolNames(tools))
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
	r.seedAgent("a1", `{"id":"a1","name":"A1","runtimes":["opencode"]}`)

	src, err := NewSource(ctx, SourceOptions{
		Cfg: r.cfg, AgentID: "a1", Store: r.st, PerServerTimeout: 300 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(r.log, nil)),
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
