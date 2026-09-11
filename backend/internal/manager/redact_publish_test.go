package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/pkg/redact"
)

// TestPublishRedactsSecretsAtSingleGate ② 脱敏闸门：publish 是事件唯一出口，
// 一处过闸即覆盖三个落点——WS 帧（bus 事件）、events 表、transcript 文件；
// 且只改写出口副本，调用方持有的载荷不被就地修改。
func TestPublishRedactsSecretsAtSingleGate(t *testing.T) {
	svc, st := newSearchRig(t)
	const secret = "sk-abcdefghijklmnop1234"

	dir := t.TempDir()
	svc.SetTranscriptDir(dir)
	svc.registerConversation(&ConversationRec{ID: "conv-redact", AgentID: "a1"})
	svc.tx.begin("conv-redact", "a1", "echo", "/tmp", false)
	defer svc.tx.closeAll()

	payload := map[string]any{
		"session_id": "conv-redact",
		"msg_id":     "m1",
		"text":       "runtime 报错原文：OPENAI_API_KEY=" + secret,
	}
	svc.publish(BusMessage, "conv-redact", payload)
	svc.flushChunks() // ③ 文本增量攒批：断言三个落点前先落盘

	// 1) WS 帧（bus 事件 —— 订阅者/WS 收到的同一份）
	var frame eventbus.Event
	for _, e := range svc.bus.EventsAfter(0) {
		if e.Type == BusMessage && e.AggregID == "conv-redact" {
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
	rows, _, err := st.EventsAfter(context.Background(), "conv-redact", 0, 10)
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

	evt := svc.publish(BusTurnError, "conv-dyn", map[string]any{
		"session_id": "conv-dyn",
		"details":    "上游拒绝：key=" + key,
	})
	if b, _ := json.Marshal(evt.Payload); strings.Contains(string(b), key) {
		t.Fatalf("已登记的真实密钥必须被替换: %s", b)
	}
}

// TestTurnErrorPayloadRedactsStderrTail turn.error 的 stderr_tail 走 acl.TailLines 出口，
// 已在那一步过闸（此处验证装配后的效果：事件里看不到密钥）。
func TestTurnErrorPayloadRedactsStderrTail(t *testing.T) {
	svc, _ := newSearchRig(t)
	const secret = "sk-abcdefghijklmnop1234"
	evt := svc.publish(BusTurnError, "conv-tail", map[string]any{
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
