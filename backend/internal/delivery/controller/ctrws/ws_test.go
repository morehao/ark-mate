package ctrws_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/platform/credential"
	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/delivery/controller/ctrws"
	"github.com/morehao/golib/biz/gcontext"
	"github.com/morehao/golib/biz/gmiddleware/ginmiddleware"
)

// testEventID 测试用的事件行身份（bus 不校验形状；真实形状由 objid 铸）。
const testEventID = "evt_01J9Z6Q0M7X2V8K4B3N5TRW1PD"

// frame 是测试用 WS 帧（松散解析关键字段）。
type frame struct {
	Type  string `json:"type"`
	AggID string `json:"agg_id"`
	Seq   int64  `json:"seq"`
	// EventSeq 服务端帧的 `event_seq`（snapshot 的会话水位 / gap 的首丢位次）。
	EventSeq int64           `json:"event_seq"`
	State    json.RawMessage `json:"state,omitempty"`
	Event    *struct {
		Type    string         `json:"type"`
		Seq     int64          `json:"seq"`
		AggID   string         `json:"agg_id"`
		Payload map[string]any `json:"payload"`
	} `json:"event,omitempty"`
}

// startWS 起测试服务并拨号（coder/websocket），返回连接与上下文辅助。
func startWS(t *testing.T, bus *eventbus.Bus) *websocket.Conn {
	t.Helper()
	return dialWS(t, newWSEngine(bus))
}

// wsSetup 装配参数（M1-1）：凭据表与首帧 attach 截止。deadline 0 = 用默认 5s；
// 测超时的用例改小以免拉长整包时间。
type wsSetup struct {
	creds    *credential.Set
	deadline time.Duration
}

// newWSEngine 装配一个只挂 WS 处理器的引擎（无凭据、默认截止）；mws 可选，但必须在这里传入——
// gin 在注册路由时就冻结 handler 链，注册之后再 Use 不会作用于已注册路由。
func newWSEngine(bus *eventbus.Bus, mws ...gin.HandlerFunc) *gin.Engine {
	return newWSEngineWith(bus, wsSetup{}, mws...)
}

// newWSEngineWith 同 newWSEngine，但可指定凭据与截止（M1-1 的握手用例需要）。
func newWSEngineWith(bus *eventbus.Bus, s wsSetup, mws ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	eng.Use(mws...)
	h := ctrws.NewWSCtr(bus, ctrws.Options{Credentials: s.creds, AttachDeadline: s.deadline})
	eng.GET("/ws/events", h.Handle)
	return eng
}

// dialWS 起 httptest 服务并完成一次 WS 握手，失败即 Fatal。
func dialWS(t *testing.T, eng *gin.Engine) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(eng)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/events"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// wsWrite 以文本帧发送 JSON。
func wsWrite(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("ws write: %v", err)
	}
}

// wsReadRaw 读下一文本帧，返回原始 JSON。
func wsReadRaw(t *testing.T, c *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("期望 text 帧，got type %v", typ)
	}
	return data
}

// wsRead 读下一文本帧并解 JSON，**跳过回放区间帧**（`replay_begin` / `replay_end`）。
//
// 为什么默认跳过（ADR-13）：这两帧描述的是"这次连接正在回放"，与"这个会话产生了哪些业务帧"
// 无关；把它们算进序列断言，会让每一条订阅用例都要多读两帧。**顺序**由
// `TestReplayFramesBracketReplay` 专门钉住（那一支用 `wsReadRaw` 逐帧核对）。
func wsRead(t *testing.T, c *websocket.Conn, dst any) {
	t.Helper()
	for {
		data := wsReadRaw(t, c)
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			t.Fatalf("unmarshal(probe): %v", err)
		}
		if probe.Type == "replay_begin" || probe.Type == "replay_end" {
			continue
		}
		if err := json.Unmarshal(data, dst); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return
	}
}

