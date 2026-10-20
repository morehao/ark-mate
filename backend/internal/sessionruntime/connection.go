package sessionruntime

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// ——— 连接池的**编排侧**（P4a 正文第五十四刀） ———
//
// 连接表、连接键/代际键、代际准入与回收、子进程登记、空闲回收都随本刀进了
// `internal/drivers/runtimeconn`（`Pool` / `Conn`）。本文件只剩两件事，而它们都属于这一侧：
//
//	① **拨号**（`acquire`）：ACP 的三个回调是会话/编排的事实（事件出口、权限、提问），
//	   拨好之后交给池落位（`Pool.Add`：登记进程 + 状态置 running + 入表 + 起断连监听）；
//	② **拆除的应用**（`DropSink`）：一条连接被摘除之后，会话状态怎么收尾、在途登记怎么作废、
//	   诊断怎么留痕——那是会话域的不变量，池不该认识（它也放不进 ≤4 方法的端口）。
//
// **锁**：连接表在池自己的锁下，句柄表在会话域自己的锁下（P4a 第五十五刀起两者分属两层）。
// 两条边都不许互指——池在**池锁之外**回调这里（见 `Pool.Drop` 的注释）。

// acquire 取（或拨）一条 runtime 连接：活着复用，断开则作废重拨；并挂断连监听。
//
// 取键 / 复用 / 代际准入 / spawn+握手 / 登记已随 P4a 第五十七刀进 `runtimeconn.Pool.Dial`；
// 本函数只交两样编排侧事实：三个 ACP 回调与凭据检查。
func (o *Orchestrator) acquire(ctx context.Context, spec runtime.Spec, fingerprint, nativeMode string) (session.Conn, error) {
	reqPerm := func(ctx context.Context, req acp.PermissionRequest) acp.PermissionDecision {
		if o.allowAll {
			return acp.PermissionDecision{Allowed: true}
		}
		return o.requestPermission(ctx, req)
	}
	var cli *acp.Client // Dial 成功后回填；hook 都在连接读循环里被调用，晚于 Dial 返回
	reqElic := func(ctx context.Context, req acp.ElicitationRequest) acp.ElicitationOutcome {
		if o.allowAll {
			// 调试/冒烟（--allow-all）：不征询，直接按空内容接受（agent 自行处理空内容）。
			return acp.ElicitationOutcome{Action: "accept"}
		}
		sid, source, note := o.resolveElicitationScope(cli, req)
		if sid == "" {
			// 既无 `_meta` scope 也无唯一在途 run（空闲 / 会话外 request-scope / 多会话并发）、
			// 或 `_meta` 给了一个**查不到平台会话**的底座 id（D-4）→ 不猜。
			// 仍按取消回应（不能把 agent 挂死），但置 ScopeAmbiguous 并留痕（agent 诊断 + 日志），
			// 让「提问卡为什么没出现」可查，而不是无痕消失。
			glog.Warnw(ctx, "elicitation: 无法归属会话，按取消处理", "scope_source", source, "note", note)
			o.svc.runtimePlane.NoteScopeAmbiguous(note)
			return acp.ElicitationOutcome{Action: "cancel"}
		}
		req.SessionID, req.ScopeSource = sid, source
		return o.requestElicitation(ctx, req)
	}
	// elicitation/complete（URL 模式收尾通知）：agent 告知提问已在 agent 侧完成 → 宿主按 ACP 侧
	// elicitationId 收敛挂起项。归属用「同连接的唯一在途 run」校验（可判定时）；不可判定时不阻断
	// ——该 id 由 agent 生成、不可猜，且 complete 只是收敛通知（重复/迟到都不该有副作用）。
	completeElic := func(_ context.Context, elicitationID string) {
		o.elic.CompleteByURLID(o.busySessionForClient(cli), elicitationID)
	}
	rc, err := o.pool.Dial(ctx, spec, session.DialOptions{
		Fingerprint:      fingerprint,
		NativeMode:       nativeMode,
		CheckCredentials: o.svc.CredentialProblem,
		ClientOptions: acp.ClientOptions{
			OnEvent:             o.runner.EventSink(spec.ID),
			RequestPermission:   reqPerm,
			CreateElicitation:   reqElic,
			CompleteElicitation: completeElic,
		},
	})
	if err != nil {
		return nil, err
	}
	cli = rc.Client()
	return rc, nil
}

// ——— `runtimeconn.DropSink` 的实现：连接拆除的**应用** ———

// OnConnDropped 一条连接刚被池摘除（池锁已释放）：作废其上的会话句柄、清在途登记与原生 id 映射。
//
// 收尾的应用面（会话状态语义 / 在途登记 / 诊断留痕）已随 P4a 第五十七刀进
// `domain/session.Runner`；本方法只是 `runtimeconn.DropSink` 的接线点。
func (o *Orchestrator) OnConnDropped(d session.ConnDrop) {
	o.runner.DropConn(d.Client, d.Intentional)
}

// InUseOn 这条连接还有人用吗（有在途轮次、或仍挂着会话）。
//
// 判定已进 `domain/session.Runner.InUse`；本方法只是执行面回收判据的接线点。
func (o *Orchestrator) InUseOn(c session.Conn) bool {
	if c == nil {
		return true
	}
	return o.runner.InUse(c.Client())
}

// busySessionForClient 返回该连接上「唯一在途」的会话 id；无在途或多会话并发在途返回空串。
func (o *Orchestrator) busySessionForClient(client *acp.Client) string {
	return o.inflight.SoleOwner(client)
}
