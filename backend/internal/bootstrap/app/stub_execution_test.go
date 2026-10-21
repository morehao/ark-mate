package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/morehao/ark-mate/internal/bootstrap/assembly"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/delivery/middleware"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— C2 §3.4：非 ACP 执行面桩（"换底座不动框架"的机器证明）———
//
// 判据：把 `session.ConnPool`（"会话从哪条连接来"）换成一个**纯内存、不认识协议**的实现，
// 控制面契约基线与会话事件序列都必须逐字不变。
//
// 为什么这条不可省：C2 把协议从内圈清干净之后，**没有测试会自己证明"内圈真的不依赖 ACP"**——
// `grep` 只能证明 import 表干净，证明不了"换掉实现还能跑"。这里用一个连 `acp` 都不 import 的
// 实现跑完整装配（路由、控制器、用例、领域、事件流），跑不动就说明某条边界又把协议漏回内圈了。
//
// 桩只活在测试里：装配缝是 `assembly.WithConnPool`（一个端口注入点，不是行为开关），
// 生产路径永远不传它。

// memExecution `session.ConnPool` 的纯内存实现：不拨号、不起进程、不 import acp。
type memExecution struct {
	mu     sync.Mutex
	conn   *memConn
	dialed int
	prompts []string
}

var _ session.ConnPool = (*memExecution)(nil)

func (m *memExecution) Dial(_ context.Context, spec runtime.Spec, _ session.DialOptions) (session.Conn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dialed++
	if m.conn == nil || m.conn.spec.ID != spec.ID {
		m.conn = &memConn{spec: spec, agent: &memAgent{pool: m}}
	}
	return m.conn, nil
}

func (m *memExecution) KeyFor(spec runtime.Spec, fingerprint, nativeMode string) session.KeyInputs {
	base := spec.ID + "|" + nativeMode
	return session.KeyInputs{BaseKey: base, ConfigDigest: "mem", Key: base + "|" + fingerprint}
}

func (m *memExecution) ConnForRuntime(runtimeID string) session.Conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return nil
	}
	if runtimeID != "" && m.conn.spec.ID != runtimeID {
		return nil
	}
	return m.conn
}

func (m *memExecution) ReclaimGeneration(session.Conn) bool { return false }
func (m *memExecution) CloseAll()                           {}
func (m *memExecution) SetDropSink(session.DropSink)        {}
func (m *memExecution) SetGenerationLimit(int)              {}
func (m *memExecution) StartIdleReaper(time.Duration, time.Duration) {}

// DialCount 被拨号过几次（断言"真的走了桩"）。
func (m *memExecution) DialCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dialed
}

// Prompts 桩会话收到过的提示词（断言"这一轮真的由桩执行"）。
func (m *memExecution) Prompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

// memConn `session.Conn`：只交事实（键、指纹、能力面）。
type memConn struct {
	spec  runtime.Spec
	agent *memAgent
}

var _ session.Conn = (*memConn)(nil)

func (c *memConn) Agent() session.AgentConn { return c.agent }
func (c *memConn) Spec() runtime.Spec       { return c.spec }
func (c *memConn) Key() string              { return c.spec.ID + "||" }
func (c *memConn) BaseKey() string          { return c.spec.ID }
func (c *memConn) ConfigDigest() string     { return "mem" }

// memAgent `session.AgentConn`：会话都在内存里，能力事实写成"什么都不会"的保守基线。
type memAgent struct{ pool *memExecution }

var _ session.AgentConn = (*memAgent)(nil)

func (a *memAgent) Capabilities() runtime.CapabilityFacts { return runtime.CapabilityFacts{} }

func (a *memAgent) NewSession(_ context.Context, opt session.SessionOptions) (session.AgentSession, error) {
	return &memSession{id: "native-1", cwd: opt.Cwd, agent: a}, nil
}

func (a *memAgent) LoadSession(context.Context, string, session.SessionOptions) (session.AgentSession, error) {
	return nil, memUnsupported()
}

func (a *memAgent) ResumeSession(context.Context, string, session.SessionOptions) (session.AgentSession, error) {
	return nil, memUnsupported()
}

func (a *memAgent) ListSessions(context.Context, string) ([]runtime.SessionRef, error) { return nil, nil }

func (a *memAgent) StderrTail() []string { return nil }

// memSession `session.AgentSession`：Prompt 回一句话并记账。
type memSession struct {
	id    string
	cwd   string
	agent *memAgent
}

var _ session.AgentSession = (*memSession)(nil)

func (s *memSession) Prompt(_ context.Context, text string) (string, error) {
	s.agent.pool.mu.Lock()
	s.agent.pool.prompts = append(s.agent.pool.prompts, text)
	s.agent.pool.mu.Unlock()
	return "桩回复：" + text, nil
}

func (s *memSession) Cancel(context.Context) error          { return nil }
func (s *memSession) ConfigOptions() []runtime.ConfigOption { return nil }
func (s *memSession) ID() string                            { return s.id }
func (s *memSession) Capabilities() runtime.CapabilityFacts { return runtime.CapabilityFacts{} }
func (s *memSession) Close(context.Context) error           { return nil }

