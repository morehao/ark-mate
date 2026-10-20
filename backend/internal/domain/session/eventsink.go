package session

import (
	"context"

	"github.com/morehao/ark-mate/internal/core/object/objusage"
	"github.com/morehao/ark-mate/internal/domain/session/event"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/golib/glog"
)

// ——— 运行期信号的**出口**（P4a 正文第五十五刀：从 `internal/manager` 搬入）———
//
// 底座回传的归一化信号（`event.Signal`，由 ACP 适配器翻译，见 `sessionruntime/event_bridge.go`）
// 在这里翻译成平台事件（`Bus*`）并发布：
//
//	① 双 id 解析：底层事件只带原生 session id，先经 `Handles.ResolveProvider` 换成平台 uuid；
//	   断连后迟到事件（映射已清）丢弃并留 debug——不落进孤儿聚合。
//	② 过程状态：消息/思考/工具/用量各自先发对应的 `run.status`（同状态连发由域内去重键吞掉）。
//	③ 工具失败归类：来自底座的签名表（第四张可执行端口表）；不认识就**不写** `fail_reason`。
//
// 纯翻译 + 两个旁路写口（工具记忆 / 模型目录缓存），不持有轮次状态——轮次状态在 `Runner`。

// EventSink 把运行期信号转为域事件进 eventbus（agg = 平台会话 uuid）。
//
// 入参是**领域语言**（`event.Signal`）：底座协议类型的翻译发生在适配器边界，本层不认识它。
func (r *Runner) EventSink(runtimeID string) func(event.Signal) {
	return func(ev event.Signal) {
		if ev.SessionID == "" {
			return
		}
		// P3 双 id：底层事件只携带原生 session id，host 侧解析为平台会话 uuid 再发布；
		// 断连后迟到事件（映射已清）不再落入孤儿聚合，丢弃并留 debug。
		cid := r.handles.ResolveProvider(ev.SessionID)
		if cid == "" {
			glog.Debugw(context.Background(), "event 无对应平台会话（映射已清，丢弃迟到事件）", "runtime_session", ev.SessionID)
			return
		}
		ev.SessionID = cid
		typ := ""
		var payload any
		switch ev.Kind {
		case event.KindMessage:
			typ = BusMessage
			// 首个文本增量到 ⇒ 「正在收模型输出」。连发由域里的去重键吞掉（同状态同 tool 只留第一帧）。
			r.RunStatusFor(ev.SessionID, RunStatusModelStreaming, RunStatusOpts{})
			if d, ok := ev.Payload.(event.MessageDelta); ok {
				payload = map[string]any{"session_id": ev.SessionID, "runtime": runtimeID, "text": d.Text}
				if d.MessageID != "" {
					payload.(map[string]any)["msg_id"] = d.MessageID
				}
			}
		case event.KindThought:
			typ = BusThought
			r.RunStatusFor(ev.SessionID, RunStatusModelStreaming, RunStatusOpts{})
			if d, ok := ev.Payload.(event.ThoughtDelta); ok {
				payload = map[string]any{"session_id": ev.SessionID, "runtime": runtimeID, "text": d.Text}
				if d.MessageID != "" {
					payload.(map[string]any)["msg_id"] = d.MessageID
				}
			}
		case event.KindToolCall:
			typ = BusToolCall
			if d, ok := ev.Payload.(event.ToolCall); ok {
				// 权限请求只带 toolCallId（dsh 的 wire 形状）：把详情留到权限裁决那一刻。
				r.rememberToolCall(d)
				// 工具过程（S1）：带 `tool_name` —— 头栏那句「正在执行 Bash」正是它（Q6 选①）。
				// 必须在 publish 工具事件**之前**发：先有「我要跑 X」再有「X 的结果」。
				r.RunStatusFor(ev.SessionID, RunStatusToolExecuting, RunStatusOpts{
					ToolCallID: d.ID, ToolName: d.Title,
				})
				payload = toolPayload(ev.SessionID, runtimeID, d)
			}
		case event.KindToolUpdate:
			typ = BusToolUpdate
			if d, ok := ev.Payload.(event.ToolCall); ok {
				// 终态 update 可能只带 status：先合并（不带 input 不会抹掉命令行），再按终态删除。
				r.rememberToolCall(d)
				if d.Status == event.ToolStatusCompleted || d.Status == event.ToolStatusFailed {
					r.forgetToolCall(d.ID)
				}
				p := toolPayload(ev.SessionID, runtimeID, d)
				// 「这条为什么没成」的归类来自**底座的签名表**（第四张可执行端口表）：
				// 被沙箱拒绝的那一帧 `status` 是 `completed`，只有结果正文说得清。
				//
				// 不认识就**不写这个键**（写一个猜的原因比不写更糟：用户会照着它去修一个不存在的问题）。
				if r.toolFailed != nil {
					if f, ok := r.toolFailed.ClassifyToolFailure(runtimeID, d.ContentText); ok {
						if f.Reason != "" {
							p[ToolPayloadFailReason] = f.Reason
						}
						if f.BizCode != "" {
							p[ToolPayloadBizCode] = f.BizCode
						}
					}
				}
				payload = p
			}
		case event.KindUserMessage:
			// 用户消息以宿主侧 Prompt 提交时发布的 BusUserMessage（Runner.Prompt）为唯一来源；
			// agent 回传的 user_message 只是会话镜像（normalizeUpdate 归一），若再转发会造成
			// 同一条用户输入在流/历史里双入。这里丢弃镜像，不再发布。
			glog.Debugw(context.Background(), "skip agent-echoed user_message (host publishes user input)", "session", ev.SessionID)
		case event.KindUsage:
			typ = BusUsage
			// ev.SessionID 已在上方解析为平台会话 uuid（cid）。
			p := map[string]any{"session_id": ev.SessionID, "runtime": runtimeID}
			if d, ok := ev.Payload.(objusage.UsageInfo); ok {
				p["used"], p["size"] = d.Used, d.Size
				if d.CostAmount > 0 {
					p["cost_amount"], p["cost_currency"] = d.CostAmount, d.CostCurrency
				}
				r.RecordUsage(ev.SessionID, d)
			}
			payload = p
		case event.KindPermission:
			typ = BusPermission
			payload = ev.Payload
		case event.KindConfigOptions:
			// 运行时主动推送全量配置项（dsh 在 provider 拓扑变化时推）：
			// 刷新平台目录缓存并转发给前端，模型下拉与「当前模型」无需轮询即可保持一致。
			if d, ok := ev.Payload.(event.ConfigOptions); ok {
				// 目录缓存的刷新在**适配器边界**做（`Options` 是不透明载荷，域不命名它），
				// 这里只剩对外转发。
				typ = BusConfigOptions
				payload = map[string]any{
					"session_id": ev.SessionID,
					"runtime":    runtimeID,
					"options":    d.Options,
				}
			}
		case event.KindCommands:
			// 运行时主动通告它的可用命令（**这是可选能力**：有的底座的 ACP 面根本不含命令，故永不产生本事件）。
			// 纯转发：平台不缓存、不合并、不做会话快照——会话重开时本事件经 since_seq
			// 全量回放即可重建，故无需 REST 读端点（ADR-17）。
			if d, ok := ev.Payload.(event.Commands); ok {
				typ = BusCommands
				payload = map[string]any{
					"session_id": ev.SessionID,
					"runtime":    runtimeID,
					"commands":   d.Commands,
				}
			}
		}
		if typ != "" && payload != nil {
			r.plane.Publish(typ, ev.SessionID, payload)
		}
	}
}

