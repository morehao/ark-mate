package manager

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/runtimeconn"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/object/objusage"
	"github.com/morehao/golib/glog"
)

// eventSink 把 acl 归一化事件转为域事件进 eventbus（agg = 平台会话 uuid）。
func (o *Orchestrator) eventSink(runtimeID string) func(acl.Event) {
	return func(ev acl.Event) {
		if ev.SessionID == "" {
			return
		}
		// P3 双 id：底层事件只携带原生 session id，host 侧解析为平台会话 uuid 再发布；
		// 断连后迟到事件（映射已清）不再落入孤儿聚合，丢弃并留 debug。
		cid := o.resolveProvider(ev.SessionID)
		if cid == "" {
			glog.Debugw(context.Background(), "event 无对应平台会话（映射已清，丢弃迟到事件）", "runtime_session", ev.SessionID)
			return
		}
		ev.SessionID = cid
		typ := ""
		var payload any
		switch ev.Kind {
		case acl.KindMessage:
			typ = session.BusMessage
			// 首个文本增量到 ⇒ 「正在收模型输出」。连发由域里的去重键吞掉（同状态同 tool 只留第一帧）。
			o.runStatusFor(ev.SessionID, session.RunStatusModelStreaming, session.RunStatusOpts{})
			if d, ok := ev.Payload.(acl.MessageDelta); ok {
				payload = map[string]any{"session_id": ev.SessionID, "runtime": runtimeID, "text": d.Text}
				if d.MessageID != "" {
					payload.(map[string]any)["msg_id"] = d.MessageID
				}
			}
		case acl.KindThought:
			typ = session.BusThought
			o.runStatusFor(ev.SessionID, session.RunStatusModelStreaming, session.RunStatusOpts{})
			if d, ok := ev.Payload.(acl.ThoughtDelta); ok {
				payload = map[string]any{"session_id": ev.SessionID, "runtime": runtimeID, "text": d.Text}
				if d.MessageID != "" {
					payload.(map[string]any)["msg_id"] = d.MessageID
				}
			}
		case acl.KindToolCall:
			typ = session.BusToolCall
			if d, ok := ev.Payload.(acl.ToolCallInfo); ok {
				// 工具过程（S1）：带 `tool_name` —— 头栏那句「正在执行 Bash」正是它（Q6 选①）。
				// 必须在 publish 工具事件**之前**发：先有「我要跑 X」再有「X 的结果」。
				o.runStatusFor(ev.SessionID, session.RunStatusToolExecuting, session.RunStatusOpts{
					ToolCallID: d.ID, ToolName: d.Title,
				})
				payload = toolPayload(ev.SessionID, runtimeID, d)
			}
		case acl.KindToolUpdate:
			typ = session.BusToolUpdate
			if d, ok := ev.Payload.(acl.ToolCallInfo); ok {
				p := toolPayload(ev.SessionID, runtimeID, d)
				// 「这条为什么没成」的归类来自**底座的签名表**（第四张可执行端口表）：
				// 被沙箱拒绝的那一帧 `status` 是 `completed`，只有结果正文说得清。
				//
				// 不认识就**不写这个键**（写一个猜的原因比不写更糟：用户会照着它去修一个不存在的问题）。
				if f, ok := o.svc.reg.ClassifyToolFailure(runtimeID, d.ContentText); ok {
					if f.Reason != "" {
						p[ToolPayloadFailReason] = f.Reason
					}
					if f.BizCode != "" {
						p[ToolPayloadBizCode] = f.BizCode
					}
				}
				payload = p
			}
		case acl.KindUserMessage:
			// 用户消息以宿主侧 Prompt 提交时发布的 session.BusUserMessage（orchestrator.Prompt）为唯一来源；
			// agent 回传的 user_message 只是会话镜像（normalizeUpdate 归一），若再转发会造成
			// 同一条用户输入在流/历史里双入。这里丢弃镜像，不再发布。
			glog.Debugw(context.Background(), "skip agent-echoed user_message (host publishes user input)", "session", ev.SessionID)
		case acl.KindUsage:
			typ = session.BusUsage
			// ev.SessionID 已在上方解析为平台会话 uuid（cid）。
			p := map[string]any{"session_id": ev.SessionID, "runtime": runtimeID}
			if d, ok := ev.Payload.(objusage.UsageInfo); ok {
				p["used"], p["size"] = d.Used, d.Size
				if d.CostAmount > 0 {
					p["cost_amount"], p["cost_currency"] = d.CostAmount, d.CostCurrency
				}
				o.recordUsage(ev.SessionID, d)
			}
			payload = p
		case acl.KindPermission:
			typ = session.BusPermission
			payload = ev.Payload
		case acl.KindConfigOptions:
			// 运行时主动推送全量配置项（dsh 在 provider 拓扑变化时推）：
			// 刷新平台目录缓存并转发给前端，模型下拉与「当前模型」无需轮询即可保持一致。
			if d, ok := ev.Payload.(acl.ConfigOptionsInfo); ok {
				if d.SessionID != "" {
					o.svc.catalog.put(runtimeID, d.Options)
				}
				typ = session.BusConfigOptions
				payload = map[string]any{
					"session_id": ev.SessionID,
					"runtime":    runtimeID,
					"options":    d.Options,
				}
			}
		case acl.KindCommands:
			// 运行时主动通告它的可用命令（**这是可选能力**：有的底座的 ACP 面根本不含命令，故永不产生本事件）。
			// 纯转发：平台不缓存、不合并、不做会话快照——会话重开时本事件经 since_seq
			// 全量回放即可重建，故无需 REST 读端点（ADR-17）。
			if d, ok := ev.Payload.(acl.CommandsInfo); ok {
				typ = session.BusCommands
				payload = map[string]any{
					"session_id": ev.SessionID,
					"runtime":    runtimeID,
					"commands":   d.Commands,
				}
			}
		}
		if typ != "" && payload != nil {
			o.svc.publish(typ, ev.SessionID, payload)
		}
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
func toolPayload(sessionID, runtimeID string, d acl.ToolCallInfo) map[string]any {
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

// recordUsage 记录平台会话最近一次用量快照（同会话后到覆盖先到；空快照忽略）。
func (o *Orchestrator) recordUsage(sessionID string, u objusage.UsageInfo) {
	if sessionID == "" || !u.Reported() {
		return
	}
	o.usageMu.Lock()
	o.usage[sessionID] = u
	o.usageMu.Unlock()
}

// LatestUsage 取会话最近一次用量快照（无上报 → false）。run 收口落库用。
func (o *Orchestrator) LatestUsage(sessionID string) (objusage.UsageInfo, bool) {
	o.usageMu.Lock()
	defer o.usageMu.Unlock()
	u, ok := o.usage[sessionID]
	return u, ok
}

// publishError 把错误按 acl 分类发布为 run.error（A3：附上 stderr 诊断尾巴，前端错误卡可展开）。
//
// `runID` 是这一轮的身份（v22 起必带）：终态物化要靠它找到开轮行——从 `runs` 里"猜最近一条
// running"只在单会话串行下碰巧成立，而猜错的代价是这一轮的事件窗口算到别的轮上。
func (o *Orchestrator) publishError(sessionID, runID string, err error, cli *acl.Client) {
	ae := acl.Classify(err)
	tail := ae.StderrTail
	if len(tail) == 0 && cli != nil {
		tail = acl.TailLines(cli.StderrTail(), runtimeconn.MaxDiagStderrLines)
	}
	o.svc.recordRuntimeError(sessionID, ae, tail)
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
	o.svc.publish(session.BusRunError, sessionID, payload)
}
