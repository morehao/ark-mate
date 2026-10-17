package session

import (
	"strings"
	"testing"
)

// TestHandoffMemoBudget P3 F3 memo 压缩：用户原文收录 + 助手聚合、跨轮分隔、预算截断保最新。
//
// P3b-2b：随 `HandoffMemo` 搬进本域（此前在 `manager/orchestrator_test.go`，走的是
// `Service.HandoffMemo` 转发）。判据一字未改——变的只是它现在验的是域方法本身。
func TestHandoffMemoBudget(t *testing.T) {
	pl, _ := testPlane(t, "")
	pl.Publish(BusUserMessage, "cv1", map[string]any{"text": "任务A：第一轮要求"})
	pl.Publish(BusMessage, "cv1", map[string]any{"text": "收到A"})
	pl.Publish(BusMessage, "cv1", map[string]any{"text": "补充A"})
	pl.Publish(BusThought, "cv1", map[string]any{"text": "噪音思考"}) // 不入 memo
	pl.Publish(BusUserMessage, "cv1", map[string]any{"text": "任务B：第二轮要求"})
	pl.Publish(BusMessage, "cv1", map[string]any{"text": "收到B"})
	pl.FlushChunks() // ③ 合并缓冲落盘后再算 memo

	memo := pl.HandoffMemo("cv1", 6000)
	for _, want := range []string{"任务A", "任务B", "收到A补充A", "收到B"} {
		if !strings.Contains(memo, want) {
			t.Fatalf("memo 应含 %q，got: %s", want, memo)
		}
	}
	if strings.Contains(memo, "噪音思考") {
		t.Fatalf("memo 不应含 thought 噪音: %s", memo)
	}
	// 预算收紧：小到只能放下 header 片段 → 历史轮次被丢弃、输出不超预算。
	tight := pl.HandoffMemo("cv1", 24)
	if strings.Contains(tight, "任务A") || strings.Contains(tight, "任务B") {
		t.Fatalf("紧预算应丢弃历史轮：%s", tight)
	}
	if runeLen(tight) > 24 {
		t.Fatalf("memo 不应超过预算：%d > 24", runeLen(tight))
	}
	if got := pl.HandoffMemo("cv1", 0); got != "" {
		t.Fatalf("0 预算应返回空: %q", got)
	}
	if got := pl.HandoffMemo("no-such", 6000); got != "" {
		t.Fatalf("无事件会话应返回空: %q", got)
	}
}