// TestWSHandlerAttachReplayLivePong 覆盖 §4.5 主路径：
// attach（无 since → 全量回放既有缓冲）→ bus 发布 → event 帧 → ping → pong → detach。
//
// v21 之前"不带 since"是 **snapshot**、带 `since_seq=0` 才是全量回放；位次从 0 起之后
// `0` 是真实位次，于是"没有游标"（省略 / 负数）承担了全量回放，snapshot 只剩淘汰回退一条路
// （见 TestWSHandlerSnapshotFallbackOnEviction）。
func TestWSHandlerAttachReplayLivePong(t *testing.T) {
	bus := eventbus.New()
	// 挂载前已存在的事件（真实路径里是重启后从 DB 注入 bus 的历史）。
	pre := bus.Publish(testEventID, "session.created", "s1", 1, map[string]any{"text": "pre"})
	conn := startWS(t, bus)

	// attach 无 since → 全量回放（含位次 0 的那一条）
	wsWrite(t, conn, map[string]any{"action": "attach", "agg_id": "s1"})
	var replay frame
	wsRead(t, conn, &replay)
	if replay.Type != "event" || replay.AggID != "s1" || replay.Event == nil || replay.Event.Seq != pre.Seq {
		t.Fatalf("无 since 应全量回放既有事件（含位次 0）: %+v", replay)
	}

	// bus 发布 → event 帧（按聚合过滤）
	pub := bus.Publish(testEventID, "session.message", "s1", 1, map[string]any{"text": "hi"})
	var evt frame
	wsRead(t, conn, &evt)
	if evt.Type != "event" || evt.AggID != "s1" || evt.Event == nil ||
		evt.Event.Type != "session.message" || evt.Event.Seq != pub.Seq {
		t.Fatalf("event 帧不符: %+v", evt)
	}

	// 未 attach 的聚合不推送（此处不发帧验证：发布给 s2 不会产生本连接可见帧，
	// 直接进入 ping 流程，避免盲等；过滤逻辑由 replay 测试的仅 s1 回放间接覆盖）
	bus.Publish(testEventID, "session.thought", "s2", 1, map[string]any{"text": "other"})

	// ping → pong
	wsWrite(t, conn, map[string]any{"action": "ping"})
	var pong frame
	wsRead(t, conn, &pong)
	if pong.Type != "pong" {
		t.Fatalf("期望 pong，got %+v", pong)
	}

	// detach → detached
	wsWrite(t, conn, map[string]any{"action": "detach", "agg_id": "s1"})
	var det frame
	wsRead(t, conn, &det)
	if det.Type != "detached" || det.AggID != "s1" {
		t.Fatalf("detached 帧不符: %+v", det)
	}
}

// TestWSHandlerReplayAfterSince 验证带 since_seq 的 attach 走历史重放（仅该聚合）。
func TestWSHandlerReplayAfterSince(t *testing.T) {
	bus := eventbus.New()
	e1 := bus.Publish(testEventID, "session.message", "s1", 1, map[string]any{"text": "one"}) // seq 1
	e2 := bus.Publish(testEventID, "session.message", "s1", 1, map[string]any{"text": "two"}) // seq 2
	bus.Publish(testEventID, "session.message", "s2", 1, map[string]any{"text": "other"})     // seq 3

	conn := startWS(t, bus)

	// attach s1 since=1 → 重放 seq2（仅该聚合）
	since := e1.Seq
	wsWrite(t, conn, map[string]any{"action": "attach", "agg_id": "s1", "since_seq": since})
	var replay frame
	wsRead(t, conn, &replay)
	if replay.Type != "event" || replay.AggID != "s1" || replay.Event == nil || replay.Event.Seq != e2.Seq {
		t.Fatalf("重放帧不符: %+v", replay)
	}
}

// TestWSHandlerFullReplayWithoutSince 验证**不带** since_seq → 全量回放
// （UI 冷开启既有会话重建视图），且回放**包含位次 0** 的那一条。
func TestWSHandlerFullReplayWithoutSince(t *testing.T) {
	bus := eventbus.New()
	bus.Publish(testEventID, "session.message", "s1", 1, map[string]any{"text": "one"}) // s1 seq 0
	bus.Publish(testEventID, "session.thought", "s1", 1, map[string]any{"text": "t"})   // s1 seq 1
	bus.Publish(testEventID, "session.message", "s2", 1, map[string]any{"text": "no"})  // s2 seq 0

	conn := startWS(t, bus)

	wsWrite(t, conn, map[string]any{"action": "attach", "agg_id": "s1"})
	var seqs []int64
	for len(seqs) < 2 {
		var f frame
		wsRead(t, conn, &f)
		if f.Type != "event" || f.AggID != "s1" || f.Event == nil {
			t.Fatalf("期望 s1 的事件帧，got %+v", f)
		}
		seqs = append(seqs, f.Event.Seq)
	}
	if seqs[0] != 0 || seqs[1] != 1 {
		t.Fatalf("全量回放应含位次 0 且升序: %v", seqs)
	}

	// 显式 `since_seq=0` 是**另一件事**："位次 0 之后" → 只回放 [1]。
	// （v21 之前 0 兼作"从头"的哨兵，这一条是那次改动在 WS 边界上的判据。）
	conn2 := startWS(t, bus)
	wsWrite(t, conn2, map[string]any{"action": "attach", "agg_id": "s1", "since_seq": int64(0)})
	var only frame
	wsRead(t, conn2, &only)
	if only.Type != "event" || only.Event == nil || only.Event.Seq != 1 {
		t.Fatalf("since_seq=0 应只回放位次 1: %+v", only)
	}
}

