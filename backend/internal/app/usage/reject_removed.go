package usage

import (
	"encoding/json"
	"github.com/morehao/ark-mate/internal/contract/dtousage"
	"strings"

	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 已下线的查询键：**显式拒绝**（会话域这一半） ———
//
// 纪律：JSON 绑定默认**丢弃未知键**，所以"把字段从 DTO 里删掉"并不等于"用户带了这个键会被拦下"
// ——它会让那个键变成「写了、存了、什么都不发生」的骗人字段（业务风险 B1）。凡是平台上不再存在的
// 可选面，都要留一个同名 raw 字段，出现即 40001 + 指路。`null` 视为"没带"。
//
// 为什么单独成文件而不是并进 `usage.go`：后者是 `TestSessionIdentityIsRuntimeOnly` 的扫描面，
// 那里**连一个被撤掉的标识符都不许写**（判据要证明的正是"那个词已经不在了"，作者写出来会把
// 判据自己顶掉）。
func rejectRemovedUsageDimension(q dtousage.UsageQueryReq) error {
	if strings.TrimSpace(q.AgentID) == "" {
		return nil
	}
	return code.RequestError("agent_selection_removed", "[agent_selection_removed] 用量面没有按底座筛选这个维度：底座由构建期固定（本机为 "+
		runtime.SelectedRuntime()+"），`agent_id` 已下线；要按时间看用 group_by=day")
}

// JSONNullish 该原始 JSON 是否"没带"（空 / null）。
//
// 导出是因为会话写面（仍在 manager）也用它判"这个已下线的键带没带"——实现只有这一处。
func JSONNullish(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}
