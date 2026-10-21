package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"github.com/morehao/ark-mate/internal/domain/session/sessionrow"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/contract/dtosession"
	appsession "github.com/morehao/ark-mate/internal/app/session"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/golib/gerror"
)

// sessionPlaneOf 取会话域平面：**读模型**（列表 / 单条 / 轮次 / 排队输入 / 检索 / 用量）由域直接
// 提供，`manager` 侧那份 TODO(P4b) 转发已删（P4a 正文第十一刀）。
//
// 夹具链不变——测试本来就在驱动真平面，换的只是取用路径。
func sessionPlaneOf(svc *Service) *session.Plane { return svc.sessionPlane }

// historySvc 取会话历史读面的**用例层**（P4a 正文第十刀起它不在 manager 上）。
//
// 夹具链不变：库句柄、会话域平面、事件总线都还是同一个，用例只是换了个所有者。
func historySvc(svc *Service) appsession.HistorySvc {
	return appsession.NewHistory(storeAsEventReader(planesOf(svc).Resources.Store()), svc.sessionPlane, svc.bus)
}

// 本文件是**会话历史读面的装配级用例**（此前与 transcript 落盘用例同处
// `manager/transcript_test.go`）。
//
// P3b-2b 把 transcript 那半（落盘格式、启动回读、检索、handoff）整体搬进 `domain/session`，
// 域内那份判据在那边（`domain/session/transcript*_test.go`）；这里留下的是**必须经 manager
// 装配链才成立**的两条：`SessionHistory`（读面不在域里）与它依赖的读模型/总线接线。
// 两边不互相替代：那边验"域自己对不对"，这里验"装配接上了没有"。