// TestWSHandlerSnapshotFallbackOnEviction 验证 attach 的 since 低于该聚合淘汰水位
// （eventbus 超限淘汰后）→ 回 snapshot（§5.2 快照/重放二分）。
func TestWSHandlerSnapshotFallbackOnEviction(t *testing.T) {
	bus := eventbus.NewWithLimit(2)
	conn := startWS(t, bus)

	// 同聚合发布 4 条（位次 0..3）：limit=2 → 缓冲只剩 [2,3]，淘汰水位（已淘汰的最高位次）= 1。
	// v21 起水上位是"淘汰到的最高位次"，`-1` 才是"从未淘汰"——这里 1 是真实位次。
	bus.Publish(testEventID, "session.message", "s1", 1, map[string]any{"text": "a"}) // seq 0
	bus.Publish(testEventID, "session.message", "s1", 1, map[string]any{"text": "b"}) // seq 1
	bus.Publish(testEventID, "session.message", "s1", 1, map[string]any{"text": "c"}) // seq 2
	bus.Publish(testEventID, "session.message", "s1", 1, map[string]any{"text": "d"}) // seq 3

	// since=0 < 水位 1 → 位次 0/1 已不可增量续传 → snapshot（带当前水位 3）
	wsWrite(t, conn, map[string]any{"action": "attach", "agg_id": "s1", "since_seq": int64(0)})
	var snap frame
	wsRead(t, conn, &snap)
	if snap.Type != "snapshot" || snap.AggID != "s1" || snap.EventSeq != 3 {
		t.Fatalf("淘汰回退应回 snapshot（带水位）: %+v", snap)
	}

	// 第二条连接 since=1（≥ 水位）→ 重放 seq2/seq3
	conn2 := startWS(t, bus)
	wsWrite(t, conn2, map[string]any{"action": "attach", "agg_id": "s1", "since_seq": int64(1)})
	var seqs []int64
	for len(seqs) < 2 {
		var f frame
		wsRead(t, conn2, &f)
		if f.Type != "event" || f.AggID != "s1" || f.Event == nil {
			t.Fatalf("期望 s1 事件帧，got %+v", f)
		}
		seqs = append(seqs, f.Event.Seq)
	}
	if seqs[0] != 2 || seqs[1] != 3 {
		t.Fatalf("水位后重放序号不符（应为 2、3）: %v", seqs)
	}
}

// wsHandshake 发首帧 attach（通道级握手，M1-1）：桌面态必须最先发、且带上 streamToken。
// 不带 agg_id —— 握手帧本身不订阅任何聚合；随后照旧发 attach agg_id 订阅。
func wsHandshake(t *testing.T, c *websocket.Conn, token string) {
	t.Helper()
	f := map[string]any{"action": "attach"}
	if token != "" {
		f["token"] = token
	}
	wsWrite(t, c, f)
}

// closeStatus 读下一帧并返回连接关闭码（期望服务端主动关闭的用例用它）。
// 读成功（拿到数据帧）视为失败——这些用例的前提就是"服务端不发数据帧直接关门"。
func closeStatus(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	if err == nil {
		t.Fatal("期望服务端关闭连接，但读到了数据帧")
	}
	return websocket.CloseStatus(err)
}

