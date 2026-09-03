package manager

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/runtime"
)

// Orchestrator 把「对一次会话的 ACP 编排」跑通：连接获取 → session/new → prompt →
// 事件流（入 eventbus）→ cancel/close。M0 提供 RunOnce（fresh session 全流程）。
type Orchestrator struct {
	svc *Service
	reg *runtime.Registry

	mu       sync.Mutex
	conns    map[string]*runtimeConn // key = runtime ID；M0 每 runtime 至多一条连接
	allowAll bool                    // 调试/冒烟用：放行一切权限请求（默认拒绝）

	logger *slog.Logger
}

type runtimeConn struct {
	client  *acl.Client
	agentID string
	spec    runtime.Spec
}

// RunOptions 一次 RunOnce 的入参。
type RunOptions struct {
	RuntimeID  string // opencode / deepseekharness
	Prompt     string
	Cwd        string   // 空则用当前工作目录
	Env        []string // 附加环境（如 DEEPSEEK_API_KEY 已导出时无需）
	ConfigPath string   // harness 专用：替换 Launch.Args 里的 {config} 占位
}

// RunResult 一次 RunOnce 的结果。
type RunResult struct {
	SessionID  string
	AgentID    string
	StopReason string
}

// NewOrchestrator 构造编排器（svc 提供 bus/存储）。
func NewOrchestrator(svc *Service, reg *runtime.Registry) *Orchestrator {
	svc.SetBus(svc.bus) // no-op 保底
	return &Orchestrator{
		svc:    svc,
		reg:    reg,
		conns:  make(map[string]*runtimeConn),
		logger: slog.Default(),
	}
}

// SetAllowAll 放开权限请求（仅调试/冒烟使用；默认一律拒绝）。
func (o *Orchestrator) SetAllowAll(v bool) { o.allowAll = v }

// SetLogger 设置编排日志。
func (o *Orchestrator) SetLogger(l *slog.Logger) {
	if l != nil {
		o.logger = l
	}
}

// RunOnce 在指定 runtime 上跑一次 fresh session + prompt，返回会话/终止信息。
// 流式事件（message/thought/tool_call/...）经 event sink 入 eventbus（agg = session id）。
func (o *Orchestrator) RunOnce(ctx context.Context, opt RunOptions) (*RunResult, error) {
	spec, err := o.reg.Require(opt.RuntimeID)
	if err != nil {
		return nil, err
	}
	agent, err := o.svc.requireAgent(spec.ID)
	if err != nil {
		return nil, err
	}

	conn, err := o.acquire(ctx, agent, spec, opt)
	if err != nil {
		o.svc.setAgentState(agent.ID, AgentStateStopped)
		return nil, err
	}

	// 会话阶梯（M0：fresh；load/resume 由能力门控在 acl.Session 层返回 unsupported，
	// M1 REST 层按 capabilities 裁剪 UI 入口）。
	cwd := opt.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	sess, err := conn.client.NewSession(ctx, acl.SessionOptions{Cwd: cwd})
	if err != nil {
		return nil, err // 无 session id 可上抛，错误经返回路径记录/转 turn.error 由调用方处理
	}

	rec := &SessionRec{ID: sess.ID(), AgentID: agent.ID, Runtime: spec.ID, Status: SessionStatusInProgress}
	o.svc.registerSession(rec)
	o.svc.publish(BusSessionCreated, sess.ID(), map[string]any{
		"session_id": sess.ID(), "agent_id": agent.ID, "runtime": spec.ID,
	})
	o.watchDisconnect(conn.client, sess.ID())

	stop, err := sess.Prompt(ctx, opt.Prompt)
	if err != nil {
		o.svc.markSession(sess.ID(), SessionStatusFailed)
		o.publishError(sess.ID(), err)
		return &RunResult{SessionID: sess.ID(), AgentID: agent.ID}, err
	}

	status := SessionStatusCompleted
	if strings.EqualFold(stop, "cancelled") {
		status = SessionStatusCancelled
		o.svc.publish(BusRunCancelled, sess.ID(), map[string]any{"session_id": sess.ID(), "stop_reason": stop})
	} else {
		o.svc.publish(BusTurnEnded, sess.ID(), map[string]any{
			"session_id": sess.ID(), "stop_reason": stop,
		})
	}
	o.svc.markSession(sess.ID(), status)
	return &RunResult{SessionID: sess.ID(), AgentID: agent.ID, StopReason: stop}, nil
}

// Close 关闭全部连接（daemon 退出/测试收尾）。
func (o *Orchestrator) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for id, rc := range o.conns {
		o.svc.setAgentState(rc.agentID, AgentStateStopped)
		_ = rc.client.Close()
		delete(o.conns, id)
	}
}

