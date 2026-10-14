package manager

// HandoffMemo 把会话历史压成 bounded 摘要（§5.6/§18 F3，跨 runtime / 无原生 resume 兜底）。
// 数据源：eventbus（sqlite 模式重启由 store 回灌 → 恢复后全量可用）；纯 memory 模式进程重启
// 无历史保留 → 空摘要（best-effort，不臆造内容）。
// 压缩：用户输入原样收录、助手 message 增量按用户轮聚合、去掉 thought/tool/usage/权限噪音；
// 预算 maxChars（字符）由 MATE_HANDOFF_MAX_CHARS 控制，超出时丢弃最旧轮次（保最新上下文）。
func (s *Service) HandoffMemo(sessionID string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	all := s.bus.EventsAfter(sessionID, -1)
	// 先按「用户轮」聚合：user_message 开新段，其后 message 增量连续拼接为助手段。
	type seg struct {
		user string
		asst string
	}
	var segs []seg
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
			segs = append(segs, seg{user: txt})
			cur = len(segs) - 1
		case BusMessage:
			if cur < 0 {
				segs = append(segs, seg{})
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
	const partOverhead = 8
	header := "【会话历史摘要（自动生成的有损压缩，用于跨会话/跨底座续聊的上下文回填）】\n"
	remain := maxChars - runeLen(header)
	var parts []string
	for i := len(segs) - 1; i >= 0 && remain > partOverhead; i-- {
		room := remain - partOverhead
		u := truncateRunes(segs[i].user, room)
		room -= runeLen(u)
		a := truncateRunes(segs[i].asst, room)
		parts = append(parts, "用户："+u+"\n助手："+a+"\n")
		remain -= partOverhead + runeLen(u) + runeLen(a)
	}
	// 反向收取后翻转回时间正序。
	out := header
	for i := len(parts) - 1; i >= 0; i-- {
		out += parts[i]
	}
	return truncateRunes(out, maxChars)
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