// TestWSHandshakeToken 首帧 attach 的凭据校验（M1-1 验收的前两条）：
// 对 token → 正常收事件；错 token → 1008；**restToken 走事件流也必须被拒**（两个面互斥）。
func TestWSAttachHandshakeToken(t *testing.T) {
	const rest = "rest-token-2f9c"
	const stream = "stream-token-71ab"
	creds := credential.MustNew(
		credential.Pair{Token: rest, Scopes: credential.ScopeREST},
		credential.Pair{Token: stream, Scopes: credential.ScopeStream},
	)

	t.Run("对 token 正常收事件", func(t *testing.T) {
		bus := eventbus.New()
		conn := dialWS(t, newWSEngineWith(bus, wsSetup{creds: creds}))

		wsHandshake(t, conn, stream)
		// 挂载前先放一条历史事件：无 since 的全量回放会把它投回来（"订阅成功"的可见证据）。
		bus.Publish(testEventID, "session.created", "s1", 1, map[string]any{"text": "pre"})
		wsWrite(t, conn, map[string]any{"action": "attach", "agg_id": "s1"})
		var snap frame
		wsRead(t, conn, &snap)
		if snap.Type != "event" || snap.AggID != "s1" || snap.Event == nil {
			t.Fatalf("握手后应能正常订阅（全量回放）: %+v", snap)
		}
		bus.Publish(testEventID, "session.message", "s1", 1, map[string]any{"text": "hi"})
		var evt frame
		wsRead(t, conn, &evt)
		if evt.Type != "event" || evt.AggID != "s1" {
			t.Fatalf("握手后应能收到事件帧，got %+v", evt)
		}
	})

	t.Run("错 token 关闭 1008", func(t *testing.T) {
		conn := dialWS(t, newWSEngineWith(eventbus.New(), wsSetup{creds: creds}))
		wsHandshake(t, conn, "nope")
		if got := closeStatus(t, conn); got != websocket.StatusPolicyViolation {
			t.Fatalf("错 token 应以 1008 关闭，got %v", got)
		}
	})

	t.Run("restToken 走事件流被拒（面互斥）", func(t *testing.T) {
		conn := dialWS(t, newWSEngineWith(eventbus.New(), wsSetup{creds: creds}))
		wsHandshake(t, conn, rest)
		if got := closeStatus(t, conn); got != websocket.StatusPolicyViolation {
			t.Fatalf("restToken 不得打开事件流，应以 1008 关闭，got %v", got)
		}
	})

	t.Run("无 token（凭据已配置）被拒", func(t *testing.T) {
		conn := dialWS(t, newWSEngineWith(eventbus.New(), wsSetup{creds: creds}))
		wsHandshake(t, conn, "")
		if got := closeStatus(t, conn); got != websocket.StatusPolicyViolation {
			t.Fatalf("凭据已配置时无 token 应以 1008 关闭，got %v", got)
		}
	})
}

// TestWSHandshakeDeadline 超时未 attach → 服务端主动以 1008 关闭（M1-1 验收第三条）。
//
// 这是"令牌不进 URL"的直接代价：连接先建立、凭据随后到。没有这条时限，
// 未鉴权连接就能一直占着 eventbus 订阅位。
func TestWSAttachHandshakeDeadline(t *testing.T) {
	conn := dialWS(t, newWSEngineWith(eventbus.New(), wsSetup{
		creds:    credential.MustNew(credential.Pair{Token: "stream-token-71ab", Scopes: credential.ScopeStream}),
		deadline: 150 * time.Millisecond,
	}))
	// 连上后什么都不发。
	if got := closeStatus(t, conn); got != websocket.StatusPolicyViolation {
		t.Fatalf("超时未 attach 应以 1008 关闭，got %v", got)
	}
}

// TestWSHandshakeFirstFrameMustBeAttach 首帧不是 attach（例如直接 ping）→ 1008。
// 这条钉住"握手是第一条帧"的协议形状：否则客户端可以先用其他动作探路。
func TestWSAttachHandshakeFirstFrameMustBeAttach(t *testing.T) {
	conn := dialWS(t, newWSEngine(eventbus.New()))
	wsWrite(t, conn, map[string]any{"action": "ping"})
	if got := closeStatus(t, conn); got != websocket.StatusPolicyViolation {
		t.Fatalf("首帧非 attach 应以 1008 关闭，got %v", got)
	}
}

// TestWSHandshakeNotRequiredWithoutCredentials 未配置凭据（独立态默认）时，
// 首帧 attach 仍必须发（协议形状统一），但 token 值无关紧要——不鉴权就是真不鉴权。
func TestWSAttachHandshakeNotRequiredWithoutCredentials(t *testing.T) {
	bus := eventbus.New()
	conn := dialWS(t, newWSEngine(bus))
	wsHandshake(t, conn, "任意值都不校验")
	bus.Publish(testEventID, "session.created", "s1", 1, map[string]any{"text": "pre"})
	wsWrite(t, conn, map[string]any{"action": "attach", "agg_id": "s1"})
	var snap frame
	wsRead(t, conn, &snap)
	if snap.Type != "event" || snap.AggID != "s1" || snap.Event == nil {
		t.Fatalf("未配置凭据时握手后应能订阅（全量回放）: %+v", snap)
	}
}

