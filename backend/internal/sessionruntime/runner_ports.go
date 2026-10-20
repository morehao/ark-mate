package sessionruntime

import (
	"context"

	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— `domain/session.Runner` 的四张窄端口在编排侧的实现（P4a 正文第五十五刀）———
//
// 端口签名只吃域/pkg 类型，所以实现能落在本层而不需要域反向 import。四个适配器都**只转发**
// 到已有的事实出口（诊断面 / 模型目录缓存 / 工具记忆 / 权限与提问管理器），不在这里做判断。

// runnerDiagnostics 运行期诊断的写口（记录失败 + stderr 尾巴）。
type runnerDiagnostics struct{ o *Orchestrator }

func (d runnerDiagnostics) RecordRuntimeError(sessionID string, ae *acp.Error, tail []string) {
	d.o.svc.runtimePlane.RecordRuntimeError(sessionID, ae, tail)
}

func (d runnerDiagnostics) StderrTail(client *acp.Client) []string {
	if client == nil {
		return nil
	}
	return acp.TailLines(client.StderrTail(), runtimeconn.MaxDiagStderrLines)
}

// runnerCatalog 运行时配置项（模型目录）的缓存写口。
type runnerCatalog struct{ svc *Service }

func (c runnerCatalog) PutConfigOptions(runtimeID string, options []acp.ConfigOption) {
	if c.svc.planes.Providers == nil {
		return
	}
	c.svc.planes.Providers.PutConfigOptions(runtimeID, options)
}

// MarkRejected 记录某取值被运行时拒绝（目录里标灰；建会话/恢复/改模型共用）。
func (c runnerCatalog) MarkRejected(runtimeID, value, reason string) {
	if c.svc.planes.Providers == nil {
		return
	}
	c.svc.planes.Providers.MarkModelRejected(runtimeID, value, reason)
}

// ClearRejection 清掉曾失败值的标记（同一取值再次被接受时）。
func (c runnerCatalog) ClearRejection(runtimeID, value string) {
	if c.svc.planes.Providers == nil {
		return
	}
	c.svc.planes.Providers.ClearModelRejection(runtimeID, value)
}

// acquireClient 是 `Lifecycle.Acquire` 的实现：拨号仍归编排侧（回调与凭据检查在这里），
// 域只拿到 ACP 客户端去开/恢复会话。
func (o *Orchestrator) acquireClient(ctx context.Context, spec runtime.Spec, fingerprint, nativeMode string) (*acp.Client, error) {
	rc, err := o.acquire(ctx, spec, fingerprint, nativeMode)
	if err != nil {
		return nil, err
	}
	if rc == nil || rc.Client() == nil {
		return nil, acp.NewError(acp.ClassConnection, "connection_unavailable",
			"连接已建立但没有可用的 ACP 客户端", nil)
	}
	return rc.Client(), nil
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
