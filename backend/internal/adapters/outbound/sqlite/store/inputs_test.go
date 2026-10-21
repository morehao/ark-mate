package store

import (
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session/queue"
)

// TestInputQueueSchemaAndClaim §6 队列的持久层契约：v6 迁移建表、插入-取队首-认领，
// 且**认领是条件更新**（第二条路径拿不到）；已投递/已作废是台账行，不可删。
func TestInputQueueSchemaAndClaim(t *testing.T) {
	st := openTestDB(t)
	ctx := ctxT()

	ver, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读 schema 版本失败: %v", err)
	}
	if ver < 6 {
		t.Fatalf("inputs 表要求 schema ≥ 6，得到 %d", ver)
	}
	seedSession(t, st, "c1")

	base := time.Now().UTC()
	rows := []queue.Item{
		{ID: "i2", SessionID: "c1", Text: "第二条", Delivery: "after_run",
			TargetRunID: "t1", Generation: 1, Status: queue.StatusQueued, CreatedAt: base.Add(time.Second)},
		{ID: "i1", SessionID: "c1", Text: "第一条", Delivery: "after_run",
			TargetRunID: "t1", Generation: 1, Status: queue.StatusQueued, CreatedAt: base},
	}
	for _, r := range rows {
		if err := st.CreateInput(ctx, r); err != nil {
			t.Fatalf("写入队列项失败: %v", err)
		}
	}

	// 队首 = **会话内位次最小**（v22 起队列顺序由 `seq` 承担，入队即取号）。
	//
	// 这里的夹具刻意反着来（先插 created_at 较晚的 i2）：那时 `seq` 才是"排第几"的权威，
	// 而按 created_at 排要靠 id 兜底同刻——"队列自己的顺序"不该由两个不相关的列拼出来。
	// 真实入队路径里 `CreatedAt` 就是插入时刻，两种口径同序（只有时钟回拨才会分叉）。
	head, err := st.HeadQueuedInput(ctx, "c1")
	if err != nil || head == nil {
		t.Fatalf("取队首失败: %v/%v", head, err)
	}
	if head.ID != "i2" || head.Seq != 0 {
		t.Fatalf("队首应为先入队的 i2（seq=0），得到 %+v", head)
	}
	queued, err := st.ListQueuedInputs(ctx, "c1")
	if err != nil || len(queued) != 2 || queued[0].ID != "i2" || queued[1].ID != "i1" || queued[1].Seq != 1 {
		t.Fatalf("队列应按 seq 升序且位次从 0 起: %+v err=%v", queued, err)
	}
	// 认领成功一次；同一 id 再认领必须失败（条件更新是并发下的唯一仲裁者）。
	ok, err := st.ClaimQueuedInput(ctx, "i1", time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("首次认领应成功: %v/%v", ok, err)
	}
	if ok, err = st.ClaimQueuedInput(ctx, "i1", time.Now().UTC()); err != nil || ok {
		t.Fatalf("重复认领必须失败: %v/%v", ok, err)
	}
	// 已投递的项不可删（台账行），待投递的项可删。
	if ok, err = st.DeleteQueuedInput(ctx, "c1", "i1"); err != nil || ok {
		t.Fatalf("已投递项不可删: %v/%v", ok, err)
	}
	if ok, err = st.DeleteQueuedInput(ctx, "c1", "i2"); err != nil || !ok {
		t.Fatalf("待投递项应可删: %v/%v", ok, err)
	}
	// 跨会话删：同 id 不同会话不命中。
	if err := st.CreateInput(ctx, queue.Item{ID: "i3", SessionID: "c1", Text: "第三条",
		Delivery: "interrupt_then_prompt", Generation: 1,
		Status: queue.StatusQueued, CreatedAt: base.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if ok, err = st.DeleteQueuedInput(ctx, "c-other", "i3"); err != nil || ok {
		t.Fatalf("跨会话删必须不命中: %v/%v", ok, err)
	}
	// ListAllQueuedInputs 跨会话取快照（启动恢复靠它逐条发收敛事件，故不按会话过滤）。
	if err := st.CreateInput(ctx, queue.Item{ID: "i4", SessionID: "c1", Text: "会话一的待投递",
		Delivery: "after_run", Generation: 1,
		Status: queue.StatusQueued, CreatedAt: base.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	seedSession(t, st, "c2")
	if err := st.CreateInput(ctx, queue.Item{ID: "i5", SessionID: "c2", Text: "会话二的待投递",
		Delivery: "after_run", Generation: 1,
		Status: queue.StatusQueued, CreatedAt: base.Add(4 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListAllQueuedInputs(ctx)
	if err != nil {
		t.Fatalf("跨会话列待投递失败: %v", err)
	}
	// i1 已投递、i2 已删、i3 因跨会话删不命中仍在队、i4/i5 新入队 → 共 3 条，且两个会话都在。
	// 排序是 `(session_id, seq)`（v22 起 seq 是队列自己的顺序，跨会话快照不再有时钟参与）。
	wantIDs := []string{"i3", "i4", "i5"}
	if len(all) != len(wantIDs) {
		t.Fatalf("应剩 %d 条待投递（i1 已投递 / i2 已删 / i3 跨会话删不命中）: %+v", len(wantIDs), all)
	}
	for i, id := range wantIDs {
		if all[i].ID != id {
			t.Fatalf("第 %d 条应为 %s（会话内位次升序）: %+v", i, id, all)
		}
	}
	if all[2].SessionID != "c2" {
		t.Fatalf("跨会话快照应带 c2 的行: %+v", all[2])
	}

	// 启动作废：把剩余 queued 全置 stale（幂等，第二次 0 行）。
	if n, err := st.StaleAllQueuedInputs(ctx); err != nil || n != 3 {
		t.Fatalf("应作废 3 行（i3+i4+i5，跨会话）: %d/%v", n, err)
	}
	if n, err := st.StaleAllQueuedInputs(ctx); err != nil || n != 0 {
		t.Fatalf("再次作废应为 0 行（幂等）: %d/%v", n, err)
	}
	if left, err := st.ListQueuedInputs(ctx, "c1"); err != nil || len(left) != 0 {
		t.Fatalf("作废后待投递集合应为空: %v/%v", left, err)
	}
}