func (s *memSession) SetConfigOption(context.Context, string, string) ([]runtime.ConfigOption, error) {
	return nil, memUnsupported()
}

// memUnsupported 桩对"没有的能力"的答复——用**平台错误**（分类 unsupported → 40501），
// 与真实适配器在同一处折出来的形状一致。它刻意不是裸 `errors.New`：那会让能力判定退化成
// "未知错误"（50000），把"这个底座不支持"与"平台炸了"混成一档。
func memUnsupported() error {
	return code.UnsupportedError("stub_capability_absent", "内存桩不提供这条能力")
}

// ——— 装配：与 `newIsolationRig` 同一份配置，唯一差别是连接池换成内存桩 ———

// newStubAssembled 装配一台**不拨 ACP** 的后端并起 HTTP，返回 handler 与监听地址。
func newStubAssembled(t *testing.T, pool session.ConnPool) (http.Handler, string) {
	t.Helper()
	cfg, _ := isolationConfig(t)
	res, err := assembly.Build(context.Background(), cfg, nil, assembly.WithConnPool(pool))
	if err != nil {
		t.Fatalf("装配（桩执行面）: %v", err)
	}
	srv := &http.Server{Handler: res.Handler.Handler}
	go func() { _ = srv.Serve(res.Handler.Listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		res.Close()
	})
	return res.Handler.Handler, res.Handler.Listener.Addr().String()
}

// TestStubExecutionRunsControlPlaneContractBaseline 换掉底座实现之后，控制面契约逐字不变。
//
// 这是 §3.4 的第一半：同一个 `assertContractBaseline`（与 `TestControlPlaneContractBaseline`
// 共用的那份采样表与判据）跑在为内存桩装配的后端上——HTTP 状态、业务码、msg、信封键、
// data 形状全部必须一致。任何"内圈偷偷依赖 ACP"的改动都会在这里以"桩跑不起来 / 形状变了"暴露。
func TestStubExecutionRunsControlPlaneContractBaseline(t *testing.T) {
	eng, host := newStubAssembled(t, &memExecution{})
	assertContractBaseline(t, eng, host)
}

// TestStubExecutionSessionEventSnapshot 建会话 + 发一轮，**整条事件出口**由内存桩驱动。
//
// 第二半判据（快照）：事件 type 与顺序是平台契约（前端/WS 消费者按它渲染），它必须由**框架**
// 产生，而不是由某个底座实现"顺便"产生。桩只做两件事——建一条原生会话、把 prompt 回成一句话；
// 其余每一个事件都来自领域与用例。
func TestStubExecutionSessionEventSnapshot(t *testing.T) {
	pool := &memExecution{}
	eng, host := newStubAssembled(t, pool)

	cwd := t.TempDir()
	created := postJSON(t, eng, host, "/v1/mate/sessions", `{"cwd":`+quoteJSON(cwd)+`}`)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("建会话失败：%+v", created)
	}

	// 先建会话再 attach：WS 的首帧 attach 是按**会话**订阅的（契约见 docs/architecture.md §6.2），
	// 且 `since_seq = -1` 表示"从该会话第一条起全量重放"——`session.created` 因此也在快照里。
	frames := dialEventStream(t, host)
	defer frames.close()
	frames.attach(t, id)
	// 等重放收尾再发 prompt：attach 的重放（`EventsAfter`）与实时流是**两条**路径，
	// 不划分界就会拿到"实时事件插进重放中间"的乱序（一次真实 race，不是桩的问题）。
	replayed := frames.awaitReplayEnd(t, 5*time.Second)
	if strings.Join(replayed, ",") != "session.created" {
		t.Fatalf("attach 应重放出建会话那一条，got %v", replayed)
	}
	postJSON(t, eng, host, "/v1/mate/sessions/"+id+"/prompt", `{"prompt":"你好"}`)

	// 桩真的被执行过（否则"事件序列"可能来自根本没跑的那条路）。
	waitFor(t, func() bool { return len(pool.Prompts()) > 0 }, "桩会话收到 prompt")
	// 只断言**结尾**是用户原文：平台会按产品身份配置在提示词前面加一段 `<product_identity>`
	// 块（那是平台自己的事实，与底座实现无关）。整段相等会把那段内容一起钉进这里。
	if got := pool.Prompts()[0]; !strings.HasSuffix(got, "你好") {
		t.Fatalf("桩会话应收到以用户原文结尾的提示词，got %q", got)
	}
	if pool.DialCount() == 0 {
		t.Fatal("连接池桩没被拨号：会话没有经过它")
	}

	// 快照：到 `run.status(idle)` 为止的会话事件 type 序列。
	types := frames.sessionEventTypes(t, 8*time.Second)
	// 实时段这一串全是**框架自己**产生的事件（一条都不能少、顺序不能变）。
	// `session.created` 不在这里：它在 attach 的重放段（上面已单独断言）。
	//
	//	run.started           开轮三连之一（域）
	//	run.status(preparing) 过程状态第一帧（域）
	//	session.user_message  用户原文（域；前缀不进事件）
	//	run.status(model…)    已向模型发出请求（域）
	//	run.status(idle)      收口（域）
	//	run.ended             本轮成功终态（域）
	//
	// **刻意没有** `session.message` / `session.thought` / `session.tool_call`：它们是适配器
	// 事件流（增量）的产物，而桩只回一句话、不发增量——那两个事件的判据在 C2 第一刀
	// （`sessionruntime/eventsink_signal_test.go` 的域信号桩）与 ACP 桩用例里。
	want := []string{
		"run.started",
		"run.status",
		"session.user_message",
		"run.status",
		"run.status",
		"run.ended",
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("会话事件序列（桩底座）与契约不符：\n got %v\nwant %v", types, want)
	}
	// 会话在平台上确实是 completed（事件只是过程；状态是结论）。
	detail := getJSON(t, eng, host, "/v1/mate/sessions/"+id)
	if got := detail["status"]; got != "completed" {
		t.Fatalf("一轮成功后会话应为 completed，got %v", got)
	}
}

