package manager

import (
	"encoding/json"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtousage"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— 已下线键的**显式拒绝** ———
//
// 纪律（与 `rejectRuntimeSelection`/`rejectDerivedDefaultModel`/`rejectRuntimeHintInPatch` 同源）：
// JSON 绑定默认**丢弃未知键**，所以"把字段从 DTO 里删掉"并不等于"用户带了这个键会被拦下"——
// 它会让那个键变成「写了、存了、什么都不发生」的骗人字段（业务风险 B1）。凡是**平台上不再存在
// 的可选面**，都要留一个同名 raw 字段，出现即 40001 + 指路。`null` 视为"没带"。

// rejectRemovedAgentSelection 显式拒绝会话写面里的 `agent_id`（v19：agent 概念整个并入 runtime）。
//
// 为什么宁可报错也不静默忽略：底座由**构建期固定**（`pkg/runtime/build.go`，用户不可自定义），
// 而 JSON 绑定会默默收下 `{"agent_id":"x"}` —— 用户会以为"我给这个会话指定了 agent"，实际上
// 那个键从未被读过。指路的方向也写在文案里：换底座要重新构建（`-X pkg/runtime.BuildRuntime=<id>`）。
func rejectRemovedAgentSelection(agentID json.RawMessage) error {
	if jsonNullish(agentID) {
		return nil
	}
	return acl.NewError(acl.ClassRequest, "agent_selection_removed",
		"agent 概念已并入 runtime：底座由构建期固定（本机为 "+runtime.SelectedRuntime()+"），"+
			"会话不再有 `agent_id`；换底座要重新构建（-X pkg/runtime.BuildRuntime=<id>）", nil)
}

// rejectRemovedUsageDimension 显式拒绝用量读面里的已下线查询键 `agent_id`（v19：会话身份只剩
// `runtime_id`，而底座由构建期固定 ⇒ 按底座聚合只有一桶，这个维度整体下线）。
//
// 为什么宁可报错也不静默忽略：静默忽略的后果**不是"少了一个维度"**，而是"用户以为那次筛选发生过"。
// 用法面的分组只有 day（`group_by=agent` 同理由 `usage.go` 的 `invalid_usage_group_by` 拦住）。
//
// 判据放在本文件而不是 `usage.go`：后者属「会话身份只剩 runtime_id」的结构判据扫描面
// （`TestSessionIdentityIsRuntimeOnly`），那里连一个被撤掉的标识符都不许写。
func rejectRemovedUsageDimension(q dtousage.UsageQueryReq) error {
	if strings.TrimSpace(q.AgentID) == "" {
		return nil
	}
	return acl.NewError(acl.ClassRequest, "agent_selection_removed",
		"[agent_selection_removed] 用量面没有按底座筛选这个维度：底座由构建期固定（本机为 "+
			runtime.SelectedRuntime()+"），`agent_id` 已下线；要按时间看用 group_by=day", nil)
}

// jsonNullish 该原始 JSON 是否"没带"（空 / null）。
func jsonNullish(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}