// acquire 取（或拨）一条 runtime 连接：活着复用，断开/不存在则新拨。
func (o *Orchestrator) acquire(ctx context.Context, agent *AgentRec, spec runtime.Spec, opt RunOptions) (*runtimeConn, error) {
	o.mu.Lock()
	rc := o.conns[spec.ID]
	o.mu.Unlock()
	if rc != nil {
		select {
		case <-rc.client.Done():
			// 断连即失效：旧连接作废重拨
		default:
			return rc, nil
		}
	}

	args := resolveArgs(spec.Launch.Args, opt)
	env := append([]string(nil), spec.Launch.Env...)
	env = append(env, opt.Env...)
	t := acl.NewStdioTransport(spec.Launch.Command, args, env)
	if spec.Launch.Command == "" {
		_ = t.Close()
		return nil, acl.NewError(acl.ClassUnsupported, "runtime_missing_launch", "runtime 未配置启动命令", nil)
	}
	logger := o.logger.With("runtime", spec.ID, "cmd", spec.Launch.Command)
	t.SetLogger(logger)

	allowPerm := func(context.Context, string, string, int) bool { return o.allowAll }
	client, err := acl.Dial(ctx, t, acl.ClientOptions{
		OnEvent:         o.eventSink(agent.ID, spec.ID),
		Logger:          logger,
		AllowPermission: allowPerm,
	})
	if err != nil {
		_ = t.Close()
		return nil, err
	}

	o.svc.setAgentState(agent.ID, AgentStateRunning)
	rc = &runtimeConn{client: client, agentID: agent.ID, spec: spec}
	o.mu.Lock()
	o.conns[spec.ID] = rc
	o.mu.Unlock()
	return rc, nil
}

// eventSink 把 acl 归一化事件转为域事件进 eventbus（agg = session id）。
func (o *Orchestrator) eventSink(agentID, runtimeID string) func(acl.Event) {
	return func(ev acl.Event) {
		if ev.SessionID == "" {
			return
		}
		typ := ""
		var payload any
		switch ev.Kind {
		case acl.KindMessage:
			typ = BusMessage
			if d, ok := ev.Payload.(acl.MessageDelta); ok {
				payload = map[string]any{"session_id": ev.SessionID, "text": d.Text, "agent_id": agentID}
			}
		case acl.KindThought:
			typ = BusThought
			if d, ok := ev.Payload.(acl.ThoughtDelta); ok {
				payload = map[string]any{"session_id": ev.SessionID, "text": d.Text, "agent_id": agentID}
			}
		case acl.KindToolCall:
			typ = BusToolCall
			if d, ok := ev.Payload.(acl.ToolCallInfo); ok {
				payload = map[string]any{
					"session_id": ev.SessionID, "agent_id": agentID,
					"tool_call_id": d.ID, "title": d.Title, "status": d.Status,
				}
			}
		case acl.KindToolUpdate:
			typ = BusToolUpdate
			if d, ok := ev.Payload.(acl.ToolCallInfo); ok {
				payload = map[string]any{
					"session_id": ev.SessionID, "agent_id": agentID,
					"tool_call_id": d.ID, "title": d.Title, "status": d.Status,
				}
			}
		case acl.KindUserMessage:
			typ = BusUserMessage
			if d, ok := ev.Payload.(acl.MessageDelta); ok {
				payload = map[string]any{"session_id": ev.SessionID, "text": d.Text, "agent_id": agentID}
			}
		case acl.KindUsage:
			typ = BusUsage
			payload = map[string]any{"session_id": ev.SessionID, "agent_id": agentID}
		case acl.KindPermission:
			typ = "session.permission"
			payload = ev.Payload
		}
		if typ != "" && payload != nil {
			o.svc.publish(typ, ev.SessionID, payload)
		}
	}
}

// watchDisconnect 监听连接断开，把该连接上的会话标为失效（harness「断连即释放」收敛点）。
func (o *Orchestrator) watchDisconnect(c *acl.Client, sessionID string) {
	go func() {
		select {
		case <-c.Done():
			o.svc.markSession(sessionID, SessionStatusFailed)
			o.svc.publish(BusTurnError, sessionID, map[string]any{
				"session_id": sessionID, "code": "connection_lost", "class": "connection",
				"details": "agent 连接断开（deepseek-harness 会话断连即失效）",
			})
		}
	}()
}

func (o *Orchestrator) publishError(sessionID string, err error) {
	ae := acl.Classify(err)
	o.svc.publish(BusTurnError, sessionID, map[string]any{
		"session_id": sessionID,
		"code":       ae.Code,
		"class":      string(ae.Class),
		"details":    ae.Details,
	})
}

// resolveArgs 替换 Launch.Args 中的占位（如 harness 的 {config}）。
func resolveArgs(args []string, opt RunOptions) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, "{config}", opt.ConfigPath)
	}
	return out
}
