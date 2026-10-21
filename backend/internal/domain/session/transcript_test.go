package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/platform/object/objid"
)

// TestTranscriptJSONL 验证 wire transcript：header 首行 + 事件原语行、schema v2（事件行带
// 身份 `id` 与位次 `seq`）、append-only 单文件。
//
// **身份必须与帧上那一份逐字相同**（v17 / J4）：这里同时从 bus 拿到事件，逐条比对 transcript
// 行里的 `id` ——只要有一处重铸（比如落盘时又 `objid.New` 一次），这条就红，而"两张皮"在
// 别的测试里完全看不出来（形状都合法）。
//
// P3b-2b：随 recorder 进本域（此前在 `manager/transcript_test.go`，走的是 manager 的 Service）。
func TestTranscriptJSONL(t *testing.T) {
	dir := t.TempDir()
	pl, bus := testPlane(t, dir)
	defer pl.CloseTranscripts()

	pl.BeginTranscript("s1", "rtx", "/tmp", false)
	pl.Publish(BusUserMessage, "s1", map[string]any{"session_id": "s1", "runtime": "rtx", "text": "hi"})
	pl.Publish(BusMessage, "s1", map[string]any{"session_id": "s1", "runtime": "rtx", "text": "hello"})
	pl.CloseTranscripts()

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
	if h.Schema != wireTranscriptSchema || h.Version != 2 || h.SessionID != "s1" || h.Runtime != "rtx" {
		t.Fatalf("header 不符: %+v", h)
	}
	var l1 wireTranscriptLine
	if err := json.Unmarshal([]byte(lines[1]), &l1); err != nil {
		t.Fatalf("行1 解析失败: %v", err)
	}
	// 下界是 0（v21 起会话第一条事件的位次就是 0；`<= 0` 会把正常行判成缺位次）。
	if l1.Type != BusUserMessage || l1.Seq < 0 || l1.Payload["text"] != "hi" {
		t.Fatalf("行1 不符: %+v", l1)
	}
	var l2 wireTranscriptLine
	if err := json.Unmarshal([]byte(lines[2]), &l2); err != nil {
		t.Fatalf("行2 解析失败: %v", err)
	}
	// 身份：transcript 行里的 id 必须与**帧上那一份**逐字相同（不重铸）。
	busEvents := bus.EventsAll()
	if len(busEvents) != 2 {
		t.Fatalf("期望 2 条总线事件，got %d", len(busEvents))
	}
	if !objid.IsPlatformID(l1.ID) || !strings.HasPrefix(l1.ID, objid.Event) {
		t.Fatalf("行1 的事件身份形状不对: %q", l1.ID)
	}
	if l1.ID != busEvents[0].ID || l2.ID != busEvents[1].ID {
		t.Fatalf("transcript 的事件身份与帧上不一致: 行1=%q 帧1=%q 行2=%q 帧2=%q",
			l1.ID, busEvents[0].ID, l2.ID, busEvents[1].ID)
	}
	// 位次：transcript 行里的 seq 就是帧上的 seq（同一个权威分配）。
	if l1.Seq != busEvents[0].Seq || l2.Seq != busEvents[1].Seq {
		t.Fatalf("transcript 的 seq 与帧上不一致: 行1=%d 帧1=%d 行2=%d 帧2=%d",
			l1.Seq, busEvents[0].Seq, l2.Seq, busEvents[1].Seq)
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
