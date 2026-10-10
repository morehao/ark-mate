package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/glog"
)

// ——— M6-8 / P-13：agent 写面之后的能力面变更通知 ———
//
// 问题（P-13 的原话）：入 DB 只解决"读到的是新的"，不解决"**何时**生效"。会话的能力面是在
// `connectConversation` 那一刻注入的（`McpServersFor(agent, spec, sel)`），此后一条活着的句柄
// 一直用着**它出生时**那一套；而钉住能力选择的会话靠代际机制在轮次边界自愈（`generation.go`），
// **缺省路径**（未声明能力选择）的指纹恒为空、按 R11 不迁移（`capability_fingerprint.go:97-101`）。
// 于是"改 agent 白名单"若什么都不做，只有**新建会话**看得见。
//
// 做法：写面提交成功后，把该 agent 的**空闲**会话句柄作废——下一轮 `ensureConversation` 按当前
// 定义重建（native resume / handoff memo 与"进程死后重建"是同一条路径），MCP 注入随之取到新值。
//
// 为什么是"作废句柄"而不是"关进程/换代际键"：
//
//   - **不动代际键、不破 R11**：缺省路径的键仍是基础键，进程可以继续被复用（句柄作废 ≠ 连接关闭），
//     同配置的其它会话/agent 一个字节都不受影响；真正需要"内容寻址"的选定路径照旧走代际。
//   - **不打断在途轮次**：有在途轮次、或有未应答的权限/提问的会话**顺延**（与 ADR-13 纪律③
//     逐条同判据）——它们这一轮继续用旧面跑完，下一次重建才换（这是协议内能做到的最细粒度：
//     ACP 没有"给活会话换能力"的通道）。
//   - **不杀进程**：重建仍在原连接上开会话（`acquire` 命中同一代际），代价只是一次
//     session/new|load 往返，与"进程空闲回收后第一轮"的代价同量级。
//
// 失败面：通知本身**不返回错误**。它已经在载体提交**之后**（定义已经是事实），此时报错只会让
// 用户以为"没保存成功"；真实情况是"定义生效了，但某几条会话要等下一轮"——那是日志与
// `deferred` 计数要回答的事，不是一次失败的写入。

// agentMcpsChanged 两次定义的白名单是否**语义上**不同（用于决定要不要发通知）。
//
// 判据是"解析出来的集合"而不是"指针/切片"，三个理由：
//
//  1. `Mcps` 是**过滤器**（`mcpSelectionFor` 按 id 过滤注册表，注册表本身按 id 排序），
//     所以顺序不影响任何注入结果——重排一次不该把用户的会话全部作废；
//  2. 重复项同理（`["a","a"]` 与 `["a"]` 注入同一套）；
//  3. `nil`（未声明 = 全量）与 `[]`（空白名单 = 一个都不要）**语义相反**，必须判为不同。
func agentMcpsChanged(before, after *[]string) bool {
	if (before == nil) != (after == nil) {
		return true
	}
	if before == nil {
		return false
	}
	return !sameStringSet(*before, *after)
}

// sameStringSet 两个字符串切片作为**集合**是否相等（忽略顺序与重复）。
func sameStringSet(a, b []string) bool {
	sa := dedupeStrings(append([]string(nil), a...))
	sb := dedupeStrings(append([]string(nil), b...))
	if len(sa) != len(sb) {
		return false
	}
	sort.Strings(sa)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// capabilityFaceFieldMcps 通知里记的"是哪个字段变了"（日志/诊断用，避免只看到一句无主语的话）。
const capabilityFaceFieldMcps = "mcps"

// notifyAgentCapabilityChange 写面之后的能力面变更通知：让该 agent 的**空闲**会话下一轮按当前
// 定义重建。返回（作废数, 顺延数）——调用方只把它记进日志/响应，不改变成功与否。
//
// `orch == nil`（纯列表模式、部分单测）时静默无操作：没有编排器就没有会话句柄可作废，
// 不是错误。
func (s *Service) notifyAgentCapabilityChange(agentID, field string) (evicted, deferred int) {
	if s.orch == nil || agentID == "" {
		return 0, 0
	}
	evicted, deferred = s.orch.evictIdleCapabilityHandles(agentID, field)
	glog.Infow(context.Background(), "能力面变更：空闲会话句柄已作废（下一轮按当前定义重建）",
		"agent", agentID, "field", field, "evicted", evicted, "deferred", deferred)
	return evicted, deferred
}

// decodeAgentMcpsPatch 解析 PATCH 里的 `mcps`（三态，见 `dtoagent.AgentUpdateReq.Mcps`）。
//
// 返回值 `present=false` = 键未出现（不改）；`value` 为 nil = 清空白名单（回落未声明）。
// 类型不对（对象/数字/含非字符串元素）一律 40000 `invalid_param`——请求形态问题改参数就好，
// 该在写面挡住，而不是落一个"看起来存进去了、注入时才发现"的值（与 skill 写面的
// `capWriteErrParam` 同一个码与同一条分线：形态 40000 / 取值语义 40001）。
func decodeAgentMcpsPatch(raw []byte) (value *[]string, present bool, err error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	var list *[]string
	if e := json.Unmarshal(raw, &list); e != nil {
		return nil, false, code.Error(code.InvalidParam,
			fmt.Sprintf("mcps 须为字符串数组或 null：%v", e))
	}
	if list == nil {
		return nil, true, nil // null → 清空白名单
	}
	for _, id := range *list {
		if id == "" {
			return nil, false, code.Error(code.InvalidParam,
				"mcps 里不得出现空字符串（要表达「一个都不要」请传 []）")
		}
	}
	// 去重保序：白名单是过滤器，重复项没有意义，归一之后 `["a","a"]` 与 `["a"]` 在载体里逐字节
	// 相同（否则"语义没变、字节变了"会让下一次比较误判）。
	//
	// **空入参要显式落成非 nil 空切片**：`dedupeStrings` 的 `in[:0]` 对 nil 切片仍返回 nil，
	// 而 `mcps: null` 与 `mcps: []` 在定义里是**两个意思**（未声明 vs 禁用全部）——指针非空、
	// 底层切片 nil 会让载体写出 `null`，把用户的"一个都不要"悄悄变成"全都要"。
	uniq := dedupeStrings(append([]string(nil), *list...))
	if uniq == nil {
		uniq = []string{}
	}
	return &uniq, true, nil
}
