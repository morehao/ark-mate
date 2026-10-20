package sessionruntime

import (
	"context"
	"github.com/morehao/ark-mate/internal/core/code"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— `domain/session.Runner` 的四张窄端口在编排侧的实现（P4a 正文第五十五刀）———
//
// 端口签名只吃域/pkg 类型，所以实现能落在本层而不需要域反向 import。四个适配器都**只转发**
// 到已有的事实出口（诊断面 / 模型目录缓存 / 工具记忆 / 权限与提问管理器），不在这里做判断。

// runnerDiagnostics 运行期诊断的写口（记录失败 + stderr 尾巴）。
type runnerDiagnostics struct{ o *Orchestrator }

func (d runnerDiagnostics) RecordRuntimeError(sessionID string, ae *code.RuntimeError, tail []string) {
	d.o.svc.runtimePlane.RecordRuntimeError(sessionID, ae, tail)
}

func (d runnerDiagnostics) StderrTail(conn session.Conn) []string {
	if conn == nil {
		return nil
	}
	// 尾巴的采集与截断在适配器（`runtimeconn`）里做，域只拿结果。
	return conn.Agent().StderrTail()
}

// runnerCatalog 运行时配置项（模型目录）的缓存写口。
type runnerCatalog struct{ svc *Service }

func (c runnerCatalog) PutConfigOptions(runtimeID string, options []runtime.ConfigOption) {
	if c.svc.facts.Providers == nil {
		return
	}
	c.svc.facts.Providers.PutConfigOptions(runtimeID, options)
}

// MarkRejected 记录某取值被运行时拒绝（目录里标灰；建会话/恢复/改模型共用）。
func (c runnerCatalog) MarkRejected(runtimeID, value, reason string) {
	if c.svc.facts.Providers == nil {
		return
	}
	c.svc.facts.Providers.MarkModelRejected(runtimeID, value, reason)
}

// ClearRejection 清掉曾失败值的标记（同一取值再次被接受时）。
func (c runnerCatalog) ClearRejection(runtimeID, value string) {
	if c.svc.facts.Providers == nil {
		return
	}
	c.svc.facts.Providers.ClearModelRejection(runtimeID, value)
}

// acquireConn 是 `Lifecycle.Acquire` 的实现：拨号仍归编排侧（回调与凭据检查在这里），
// 域只拿到**连接端口**去开/恢复会话（能力面经 `Conn.Agent()` 取）。
func (o *Orchestrator) acquireConn(ctx context.Context, spec runtime.Spec, fingerprint, nativeMode string) (session.Conn, error) {
	rc, err := o.acquire(ctx, spec, fingerprint, nativeMode)
	if err != nil {
		return nil, err
	}
	if rc == nil || rc.Agent() == nil {
		return nil, code.NewError(code.ClassConnection, "connection_unavailable",
			"连接已建立但没有可用的 ACP 客户端", nil)
	}
	return rc, nil
}

// runnerInteractions 会话取消时把挂起的权限/提问一并收敛（权限面事实）。
type runnerInteractions struct {
	perm interface{ CancelSession(string) }
	elic interface{ CancelSession(string) }
}

func (i runnerInteractions) CancelSession(sessionID string) {
	if i.perm != nil {
		i.perm.CancelSession(sessionID)
	}
	if i.elic != nil {
		i.elic.CancelSession(sessionID)
	}
}