// TestSessionHistoryPaging 验证 history 分页（after/limit、has_more、仅本聚合、未知会话 40401）。
func TestSessionHistoryPaging(t *testing.T) {
	svc := newServiceForTest(testutil.StubRegistry(), eventbus.New())
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	// ③ 文本增量合并：不同 msg_id = 不同消息，5 条各自成事件（断言前 flush 落盘）。
	for i := 0; i < 5; i++ {
		svc.sessionPlane.Publish(session.BusMessage, "s1", map[string]any{"msg_id": fmt.Sprintf("m%d", i), "text": "msg"})
	}
	svc.sessionPlane.Publish(session.BusThought, "s2", map[string]any{"text": "other"}) // 另一聚合不混入
	svc.sessionPlane.FlushChunks()

	// `After: -1` 显式声明"从头"：v21 起 `After` 的零值 0 表示"位次 0 之后"（会跳过第 0 条），
	// 只有 -1 是"没有游标"。控制器那侧也是这么默认的（见 dtosession.HistoryQuery 注释）。
	page1, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(-1), Limit: 2})
	if err != nil {
		t.Fatalf("history page1: %v", err)
	}
	if len(page1.Events) != 2 || !page1.HasMore {
		t.Fatalf("page1 不符: %+v", page1)
	}
	page2, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(page1.Events[1].Seq), Limit: 2})
	if err != nil {
		t.Fatalf("history page2: %v", err)
	}
	if len(page2.Events) != 2 || !page2.HasMore || page2.Events[0].AggregID != "s1" {
		t.Fatalf("page2 不符: %+v", page2)
	}
	tail, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(page2.Events[1].Seq), Limit: 2})
	if err != nil {
		t.Fatalf("history tail: %v", err)
	}
	if len(tail.Events) != 1 || tail.HasMore {
		t.Fatalf("tail 不符: %+v", tail)
	}

	// C2：before 向前分页——从末尾往回翻，升序返回，has_more 指向「更早还有」。
	lastPage, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{Before: ptr64(tail.Events[0].Seq), Limit: 2})
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
	oldest, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{Before: ptr64(lastPage.FirstSeq), Limit: 10})
	if err != nil {
		t.Fatalf("before 到底: %v", err)
	}
	if len(oldest.Events) != 2 || oldest.HasMore || oldest.FirstSeq != 0 {
		t.Fatalf("翻到最早一页应 has_more=false 且首条 seq=0（v21 起最早一条是 0）: %+v", oldest)
	}
	// 互斥游标 → 40001（服务层兜底）。`After: 0` 也要算"给了游标"——它曾经是"没给"的哨兵，
	// 现在它是合法游标，拿零值当"未设置"会让 after+before 同时存在时静默按 before 处理。
	if _, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(0), Before: ptr64(2)}); err == nil {
		t.Fatal("after+before 应报错")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != 40001 {
			t.Fatalf("期望 40001，got %v", err)
		}
	}
	if _, err := historySvc(svc).SessionHistory(context.Background(), "ghost", dtosession.HistoryQuery{Limit: 10}); err == nil {
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

// TestSessionHistoryStoreBacked sqlite 模式（C2）：历史读 events 表而非 eventbus——
// 双向游标（after/before）、has_more 沿方向、首尾 seq、载荷解码、跨会话隔离。
func TestSessionHistoryStoreBacked(t *testing.T) {
	st := testutil.OpenStore(t, ":memory:")
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	if err := st.UpsertSession(context.Background(), sessionrow.Item{
		ID: "s1", Status: "in_progress", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	svc, _, _ := newTestRig(t)
	enableStore(svc, st)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})
	for i := 0; i < 5; i++ {
		svc.sessionPlane.Publish(session.BusMessage, "s1", map[string]any{"msg_id": fmt.Sprintf("m%d", i), "text": fmt.Sprintf("m%d", i)})
	}
	svc.sessionPlane.Publish(session.BusMessage, "s2", map[string]any{"text": "other"})
	svc.sessionPlane.FlushChunks()

	// 向后分页（after）。
	p1, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(-1), Limit: 3})
	if err != nil {
		t.Fatalf("store history p1: %v", err)
	}
	if len(p1.Events) != 3 || !p1.HasMore || p1.FirstSeq != 0 || p1.LastSeq != p1.Events[2].Seq {
		t.Fatalf("store 首页不符（首条应为位次 0）: %+v", p1)
	}
	// `After: 0` = "位次 0 **之后**"：跳过第 0 条。这条把 v21 的哨兵改动钉在服务层上
	// ——零值不再等于"从头"，忘写 -1 的调用方会静默丢掉该会话的第一条事件。
	skip0, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(0), Limit: 1})
	if err != nil {
		t.Fatalf("store history after=0: %v", err)
	}
	if len(skip0.Events) != 1 || skip0.Events[0].Seq != 1 {
		t.Fatalf("After=0 应跳过位次 0: %+v", skip0)
	}
	if m, ok := p1.Events[0].Payload.(map[string]any); !ok || m["text"] != "m0" {
		t.Fatalf("载荷未解码: %#v", p1.Events[0].Payload)
	}
	p2, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{After: ptr64(p1.LastSeq), Limit: 3})
	if err != nil {
		t.Fatalf("store history p2: %v", err)
	}
	if len(p2.Events) != 2 || p2.HasMore {
		t.Fatalf("store 末页不符: %+v", p2)
	}

	// 向前分页（before）：从末页首条往回翻。
	back, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{Before: ptr64(p2.FirstSeq), Limit: 2})
	if err != nil {
		t.Fatalf("store history before: %v", err)
	}
	if len(back.Events) != 2 || !back.HasMore {
		t.Fatalf("before 页不符: %+v", back)
	}
	if back.LastSeq >= p2.FirstSeq {
		t.Fatalf("before 页应早于游标: last=%d cursor=%d", back.LastSeq, p2.FirstSeq)
	}
	back0, err := historySvc(svc).SessionHistory(context.Background(), "s1", dtosession.HistoryQuery{Before: ptr64(back.FirstSeq), Limit: 5})
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
