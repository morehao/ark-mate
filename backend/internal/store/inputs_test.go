package store

import (
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/model"
)

// TestInputQueueSchemaAndClaim §6 队列的持久层契约：v6 迁移建表、插入-取队首-认领，
// 且**认领是条件更新**（第二条路径拿不到）；已投递/已作废是台账行，不可删。
func TestInputQueueSchemaAndClaim(t *testing.T) {
	st := openMem(t)
	ctx := ctxT()

	ver, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读 schema 版本失败: %v", err)
	}
	if ver < 6 {
		t.Fatalf("inputs 表要求 schema ≥ 6，得到 %d", ver)
	}
	seedConversation(t, st, "c1")

	base := time.Now().UTC()
	rows := []Input{
		{ID: "i2", ConversationID: "c1", Text: "第二条", Delivery: model.InputDeliveryAfterTurn,
			TargetTurnID: "t1", Generation: 1, Status: model.InputStatusQueued, CreatedAt: base.Add(time.Second)},
		{ID: "i1", ConversationID: "c1", Text: "第一条", Delivery: model.InputDeliveryAfterTurn,
			TargetTurnID: "t1", Generation: 1, Status: model.InputStatusQueued, CreatedAt: base},
	}
	for _, r := range rows {
		if err := st.CreateInput(ctx, r); err != nil {
			t.Fatalf("写入队列项失败: %v", err)
		}
	}

	// 队首 = created_at 最早（不是插入顺序）。
	head, err := st.HeadQueuedInput(ctx, "c1")
	if err != nil || head == nil {
		t.Fatalf("取队首失败: %v/%v", head, err)
	}
	if head.ID != "i1" {
		t.Fatalf("队首应为 i1（创建时间最早），得到 %s", head.ID)
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
	if err := st.CreateInput(ctx, Input{ID: "i3", ConversationID: "c1", Text: "第三条",
		Delivery: model.InputDeliveryInterruptThenPrompt, Generation: 1,
		Status: model.InputStatusQueued, CreatedAt: base.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if ok, err = st.DeleteQueuedInput(ctx, "c-other", "i3"); err != nil || ok {
		t.Fatalf("跨会话删必须不命中: %v/%v", ok, err)
	}
	// 启动作废：把剩余 queued 全置 stale（幂等，第二次 0 行）。
	if n, err := st.StaleAllQueuedInputs(ctx); err != nil || n != 1 {
		t.Fatalf("应作废 1 行: %d/%v", n, err)
	}
	if n, err := st.StaleAllQueuedInputs(ctx); err != nil || n != 0 {
		t.Fatalf("再次作废应为 0 行（幂等）: %d/%v", n, err)
	}
	if left, err := st.ListQueuedInputs(ctx, "c1"); err != nil || len(left) != 0 {
		t.Fatalf("作废后待投递集合应为空: %v/%v", left, err)
	}
}
