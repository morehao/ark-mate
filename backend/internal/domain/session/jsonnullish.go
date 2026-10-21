package session

import (
	"encoding/json"
	"strings"
)

// JSONNullish 该原始 JSON 是否"没带"（空 / null）。
//
// 导出是因为会话写面（仍在 manager）也用它判"这个已下线的键带没带"——实现只有这一处。
func JSONNullish(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}
