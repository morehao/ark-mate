package manager

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/golib/gerror"
)

// TestTranscriptJSONL 验证 wire transcript：header 首行 + 事件原语行、schema v1、append-only 单文件。
func TestTranscriptJSONL(t *testing.T) {
	reg := runtime.NewRegistry()
	bus := eventbus.New()
	svc := NewService(reg, bus)
	dir := t.TempDir()
	svc.SetTranscriptDir(dir)
	defer svc.closeTranscripts()

	svc.beginTranscript("s1", "agent-opencode", "opencode", "/tmp")
	svc.publish(BusUserMessage, "s1", map[string]any{"session_id": "s1", "agent_id": "a", "text": "hi"})
	svc.publish(BusMessage, "s1", map[string]any{"session_id": "s1", "agent_id": "a", "text": "hello"})
	svc.closeTranscripts()

	path := filepath.Join(dir, "s1.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	lines := splitNonEmpty(string(data))
	if len(lines) != 3 {
		t.Fatalf("期望 header+2 行，got %d 行", len(lines))
	}
	var h wireTranscriptHeader
	if err := json.Unmarshal([]byte(lines[0]), &h); err != nil {
		t.Fatalf("header 解析失败: %v", err)
	}
	if h.Schema != wireTranscriptSchema || h.Version != 1 || h.SessionID != "s1" || h.Runtime != "opencode" {
		t.Fatalf("header 不符: %+v", h)
	}
	var l1 wireTranscriptLine
	if err := json.Unmarshal([]byte(lines[1]), &l1); err != nil {
		t.Fatalf("行1 解析失败: %v", err)
	}
	if l1.Type != BusUserMessage || l1.Seq <= 0 || l1.Payload["text"] != "hi" {
		t.Fatalf("行1 不符: %+v", l1)
	}
}

// TestSessionHistoryPaging 验证 history 分页（after/limit、has_more、仅本聚合、未知会话 40401）。
func TestSessionHistoryPaging(t *testing.T) {
	reg := runtime.NewRegistry()
	bus := eventbus.New()
	svc := NewService(reg, bus)
	svc.registerSession(&SessionRec{ID: "s1", AgentID: "agent-opencode", Runtime: "opencode"})

	for i := 0; i < 5; i++ {
		svc.publish(BusMessage, "s1", map[string]any{"text": "msg"})
	}
	svc.publish(BusThought, "s2", map[string]any{"text": "other"}) // 另一聚合不混入

	page1, err := svc.SessionHistory(context.Background(), "s1", 0, 2)
	if err != nil {
		t.Fatalf("history page1: %v", err)
	}
	if len(page1.Events) != 2 || !page1.HasMore {
		t.Fatalf("page1 不符: %+v", page1)
	}
	page2, err := svc.SessionHistory(context.Background(), "s1", page1.Events[1].Seq, 2)
	if err != nil {
		t.Fatalf("history page2: %v", err)
	}
	if len(page2.Events) != 2 || !page2.HasMore || page2.Events[0].AggregID != "s1" {
		t.Fatalf("page2 不符: %+v", page2)
	}
	tail, err := svc.SessionHistory(context.Background(), "s1", page2.Events[1].Seq, 2)
	if err != nil {
		t.Fatalf("history tail: %v", err)
	}
	if len(tail.Events) != 1 || tail.HasMore {
		t.Fatalf("tail 不符: %+v", tail)
	}

	if _, err := svc.SessionHistory(context.Background(), "ghost", 0, 10); err == nil {
		t.Fatal("未知会话应报错")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != 40401 {
			t.Fatalf("期望 40401，got %v", err)
		}
	}
}

func splitNonEmpty(s string) []string {
	sc := bufio.NewScanner(strings.NewReader(s))
	var out []string
	for sc.Scan() {
		if ln := strings.TrimSpace(sc.Text()); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}