// TestWSAcceptSurvivesAccessLogWriterWrap 守护「访问日志包装 ctx.Writer」与本包 WS 升级的相容性。
//
// golib v1.32.16 起默认挂载的 ginmiddleware.AccessLog 会用 ResponseCaptureWriter 包一层
// ctx.Writer（用于有界采集响应体），而 WebSocket 握手依赖 `http.Hijacker`（coder/websocket
// 直接对传入的 ResponseWriter 做类型断言）。包装类型只靠内嵌 gin.ResponseWriter 透出
// Hijack —— 一旦这层关系变了（换实现、多包一层、改走 Unwrap），升级会直接失败，
// 而 ctrws 自身单测不带中间件、router 侧又只断言「非升级请求 426」，两条都发现不了。
// 因此这里显式挂上真实 AccessLog 跑一次 ping/pong，把回归钉在传输层。
func TestWSAcceptSurvivesAccessLogWriterWrap(t *testing.T) {
	bus := eventbus.New()
	eng := newWSEngine(bus, ginmiddleware.AccessLog())

	// 前提校验：先确认 AccessLog 真的在链上。否则「WS 能连上」可能只是因为中间件
	// 压根没跑，这条测试就成了空转（AccessLog 会在入口 Set(KeyUrlFull)，据此判定）。
	probeOK := false
	eng.GET("/probe", func(c *gin.Context) {
		_, probeOK = c.Get(gcontext.KeyUrlFull)
		c.Status(http.StatusNoContent)
	})
	probe := httptest.NewRecorder()
	eng.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/probe", nil))
	if !probeOK {
		t.Fatal("AccessLog 未生效：本测试的前提不成立，WS 断言等于空转")
	}

	conn := dialWS(t, eng)
	// 首帧必须是 attach（M1-1 的握手），之后才轮到 ping/pong。
	wsHandshake(t, conn, "")
	wsWrite(t, conn, map[string]any{"action": "ping"})
	var pong frame
	wsRead(t, conn, &pong)
	if pong.Type != "pong" {
		t.Fatalf("被访问日志包装后 WS 仍应可用，期望 pong，got %+v", pong)
	}
}

// TestReplayFramesBracketReplay 回放区间帧（ADR-13）：`replay_begin` 恒在 `replay_end` 之前，
// 且把它们之间的事件**全部**包住。
//
// 为什么这一条需要行为判据（源码那半由 archtest 的 `TestReplayFramesBracketHistory` 钉）：
// "两帧都在、顺序也对"可以在**回放之外**成立（例如放在 attach 的开头和一条 defer 里），
// 而那样前端仍然会在回放中途退出加载态、在回放结束后才开始等——两种表现都不报错。
func TestReplayFramesBracketReplay(t *testing.T) {
	bus := eventbus.New()
	pre1 := bus.Publish(testEventID, "session.created", "s1", 1, map[string]any{"text": "a"})
	pre2 := bus.Publish(testEventID, "session.user_message", "s1", 2, map[string]any{"text": "b"})
	conn := startWS(t, bus)
	wsWrite(t, conn, map[string]any{"action": "attach", "agg_id": "s1"})

	var types []string
	var seqs []int64
	// 三帧：begin + 两条事件 + end。
	for i := 0; i < 4; i++ {
		var f frame
		if err := json.Unmarshal(wsReadRaw(t, conn), &f); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		types = append(types, f.Type)
		seqs = append(seqs, f.EventSeq)
	}
	want := []string{"replay_begin", "event", "event", "replay_end"}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("帧序列不符：got %v（want %v）：回放必须被 begin/end **夹住**", types, want)
		}
	}
	if seqs[0] != -1 {
		t.Fatalf("全量回放时 `replay_begin.event_seq` 应为 -1（= 从最早一条起），got %d", seqs[0])
	}
	if seqs[3] != pre2.Seq {
		t.Fatalf("`replay_end.event_seq` 应为回放到的位次 %d，got %d", pre2.Seq, seqs[3])
	}
	_ = pre1

	// 增量续传：begin 带请求的起点。
	conn2 := startWS(t, bus)
	wsWrite(t, conn2, map[string]any{"action": "attach", "agg_id": "s1", "since_seq": pre1.Seq})
	var begin frame
	if err := json.Unmarshal(wsReadRaw(t, conn2), &begin); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if begin.Type != "replay_begin" || begin.EventSeq != pre1.Seq {
		t.Fatalf("增量续传的 begin 帧应带请求起点 %d：%+v", pre1.Seq, begin)
	}
}
