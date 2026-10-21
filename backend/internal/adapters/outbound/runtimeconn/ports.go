package runtimeconn

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/provider"

	"github.com/morehao/ark-mate/internal/adapters/outbound/acp"
)

// ——— P4a 正文第五十四刀：执行面对外要的四个口子（装配期登记） ———
//
// 本域是**执行面**：它持连接与进程。但有几样东西**不属于**它：
//
//	CredentialSource   —— 「将被注入的模型凭据够不够」：供应商域的事实；
//	ProviderEnvSource  —— 「按这台机器上这一份配置，子进程该拿到哪些 env」：供应商域的事实，
//	                      而它同时是**基础连接键**的输入（同配置共享暖进程）；
//	ConfigDigestSource —— 「平台物化的模型清单指纹」：供应商/模型面的事实，进代际键
//	                      （配置在进程启动时读，不进键就会让暖进程继续用旧清单）；
//	LiveClientSource   —— 「此刻哪条连接活着、pid 是多少」：**本域自己的**读面，装配期指向池。
//
// 第五个口子 `DropSink`（拆除的应用面）在阶段 0.4 随 `Conn` / `ConnPool` 一起搬进
// `domain/session/conn.go`：连接端口的**声明**归域，执行面只实现它。
//
// 全部做成**端口**而不是让本域去 import manager/provider：执行面不该认识供应商档案的形态，
// 也不该认识会话表——正如供应商域不该认识连接池。端口都很窄（1～2 个方法），
// `TestRuntimeconnPortsStayNarrow` 钉着上限 4。

// CredentialSource 供应商凭据面的两个事实（诊断的"怎么修"要用）。
type CredentialSource interface {
	// CredentialProblem 「将被注入」的凭据是否悬空/缺密钥（nil = 没问题）。
	// 返回的错误里已含出路文本（原文照用，不在本域改写）。
	CredentialProblem(ctx context.Context) error
	// AuthGuidance 已连接时底座自报的鉴权方式（空 = 没声明）。
	AuthGuidance() []provider.AuthGuide
}

// LiveClientSource 连接池的"此刻有活连接吗"读面。
type LiveClientSource interface {
	// LiveClient 某 runtime 当前的连接（未断开时）；pid 为子进程号（0 = 未登记）。
	LiveClient(runtimeID string) (client *acp.Client, pid int, live bool)
}

// ProviderEnvSource 某 runtime 上要给子进程注入的 env（`KEY=VALUE` 列表；不适用 → nil）。
//
// 它有两个消费者，且**必须是同一次取值**：真正下发给子进程的 env、以及基础连接键里的
// env 签名（`Pool.KeyFor` 一次算完两者）。分两次各取一遍的后果是「键说这套配置共享暖进程、
// 实际注入的却是另一套 env」——静默，且只在带凭据的底座上现形。
type ProviderEnvSource interface {
	ProviderEnv(runtimeID string) []string
}

// ConfigDigestSource 平台为某 runtime 物化的模型清单指纹（空 = 未同步）。
// 它进**代际键**：不进键的话，清单改了而暖进程还活着，底座继续用旧清单，界面却显示「已保存」。
type ConfigDigestSource interface {
	ConfigDigest(runtimeID string) string
}

// SetCredentialSource 装配期登记凭据面。
func (pl *Plane) SetCredentialSource(c CredentialSource) { pl.cred = c }

// SetLiveClientSource 装配期登记连接池读面（池由 `NewPool` 造出来后立刻登记）。
func (pl *Plane) SetLiveClientSource(l LiveClientSource) { pl.live = l }
