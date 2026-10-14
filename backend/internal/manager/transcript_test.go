package manager

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/gerror"
)

// TestTranscriptJSONL 验证 wire transcript：header 首行 + 事件原语行、schema v2（事件行带
// 身份 `id` 与位次 `seq`）、append-only 单文件。
//
// **身份必须与帧上那一份逐字相同**（v17 / J4）：这里同时从 bus 拿到事件，逐条比对 transcript
// 行里的 `id` ——只要有一处重铸（比如落盘时又 `objid.New` 一次），这条就红，而"两张皮"在
// 别的测试里完全看不出来（形状都合法）。
func TestTranscriptJSONL(t *testing.T) {
	reg := runtime.NewRegistry()
	bus := eventbus.New()
	svc := NewService(reg, bus)
	dir := t.TempDir()
	svc.SetTranscriptDir(dir)
	defer svc.closeTranscripts()

	svc.beginTranscript("s1", "rtx", "/tmp", false)
	svc.publish(BusUserMessage, "s1", map[string]any{"session_id": "s1", "runtime": "rtx", "text": "hi"})
	svc.publish(BusMessage, "s1", map[string]any{"session_id": "s1", "runtime": "rtx", "text": "hello"})
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

// TestSessionHistoryPaging 验证 history 分页（after/limit、has_more、仅本聚合、未知会话 40401）。
func TestSessionHistoryPaging(t *testing.T) {
	reg := runtime.NewRegistry()
	bus := eventbus.New()
	svc := NewService(reg, bus)
	svc.registerSession(&SessionRec{ID: "s1"})

	// ③ 文本增量合并：不同 msg_id = 不同消息，5 条各自成事件（断言前 flush 落盘）。
	for i := 0; i < 5; i++ {
		svc.publish(BusMessage, "s1", map[string]any{"msg_id": fmt.Sprintf("m%d", i), "text": "msg"})
	}
	svc.publish(BusThought, "s2", map[string]any{"text": "other"}) // 另一聚合不混入
	svc.flushChunks()

	// `After: -1` 显式声明"从头"：v21 起 `After` 的零值 0 表示"位次 0 之后"（会跳过第 0 条），
	// 只有 -1 是"没有游标"。控制器那侧也是这么默认的（见 dtosession.HistoryQuery 注释）。
	page1, err := svc.SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(-1), Limit: 2})
	if err != nil {
		t.Fatalf("history page1: %v", err)
	}
	if len(page1.Events) != 2 || !page1.HasMore {
		t.Fatalf("page1 不符: %+v", page1)
	}
	page2, err := svc.SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(page1.Events[1].Seq), Limit: 2})
	if err != nil {
		t.Fatalf("history page2: %v", err)
	}
	if len(page2.Events) != 2 || !page2.HasMore || page2.Events[0].AggregID != "s1" {
		t.Fatalf("page2 不符: %+v", page2)
	}
	tail, err := svc.SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(page2.Events[1].Seq), Limit: 2})
	if err != nil {
		t.Fatalf("history tail: %v", err)
	}
	if len(tail.Events) != 1 || tail.HasMore {
		t.Fatalf("tail 不符: %+v", tail)
	}

	// C2：before 向前分页——从末尾往回翻，升序返回，has_more 指向「更早还有」。
	lastPage, err := svc.SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{Before: ptr64(tail.Events[0].Seq), Limit: 2})
	if err != nil {
		t.Fatalf("before 分页: %v", err)
	}
	if len(lastPage.Events) != 2 || !lastPage.HasMore {
		t.Fatalf("before 分页不符: %+v", lastPage)
	}
	if lastPage.Events[0].Seq >= lastPage.Events[1].Seq {
		t.Fatalf("before 分页应升序返回: %+v", lastPage.Events)
	}
	if lastPage.LastSeq >= tail.Events[0].Seq {
		t.Fatalf("before 页末条应早于游标: last=%d cursor=%d", lastPage.LastSeq, tail.Events[0].Seq)
	}
	oldest, err := svc.SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{Before: ptr64(lastPage.FirstSeq), Limit: 10})
	if err != nil {
		t.Fatalf("before 到底: %v", err)
	}
	if len(oldest.Events) != 2 || oldest.HasMore || oldest.FirstSeq != 0 {
		t.Fatalf("翻到最早一页应 has_more=false 且首条 seq=0（v21 起最早一条是 0）: %+v", oldest)
	}
	// 互斥游标 → 40001（服务层兜底）。`After: 0` 也要算"给了游标"——它曾经是"没给"的哨兵，
	// 现在它是合法游标，拿零值当"未设置"会让 after+before 同时存在时静默按 before 处理。
	if _, err := svc.SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(0), Before: ptr64(2)}); err == nil {
		t.Fatal("after+before 应报错")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != 40001 {
			t.Fatalf("期望 40001，got %v", err)
		}
	}
	if _, err := svc.SessionHistory(context.Background(), "ghost", dtosession.HistoryQuery{Limit: 10}); err == nil {
		t.Fatal("未知会话应报错")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != 40401 {
			t.Fatalf("期望 40401，got %v", err)
		}
	}
}

