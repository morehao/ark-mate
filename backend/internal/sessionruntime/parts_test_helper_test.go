package sessionruntime

import "github.com/morehao/ark-mate/internal/core/protocol"

// partsText 把 parts 里的 text 块拼起来（P2 单测桩用：断言"发出的文本"）。
func partsText(parts []protocol.PromptPart) string {
	var s string
	for _, p := range parts {
		if p.Kind == protocol.PromptPartText || p.Kind == "" {
			s += p.Text
		}
	}
	return s
}