// rememberToolCall / forgetToolCall 是工具记忆端口的 nil 安全包装（未装配时 no-op）。
func (r *Runner) rememberToolCall(info event.ToolCall) {
	if r.toolMemory != nil {
		r.toolMemory.Remember(info)
	}
}

func (r *Runner) forgetToolCall(id string) {
	if r.toolMemory != nil {
		r.toolMemory.Forget(id)
	}
}

// 工具事件载荷的**白名单键**（S4 / ADR-16 的旁支）。
//
// 这张表是**契约的一半**：另一半是前端 `StreamEntry` 里读的那些键。新增一个只有后端会写的键
// 不会让任何测试变红——事件照常推、界面照常渲染，只是那个键永远没人读（或者下一个人照着它
// 又在前端加一份推断）。由 archtest 的 `TestToolPayloadKeysAreClosed` 两侧一起钉。
const (
	ToolPayloadSessionID  = "session_id"
	ToolPayloadRuntime    = "runtime"
	ToolPayloadToolCallID = "tool_call_id"
	ToolPayloadTitle      = "title"
	ToolPayloadStatus     = "status"
	ToolPayloadKind       = "kind"
	ToolPayloadInput      = "input"
	ToolPayloadOutput     = "output"
	ToolPayloadFailReason = "fail_reason"
	ToolPayloadBizCode    = "biz_code"
)

