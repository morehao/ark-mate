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

// ——— T6：截断必须**可回读**（丢了什么 + 锚点 + 去哪儿找） ———
//
// 原先的截断是完全静默的：丢掉最旧轮、按需裁某轮正文，输出里一个字都不说。后果是双盲——
// 模型只看到更短的上下文（它以为自己看到的就是全部），人只看到"它怎么忘了前面说的话"，
// 而两端都不知道这是有损压缩、更不知道该去哪儿补。

// TestHandoffMemoTruncationIsLocatable 预算不够时必须写出三样：丢了什么、位次锚点、读面路径。
func TestHandoffMemoTruncationIsLocatable(t *testing.T) {
	pl, _ := testPlane(t, "")
	// 正文写长一点：短句子在任何合理的预算下都放得下，那样证明不了截断。
	long := strings.Repeat("这一段是较早的上下文细节。", 12)
	pl.Publish(BusUserMessage, "cv9", map[string]any{"text": "第一轮：" + long})
	pl.Publish(BusMessage, "cv9", map[string]any{"text": "改好了"})
	pl.Publish(BusUserMessage, "cv9", map[string]any{"text": "第二轮：" + long})
	pl.Publish(BusMessage, "cv9", map[string]any{"text": "测试补上"})
	pl.Publish(BusUserMessage, "cv9", map[string]any{"text": "第三轮：" + long})
	pl.Publish(BusMessage, "cv9", map[string]any{"text": "验收通过"})
	pl.FlushChunks()

	// 预算：放得下最新一轮 + 提示行，放不下三轮。
	const budget = 400
	memo := pl.HandoffMemo("cv9", budget)
	if runeLen(memo) > budget {
		t.Fatalf("memo 超预算：%d > %d", runeLen(memo), budget)
	}
	if !strings.Contains(memo, "第三轮") {
		t.Fatalf("最新一轮必须留着（丢最旧、保最新）：%s", memo)
	}
	if strings.Contains(memo, "第一轮") {
		t.Fatalf("这份预算下最旧一轮必须被丢弃（丢最旧、保最新）：\n%s", memo)
	}
	if !strings.Contains(memo, "更早 1 轮未收入") {
		t.Fatalf("提示要说清丢了几轮：\n%s", memo)
	}
	for _, want := range []string{
		"已截断",                                   // ① 说清"有损"
		"轮未收入",                                  // ② 说清丢了多少
		"seq=",                                  // ③ 锚点：被保留内容的位次起点
		"GET /sessions/cv9/history?since_seq=0", // ④ 去哪儿找（完整路径）
	} {
		if !strings.Contains(memo, want) {
			t.Fatalf("截断提示应含 %q（可回读定位），got:\n%s", want, memo)
		}
	}
}

// TestHandoffMemoNoNoticeWhenItFits 没丢东西就不该出现"已截断"（否则模型会以为上下文不全）。
func TestHandoffMemoNoNoticeWhenItFits(t *testing.T) {
	pl, _ := testPlane(t, "")
	pl.Publish(BusUserMessage, "cv8", map[string]any{"text": "问一句"})
	pl.Publish(BusMessage, "cv8", map[string]any{"text": "答一句"})
	pl.FlushChunks()

	memo := pl.HandoffMemo("cv8", 6000)
	if strings.Contains(memo, "已截断") || strings.Contains(memo, "since_seq") {
		t.Fatalf("没截断就不该有截断提示：%s", memo)
	}
	// 反面同样钉住：正文被裁（放不下整段）也要说——这条走"只有一轮、正文超预算"的形态。
	long := strings.Repeat("这一轮正文很长。", 20)
	pl.Publish(BusUserMessage, "cv8b", map[string]any{"text": long})
	pl.FlushChunks()
	tight := pl.HandoffMemo("cv8b", 200)
	if !strings.Contains(tight, "已截断") {
		t.Fatalf("正文被裁也必须提示（静默裁剪等于信息丢失）：%s", tight)
	}
	if runeLen(tight) > 200 {
		t.Fatalf("紧预算下不得超预算：%d > 200", runeLen(tight))
	}
}
