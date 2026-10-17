package manager

import (
	"github.com/morehao/ark-mate/internal/runtimeconn"
	"github.com/morehao/ark-mate/pkg/acl"
)

// ——— `domain/session.Runner` 的四张窄端口在编排侧的实现（P4a 正文第五十五刀）———
//
// 端口签名只吃域/pkg 类型，所以实现能落在本层而不需要域反向 import。四个适配器都**只转发**
// 到已有的事实出口（诊断面 / 模型目录缓存 / 工具记忆 / 权限与提问管理器），不在这里做判断。

// runnerDiagnostics 运行期诊断的写口（记录失败 + stderr 尾巴）。
type runnerDiagnostics struct{ o *Orchestrator }

func (d runnerDiagnostics) RecordRuntimeError(sessionID string, ae *acl.Error, tail []string) {
	d.o.svc.recordRuntimeError(sessionID, ae, tail)
}

func (d runnerDiagnostics) StderrTail(client *acl.Client) []string {
	if client == nil {
		return nil
	}
	return acl.TailLines(client.StderrTail(), runtimeconn.MaxDiagStderrLines)
}

// runnerCatalog 运行时配置项（模型目录）的缓存写口。
type runnerCatalog struct{ svc *Service }

func (c runnerCatalog) PutConfigOptions(runtimeID string, options []acl.ConfigOption) {
	if c.svc.catalog == nil {
		return
	}
	c.svc.catalog.put(runtimeID, options)
}

// runnerToolMemory 工具调用详情的短期记忆（仍归编排侧：它的生命周期按 toolCallId，不按轮次）。
type runnerToolMemory struct{ o *Orchestrator }

func (m runnerToolMemory) Remember(info acl.ToolCallInfo) { m.o.rememberToolCall(info) }
func (m runnerToolMemory) Forget(id string)               { m.o.forgetToolCall(id) }

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
