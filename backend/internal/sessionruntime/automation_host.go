package sessionruntime

import (
	"context"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// ——— P2b：自动化域的宿主适配器（`automation.Host` 的实现） ———
//
// 自动化域不 import `domain/session`（同层零 import 守卫），所以"建会话 / 取消某一轮 /
// 在跑吗 / 发一条实时事件"这四件事由这里如实交出。
//
// **这里的每一次调用都不得持 manager 的任何锁**：自动化域会在自己的锁下回调这四件事
// （`runMu` 串行化"认领 → 建会话 → 写回"），再叠一层 manager 锁就是锁序反转。
// 守卫 `TestManagerDoesNotHoldMutexWhileCallingSessionPlane` 钉的是同族的另一半。

// CreateSession 实现 `automation.Host`：建一个自动化触发的会话。
//
// 刻意**不做第二份前置校验**（runtime 是否可用 / cwd 是否还在）：那份校验必然与真实创建路径
// 漂移，而 ADR-10 真正要求的是"失败也推进游标"——与校验在哪一层无关。把真实路径带码的错误
// 原样交回去，用户在面板上看到的正是"为什么没跑成"。
func (s *Service) CreateHostSession(ctx context.Context, req automation.HostSessionReq) (string, error) {
	if s.sessionUC == nil {
		return "", code.FromError(acp.NewError(acp.ClassService, "orchestrator_unavailable", "服务尚未装配会话用例", nil))
	}
	resp, err := s.sessionUC.CreateSession(ctx, dtosession.SessionCreateReq{
		Prompt: req.Prompt,
		Cwd:    req.Cwd,
		Model:  req.Model,
		Origin: req.Origin,
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// CancelHostRun 实现 `automation.Host`。
func (s *Service) CancelHostRun(ctx context.Context, sessionID string) error {
	if s.sessionUC == nil {
		return code.FromError(acp.NewError(acp.ClassService, "orchestrator_unavailable", "服务尚未装配会话用例", nil))
	}
	return s.sessionUC.CancelRun(ctx, sessionID)
}

// HostSessionRunning 实现 `automation.Host`（overlap 判据；空 id → false）。
//
// **不持 `s.mu`**：`sessionPlane.Lookup` 自己取会话域的锁并返回副本，所以读 `Status` 本身
// 就是竞态安全的。
func (s *Service) HostSessionRunning(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	conv, ok := s.sessionPlane.Lookup(sessionID)
	if !ok {
		return false
	}
	return conv.Status == session.SessionStatusInProgress
}

// EmitHostRun 实现 `automation.Host`：把 automation.run 推进实时通道。
//
// **刻意不带事件 id**（`Publish` 的第一参数空串）：`automation.run` 只走实时通道、不落
// `events` 表也不进 transcript，所以它没有 durable 身份——于是"帧上有 id ⟺ 它在 durable
// 日志里查得到"这条不变式成立（见 `eventbus.Event.ID` 的注释）。
//
// 事件类型常量在这里出现（不在自动化域）：`session.BusAutomationRun` 是会话域的对外契约，
// 自动化域只描述"发生了什么"。
func (s *Service) EmitHostRun(_ context.Context, automationID string, version int64, payload map[string]any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish("", session.BusAutomationRun, "automation:"+automationID, version, payload)
}

// automationHostAdapter 把 `Service` 的四个 `HostXxx` 方法**改名**成端口要求的方法名。
//
// 为什么不直接把方法命名成 `CreateSession` / `CancelRun`：那两个名字在 `Service` 上**已经被
// 占用**（会话创建与取消的真实入口，签名不同）。让端口适配器做一层改名，比把既有入口改名安全
// 得多——那些入口有几十个调用点。
type automationHostAdapter struct{ svc *Service }

// CreateSession 实现 `automation.Host`。
func (a automationHostAdapter) CreateSession(ctx context.Context, req automation.HostSessionReq) (string, error) {
	return a.svc.CreateHostSession(ctx, req)
}

// CancelRun 实现 `automation.Host`。
func (a automationHostAdapter) CancelRun(ctx context.Context, sessionID string) error {
	return a.svc.CancelHostRun(ctx, sessionID)
}

// SessionRunning 实现 `automation.Host`。
func (a automationHostAdapter) SessionRunning(sessionID string) bool {
	return a.svc.HostSessionRunning(sessionID)
}

// EmitRun 实现 `automation.Host`。
func (a automationHostAdapter) EmitRun(ctx context.Context, automationID string, version int64, payload map[string]any) {
	a.svc.EmitHostRun(ctx, automationID, version, payload)
}

var _ = model.SessionEntity{}
