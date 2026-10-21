package session

import "fmt"

// HandoffMemo 把会话历史压成 bounded 摘要（§5.6/§18 F3，跨 runtime / 无原生 resume 兜底）。
// 数据源：eventbus（sqlite 模式重启由 store 回灌 → 恢复后全量可用）；纯 memory 模式进程重启
// 无历史保留 → 空摘要（best-effort，不臆造内容）。
// 压缩：用户输入原样收录、助手 message 增量按用户轮聚合、去掉 thought/tool/usage/权限噪音；
// 预算 maxChars（字符）由 MATE_HANDOFF_MAX_CHARS 控制，超出时丢弃最旧轮次（保最新上下文）。
//
// **截断必须可回读**（T6）：丢掉的部分不是"没了"，而是"在别处还看得见"。所以只要丢了任何东西
// （整轮被丢 / 某轮正文被裁），memo 末尾就带一行提示，写明**丢了什么**（多少轮、哪段位次）与
// **去哪儿找**（会话 id + `since_seq` 锚点 + 读面路径）。原先的截断是**完全静默**的：模型只看到
// 更短的上下文，人只看到"它怎么忘了前面说的话"，而两端都无从知道这是有损压缩。
//
// P3b-2b：随会话域搬进来（它读的是**本域的事件面** `pl.bus`，此前挂在 manager 的 Service 上
// 只是为了借用同一个 bus 引用）。调用点（`ensureSession` 的原生恢复兜底）改经会话域调用。
func (pl *Plane) HandoffMemo(sessionID string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	all := pl.bus.EventsAfter(sessionID, -1)
	// 先按「用户轮」聚合：user_message 开新段，其后 message 增量连续拼接为助手段。
	// 每段记下**该轮首条事件的位次**——截断提示里的"去哪儿找"要用它当锚点。
	var segs []handoffSeg
	cur := -1
	for _, evt := range all {
		m, ok := evt.Payload.(map[string]any)
		if !ok {
			continue
		}
		txt, _ := m["text"].(string)
		if txt == "" {
			continue
		}
		switch evt.Type {
		case BusUserMessage:
			segs = append(segs, handoffSeg{user: txt, seq: evt.Seq})
			cur = len(segs) - 1
		case BusMessage:
			if cur < 0 {
				segs = append(segs, handoffSeg{seq: evt.Seq})
				cur = len(segs) - 1
			}
			segs[cur].asst += txt
		}
	}
	if len(segs) == 0 {
		return ""
	}
	// 从最新轮反向收取，直到预算耗尽（丢弃最旧轮）。header 也计入预算；每条含行标签开销
	// （"用户："/"\n助手："/"\n" 共 8 字符）一并计费，预算过小放不下内容时只剩 header 片段
	// （rune 安全，不劈断多字节字符，绝不超过 maxChars）。
	//
	// 两趟：先按**全额预算**收一遍看有没有丢东西；丢了就把提示行的位置留出来重收。
	// 一趟写完再裁剪不行——提示行会被一起裁掉，而那正是"信息丢失看起来像正常"的来源。
	//
	// 预留量取"最坏情况那条提示"的长度（丢满全部轮次），因此重收之后的实际提示一定装得下；
	// 预算小到连 header + 提示都放不下时**保提示**（它是"这里被裁过"的唯一证据），
	// 正文退化成没有——极端预算下的正确行为是"告诉你去哪儿找"，而不是"给你一段看起来完整的残片"。
	body, dropped, trimmed := buildMemoBody(segs, maxChars-runeLen(handoffHeader))
	out := handoffHeader + body
	if dropped > 0 || trimmed {
		reserve := runeLen(handoffTruncationNotice(sessionID, segs, len(segs), true))
		body, dropped, trimmed = buildMemoBody(segs, maxChars-runeLen(handoffHeader)-reserve-1)
		out = handoffHeader + body
		notice := handoffTruncationNotice(sessionID, segs, dropped, trimmed)
		if room := maxChars - runeLen(out) - runeLen("\n"); runeLen(notice) > room {
			// 装不下完整提示（预算极小）：把提示本身截到剩余空间——仍然包含"已截断"这段前缀。
			notice = truncateRunes(notice, room)
		}
		if notice != "" {
			out += "\n" + notice
		}
	}
	return truncateRunes(out, maxChars)
}

// handoffHeader memo 的首行（预算计费，改它要连测试一起改）。
const handoffHeader = "【会话历史摘要（自动生成的有损压缩，用于跨会话/跨底座续聊的上下文回填）】\n"

// handoffTruncationNotice 截断提示（T6 的判据：**必须带可回读定位**）。
//
// 三样缺一不可，而且都要短（它自己也在预算里）：
//   - **丢了什么**：整轮数量、或"有一轮正文被裁剪"；
//   - **锚点**：保留的最近内容从哪个 `seq` 起（`seq` 是会话内位次，最精确的可回读坐标）；
//   - **去哪儿找**：`GET /sessions/<id>/history?since_seq=0`（两种持久层模式都读得回来）。
//
// 只说"（已截断）"等于信息丢失：模型不知道少了什么，人不知道该去哪儿补——那正是本条要消灭的。
func handoffTruncationNotice(sessionID string, segs []handoffSeg, droppedTurns int, trimmedText bool) string {
	firstSeq := int64(0)
	if i := len(segs) - droppedTurns; i >= 0 && i < len(segs) {
		firstSeq = segs[i].seq
	}
	switch {
	case droppedTurns > 0 && trimmedText:
		return fmt.Sprintf("【已截断：更早 %d 轮未收入、且有一轮正文被裁剪；内容从 seq=%d 起。完整历史：GET /sessions/%s/history?since_seq=0】",
			droppedTurns, firstSeq, sessionID)
	case droppedTurns > 0:
		return fmt.Sprintf("【已截断：更早 %d 轮未收入；内容从 seq=%d 起。完整历史：GET /sessions/%s/history?since_seq=0】",
			droppedTurns, firstSeq, sessionID)
	default:
		return fmt.Sprintf("【已截断：有一轮正文被裁剪；内容从 seq=%d 起。完整历史：GET /sessions/%s/history?since_seq=0】",
			firstSeq, sessionID)
	}
}

// handoffSeg memo 的一个"用户轮"（用户原文 + 其后的助手文本 + 该轮首条事件的位次）。
type handoffSeg struct {
	user string
	asst string
	seq  int64
}

// buildMemoBody 按预算反向收取（保最新），返回正文、被丢弃的整轮数与"有轮次正文被裁"标志。
func buildMemoBody(segs []handoffSeg, budget int) (string, int, bool) {
	const partOverhead = 8
	remain := budget
	var parts []string
	kept := 0
	trimmed := false
	for i := len(segs) - 1; i >= 0 && remain > partOverhead; i-- {
		room := remain - partOverhead
		u := truncateRunes(segs[i].user, room)
		room -= runeLen(u)
		a := truncateRunes(segs[i].asst, room)
		if runeLen(u) < runeLen(segs[i].user) || runeLen(a) < runeLen(segs[i].asst) {
			trimmed = true
		}
		parts = append(parts, "用户："+u+"\n助手："+a+"\n")
		remain -= partOverhead + runeLen(u) + runeLen(a)
		kept++
	}
	body := ""
	for i := len(parts) - 1; i >= 0; i-- {
		body += parts[i]
	}
	return body, len(segs) - kept, trimmed
}

// runeLen 返回字符串的 rune 数（预算按字符计）。
func runeLen(s string) int { return len([]rune(s)) }

// truncateRunes 按 rune 数截断（不会劈断多字节 UTF-8 字符）。
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}