// ToolPayloadKeys 载荷允许出现的全部键（白名单，顺序即文档顺序）。
var ToolPayloadKeys = []string{
	ToolPayloadSessionID, ToolPayloadRuntime, ToolPayloadToolCallID, ToolPayloadTitle,
	ToolPayloadStatus, ToolPayloadKind, ToolPayloadInput, ToolPayloadOutput,
	ToolPayloadFailReason, ToolPayloadBizCode,
}

// toolPayload 构造工具事件载荷：稳定键 tool_call_id/title/status，可选透传 kind/input/output
// （ACP ToolKind/rawInput/rawOutput，决策 v5②；字段为空/缺省时不出现在载荷中）。
//
// `fail_reason` / `biz_code` 由调用方按底座的签名表补（见 `ToolPayloadFailReason`）：
// 它们**不是** ACP 字段，而是平台从结果正文里读出来的归类。
func toolPayload(sessionID, runtimeID string, d event.ToolCall) map[string]any {
	p := map[string]any{
		ToolPayloadSessionID: sessionID, ToolPayloadRuntime: runtimeID,
		ToolPayloadToolCallID: d.ID, ToolPayloadTitle: d.Title, ToolPayloadStatus: d.Status,
	}
	if d.Kind != "" {
		p[ToolPayloadKind] = d.Kind
	}
	if d.Input != nil {
		p[ToolPayloadInput] = d.Input
	}
	if d.Output != nil {
		p[ToolPayloadOutput] = d.Output
	}
	return p
}

// PublishError 把错误按 acp 分类发布为 run.error（A3：附上 stderr 诊断尾巴，前端错误卡可展开）。
//
// `runID` 是这一轮的身份（v22 起必带）：终态物化要靠它找到开轮行——从 `runs` 里"猜最近一条
// running"只在单会话串行下碰巧成立，而猜错的代价是这一轮的事件窗口算到别的轮上。
func (r *Runner) PublishError(sessionID, runID string, err error, cli *acp.Client) {
	ae := acp.Classify(err)
	tail := ae.StderrTail
	if len(tail) == 0 && cli != nil && r.diagnostics != nil {
		tail = r.diagnostics.StderrTail(cli)
	}
	if r.diagnostics != nil {
		r.diagnostics.RecordRuntimeError(sessionID, ae, tail)
	}
	payload := map[string]any{
		"session_id": sessionID,
		"run_id":     runID,
		"code":       ae.Code,
		"class":      string(ae.Class),
		"details":    ae.Details,
	}
	if len(tail) > 0 {
		payload["stderr_tail"] = tail
	}
	r.plane.Publish(BusRunError, sessionID, payload)
}