// eventStream 一条 WS 事件流的测试读端。
type eventStream struct {
	conn *websocket.Conn
}

func dialEventStream(t *testing.T, host string) *eventStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+host+"/v1/mate/ws/events", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{middleware.DesktopAppOrigin}},
	})
	if err != nil {
		t.Fatalf("拨 WS 事件流: %v", err)
	}
	return &eventStream{conn: conn}
}

func (e *eventStream) close() { _ = e.conn.CloseNow() }

// attach 发首帧 attach（`since_seq: -1` = 从该会话第一条起全量重放）。
func (e *eventStream) attach(t *testing.T, sessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	frame := `{"action":"attach","agg_id":` + quoteJSON(sessionID) + `,"since_seq":-1}`
	if err := e.conn.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Fatalf("发 attach 帧: %v", err)
	}
}

// awaitReplayEnd 读到 `replay_end` 为止，返回重放段里的会话事件 type 序列。
func (e *eventStream) awaitReplayEnd(t *testing.T, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var types []string
	for {
		typ, sessionType, _ := e.readFrame(t, deadline)
		if typ == "replay_end" {
			return types
		}
		if sessionType != "" {
			types = append(types, sessionType)
		}
	}
}

// readFrame 读一帧：返回帧 type（snapshot/event/gap/replay_begin/…）与会话事件 type（若有）。
func (e *eventStream) readFrame(t *testing.T, deadline time.Time) (frameType, sessionType string, event bool) {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	_, data, err := e.conn.Read(ctx)
	if err != nil {
		t.Fatalf("读事件流失败: %v", err)
	}
	var frame struct {
		Type  string `json:"type"`
		Event *struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		} `json:"event"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("解析帧: %v (%s)", err, data)
	}
	if frame.Event == nil {
		return frame.Type, "", false
	}
	return frame.Type, frame.Event.Type, true
}

// sessionEventTypes 读到终端事件（`run.ended` / `run.error` / `run.cancelled`）为止的会话事件 type 序列。
//
// 终止条件是**平台自己的终态事件**，不是固定条数或超时：条数写死时，多一个/少一个事件会以
// "读超时"的形式出现，而真正的原因（顺序变了）被埋没。
func (e *eventStream) sessionEventTypes(t *testing.T, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var types []string
	for {
		_, typ, _ := e.readFrame(t, deadline)
		if typ == "" {
			continue // snapshot / replay_begin 之类的控制帧：它们不是会话事件
		}
		if !strings.HasPrefix(typ, "session.") && !strings.HasPrefix(typ, "run.") {
			continue
		}
		types = append(types, typ)
		if typ == "run.ended" || typ == "run.error" || typ == "run.cancelled" {
			return types
		}
	}
}

// getJSON 打一条 GET 并解出信封的 `data`（失败即 Fatal）。
func getJSON(t *testing.T, eng http.Handler, host, path string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	var env struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s 响应不是信封 JSON: %s (%v)", path, w.Body.String(), err)
	}
	if env.Code != 0 {
		t.Fatalf("%s 失败: code=%d msg=%q", path, env.Code, env.Msg)
	}
	return env.Data
}

// postJSON 打一条 JSON 请求并解出信封的 `data`（失败即 Fatal，附上整条信封）。
func postJSON(t *testing.T, eng http.Handler, host, path, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	var env struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s 响应不是信封 JSON: %s (%v)", path, w.Body.String(), err)
	}
	if env.Code != 0 {
		t.Fatalf("%s 失败: code=%d msg=%q body=%s", path, env.Code, env.Msg, w.Body.String())
	}
	return env.Data
}

// waitFor 轮询直到 cond 成立（测试里唯一的"等后台动作"手法；超时即失败）。
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// quoteJSON 把字符串编成 JSON 字面量（请求体是手写串，路径里带引号/反斜杠时必须转义）。
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
