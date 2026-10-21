package run

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/platform/redact"
	"github.com/morehao/ark-mate/internal/domain/provider"
)

// TestPublishRedactsSecretsAtSingleGate ② 脱敏闸门：publish 是事件唯一出口，
// 一处过闸即覆盖三个落点——WS 帧（bus 事件）、events 表、transcript 文件；
// 且只改写出口副本，调用方持有的载荷不被就地修改。
func TestPublishRedactsSecretsAtSingleGate(t *testing.T) {
	svc, st := newSearchRig(t)
	const secret = "sk-abcdefghijklmnop1234"

	dir := t.TempDir()
	svc.SessionPlane().SetTranscriptDir(dir)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "conv-redact"})
	transcriptPlane(svc).BeginTranscript("conv-redact", "stub", "/tmp", false)
	defer transcriptPlane(svc).CloseTranscripts()

	payload := map[string]any{
		"session_id": "conv-redact",
		"msg_id":     "m1",
		"text":       "runtime 报错原文：OPENAI_API_KEY=" + secret,
	}
	svc.sessionPlane.Publish(session.BusMessage, "conv-redact", payload)
	svc.sessionPlane.FlushChunks() // ③ 文本增量攒批：断言三个落点前先落盘

	// 1) WS 帧（bus 事件 —— 订阅者/WS 收到的同一份）
	var frame eventbus.Event
	for _, e := range svc.bus.EventsAll() {
		if e.Type == session.BusMessage && e.AggregID == "conv-redact" {
			frame = e
		}
	}
	if frame.Type == "" {
		t.Fatal("未找到 session.message 的 bus 事件")
	}
	if b, _ := json.Marshal(frame); strings.Contains(string(b), secret) {
		t.Fatalf("WS 帧未脱敏: %s", b)
	} else if !strings.Contains(string(b), redact.Placeholder) {
		t.Fatalf("WS 帧应含占位串: %s", b)
	}
	// 2) events 表
	rows, _, err := st.EventsAfter(context.Background(), "conv-redact", -1, 10)
	if err != nil || len(rows) == 0 {
		t.Fatalf("事件未落库: n=%d err=%v", len(rows), err)
	}
	if b, _ := json.Marshal(rows); strings.Contains(string(b), secret) {
		t.Fatalf("events 表未脱敏: %s", b)
	}
	// 3) transcript 文件
	raw, err := os.ReadFile(filepath.Join(dir, "conv-redact.jsonl"))
	if err != nil {
		t.Fatalf("读 transcript: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("transcript 未脱敏: %s", raw)
	}
	if !strings.Contains(string(raw), redact.Placeholder) {
		t.Fatalf("transcript 应含占位串: %s", raw)
	}
	// 4) 出口副本脱敏，入参不被就地改写
	if got, _ := payload["text"].(string); !strings.Contains(got, secret) {
		t.Fatalf("入参不应被就地改写: %q", got)
	}
}

// TestPublishRedactsDynamicProviderKey 已登记的真实密钥值：即便形状完全不像密钥
// （无 sk-/AKIA 等前缀），出现在事件里也必须被替换。
func TestPublishRedactsDynamicProviderKey(t *testing.T) {
	svc, _ := newSearchRig(t)
	const key = "hunter2-not-a-known-shape"
	unregister := redact.RegisterDynamicSecret(key)
	defer unregister()

	evt := svc.sessionPlane.Publish(session.BusRunError, "conv-dyn", map[string]any{
		"session_id": "conv-dyn",
		"details":    "上游拒绝：key=" + key,
	})
	if b, _ := json.Marshal(evt.Payload); strings.Contains(string(b), key) {
		t.Fatalf("已登记的真实密钥必须被替换: %s", b)
	}
}

// TestRunErrorPayloadRedactsStderrTail run.error 的 stderr_tail 走 acp.TailLines 出口，
// 已在那一步过闸（此处验证装配后的效果：事件里看不到密钥）。
func TestRunErrorPayloadRedactsStderrTail(t *testing.T) {
	svc, _ := newSearchRig(t)
	const secret = "sk-abcdefghijklmnop1234"
	evt := svc.sessionPlane.Publish(session.BusRunError, "conv-tail", map[string]any{
		"session_id":  "conv-tail",
		"details":     "进程退出",
		"stderr_tail": []string{"OPENAI_API_KEY=" + secret, "listening on :8080"},
	})
	b, _ := json.Marshal(evt.Payload)
	if strings.Contains(string(b), secret) {
		t.Fatalf("stderr_tail 未脱敏: %s", b)
	}
	if !strings.Contains(string(b), "listening on :8080") {
		t.Fatalf("正常 stderr 行应保留: %s", b)
	}
}

// ——— v20 的动态那半（设计 §8.3-①）：**经写面/解析面进来的真值必须自动进闸门** ———
//
// 为什么不能只靠形状正则：真实密钥常常没有任何可辨识形状（自建网关的 token、随机短串），
// 而 v20 把密钥的权威搬进库之后，平台是**唯一**知道这些值的地方——不在这里登记，
// 一把无形状的密钥就会原样出现在事件/transcript/报错里，且不会有任何断言变红。