// ptr64 取 int64 的地址（构造"给了游标"的 HistoryQuery 用；v21 起游标有无由指针承载）。
func ptr64(v int64) *int64 { return &v }

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

// TestSessionHistoryStoreBacked sqlite 模式（C2）：历史读 events 表而非 eventbus——
// 双向游标（after/before）、has_more 沿方向、首尾 seq、载荷解码、跨会话隔离。
func TestSessionHistoryStoreBacked(t *testing.T) {
	st := testutil.OpenStore(t, ":memory:")
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	if err := st.UpsertSession(context.Background(), model.SessionEntity{
		ID: "s1", Status: "in_progress", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	svc, _, _ := newTestRig(t)
	svc.EnableStore(st)
	svc.registerSession(&SessionRec{ID: "s1"})
	for i := 0; i < 5; i++ {
		svc.publish(BusMessage, "s1", map[string]any{"msg_id": fmt.Sprintf("m%d", i), "text": fmt.Sprintf("m%d", i)})
	}
	svc.publish(BusMessage, "s2", map[string]any{"text": "other"})
	svc.flushChunks()

	// 向后分页（after）。
	p1, err := svc.SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(-1), Limit: 3})
	if err != nil {
		t.Fatalf("store history p1: %v", err)
	}
	if len(p1.Events) != 3 || !p1.HasMore || p1.FirstSeq != 0 || p1.LastSeq != p1.Events[2].Seq {
		t.Fatalf("store 首页不符（首条应为位次 0）: %+v", p1)
	}
	// `After: 0` = "位次 0 **之后**"：跳过第 0 条。这条把 v21 的哨兵改动钉在服务层上
	// ——零值不再等于"从头"，忘写 -1 的调用方会静默丢掉该会话的第一条事件。
	skip0, err := svc.SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(0), Limit: 1})
	if err != nil {
		t.Fatalf("store history after=0: %v", err)
	}
	if len(skip0.Events) != 1 || skip0.Events[0].Seq != 1 {
		t.Fatalf("After=0 应跳过位次 0: %+v", skip0)
	}
	if m, ok := p1.Events[0].Payload.(map[string]any); !ok || m["text"] != "m0" {
		t.Fatalf("载荷未解码: %#v", p1.Events[0].Payload)
	}
	p2, err := svc.SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(p1.LastSeq), Limit: 3})
	if err != nil {
		t.Fatalf("store history p2: %v", err)
	}
	if len(p2.Events) != 2 || p2.HasMore {
		t.Fatalf("store 末页不符: %+v", p2)
	}

	// 向前分页（before）：从末页首条往回翻。
	back, err := svc.SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{Before: ptr64(p2.FirstSeq), Limit: 2})
	if err != nil {
		t.Fatalf("store history before: %v", err)
	}
	if len(back.Events) != 2 || !back.HasMore {
		t.Fatalf("before 页不符: %+v", back)
	}
	if back.LastSeq >= p2.FirstSeq {
		t.Fatalf("before 页应早于游标: last=%d cursor=%d", back.LastSeq, p2.FirstSeq)
	}
	back0, err := svc.SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{Before: ptr64(back.FirstSeq), Limit: 5})
	if err != nil {
		t.Fatalf("store history before 到底: %v", err)
	}
	if len(back0.Events) != 1 || back0.HasMore {
		t.Fatalf("最早一页不符: %+v", back0)
	}
	if m, ok := back0.Events[0].Payload.(map[string]any); !ok || m["text"] != "m0" {
		t.Fatalf("最早事件应为 m0: %#v", back0.Events[0].Payload)
	}
	for _, e := range append(append([]eventbus.Event{}, p1.Events...), back.Events...) {
		if e.AggregID != "s1" {
			t.Fatalf("跨会话混入: %+v", e)
		}
	}
}