// TestProviderKeyValueIsRedactedAfterWriteFace 通过写面存一把**无形状**的密钥 → 三个落点零命中。
func TestProviderKeyValueIsRedactedAfterWriteFace(t *testing.T) {
	ctx := context.Background()
	svc, st := newSearchRig(t)
	home := t.TempDir()
	planesOf(svc).Credentials.SetLayered(newTestCredentials(t, home))
	planesOf(svc).Providers.SetProviderDir(filepath.Join(home, "providers"))

	key := "zq7-not-a-known-shape-42" // 形状上谁都认不出来
	if _, err := planesOf(svc).Providers.CreateProvider(ctx, provider.CreateRequest{
		ID: "dyn", Protocol: "openai", APIKey: &key,
	}); err != nil {
		t.Fatalf("写面存密钥: %v", err)
	}
	// 写面回执本身不得回显明文（另一条判据，但放在同一现场里更省事）。
	items, err := planesOf(svc).Providers.ListProviders(ctx)
	if err != nil || len(items) != 1 || !items[0].HasKey {
		t.Fatalf("装回读面: n=%d err=%v", len(items), err)
	}
	if strings.Contains(strings.Join(items[0].Problems, " "), key) {
		t.Fatal("读面 problems 不该含明文")
	}

	dir := t.TempDir()
	svc.SessionPlane().SetTranscriptDir(dir)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "conv-dyn-v20"})
	transcriptPlane(svc).BeginTranscript("conv-dyn-v20", "stub", "/tmp", false)
	defer transcriptPlane(svc).CloseTranscripts()
	svc.sessionPlane.Publish(session.BusMessage, "conv-dyn-v20", map[string]any{
		"session_id": "conv-dyn-v20",
		"msg_id":     "m1",
		"text":       "底座报错原文：" + key,
	})
	svc.sessionPlane.FlushChunks()

	// 1) WS 帧 2) events 表 3) transcript 文件
	var frame eventbus.Event
	for _, e := range svc.bus.EventsAll() {
		if e.Type == session.BusMessage && e.AggregID == "conv-dyn-v20" {
			frame = e
		}
	}
	if b, _ := json.Marshal(frame); strings.Contains(string(b), key) {
		t.Fatalf("WS 帧未脱敏（写面/解析面进来的真值必须自动登记）: %s", b)
	}
	rows, _, err := st.EventsAfter(ctx, "conv-dyn-v20", -1, 10)
	if err != nil || len(rows) == 0 {
		t.Fatalf("事件未落库: n=%d err=%v", len(rows), err)
	}
	if b, _ := json.Marshal(rows); strings.Contains(string(b), key) {
		t.Fatalf("events 表未脱敏: %s", b)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "conv-dyn-v20.jsonl"))
	if err != nil {
		t.Fatalf("读 transcript: %v", err)
	}
	if strings.Contains(string(raw), key) {
		t.Fatalf("transcript 未脱敏: %s", raw)
	}
}

// TestMcpSecretValueIsRedactedAfterResolution 库里那把 MCP 密钥被解析出来之后同样进闸门。
//
// 这条对应一个真实的泄漏形态：stdio MCP server 把凭据打进 stderr（"401 token=…"），
// 那串经 `run.error.stderr_tail` 外露。v20 之前平台不知道 MCP 密钥的值（那是密文），
// 现在它就在我们库里——登记它是平台自己的责任。
func TestMcpSecretValueIsRedactedAfterResolution(t *testing.T) {
	ctx := context.Background()
	svc, st := newSearchRig(t)
	home := t.TempDir()
	planesOf(svc).Credentials.SetLayered(newTestCredentials(t, home))
	planesOf(svc).Providers.SetProviderDir(filepath.Join(home, "providers"))
	enableStore(svc, st)

	const ref = "store:mcp/dyn-token"
	const val = "mcp-7f-not-a-known-shape" // 无形状：只能靠登记值那一档
	if err := st.SetMcpSecret(ctx, ref, val); err != nil {
		t.Fatal(err)
	}
	if err := planesOf(svc).Capabilities.RegisterMcpServer(&capability.McpServer{
		ID: "dyn", Transport: capability.TransportStdio, Command: "/usr/local/bin/npx",
		SecretEnv: map[string]string{"TOKEN": ref},
	}); err != nil {
		t.Fatal(err)
	}
	got := directMcp(svc.McpServersFor("stub", "", nil))
	if len(got) != 1 || got[0].Env["TOKEN"] != val {
		t.Fatalf("注入侧应解析出明文: %+v", got)
	}

	dir := t.TempDir()
	svc.SessionPlane().SetTranscriptDir(dir)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "conv-mcp-dyn"})
	transcriptPlane(svc).BeginTranscript("conv-mcp-dyn", "stub", "/tmp", false)
	defer transcriptPlane(svc).CloseTranscripts()
	svc.sessionPlane.Publish(session.BusRunError, "conv-mcp-dyn", map[string]any{
		"session_id":  "conv-mcp-dyn",
		"details":     "子进程退出",
		"stderr_tail": []string{"401 Unauthorized: token=" + val},
	})
	svc.sessionPlane.FlushChunks()

	var frame eventbus.Event
	for _, e := range svc.bus.EventsAll() {
		if e.Type == session.BusRunError && e.AggregID == "conv-mcp-dyn" {
			frame = e
		}
	}
	if b, _ := json.Marshal(frame); strings.Contains(string(b), val) {
		t.Fatalf("run.error 的 stderr_tail 未脱敏: %s", b)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "conv-mcp-dyn.jsonl"))
	if err != nil {
		t.Fatalf("读 transcript: %v", err)
	}
	if strings.Contains(string(raw), val) {
		t.Fatalf("transcript 未脱敏: %s", raw)
	}
}
