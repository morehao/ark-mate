package runtimeconn

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/provider"

	"github.com/morehao/ark-mate/internal/infra/protocol/acp"
)

// ——— P4a 正文第五十四刀：执行面对外要的五个口子（装配期登记） ———
//
// 本域是**执行面**：它持连接与进程。但有几样东西**不属于**它：
//
//	CredentialSource   —— 「将被注入的模型凭据够不够」：供应商域的事实；
//	ProviderEnvSource  —— 「按这台机器上这一份配置，子进程该拿到哪些 env」：供应商域的事实，
//	                      而它同时是**基础连接键**的输入（同配置共享暖进程）；
//	ConfigDigestSource —— 「平台物化的模型清单指纹」：供应商/模型面的事实，进代际键
//	                      （配置在进程启动时读，不进键就会让暖进程继续用旧清单）；
//	LiveClientSource   —— 「此刻哪条连接活着、pid 是多少」：**本域自己的**读面，装配期指向池；
//	DropSink           —— 「一条连接被摘除之后，会话/在途/诊断该怎么收尾」：编排侧的事实。
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

// ConnDrop 一条连接被摘除时交给编排侧的事实。
//
// **为什么带 `Client` 而不是会话 id 列表**：谁挂在一条连接上是**会话侧**的登记
// （`ActiveSession.client`），执行面没有、也不该有那份表。计划早先写的是
// `OnConnDropped(affected []string, …)`，但那样池就必须**自己算出** affected——它只能靠
// 再存一份「连接 → 会话」的映射，而那正是「活动集在两层各存一份」这条禁令点名的形态。
// 于是改成把**句柄**交回去，由编排侧用自己的登记算出受影响者：`affected` 的来源只有一份。
type ConnDrop struct {
	// Client 刚摘下的句柄（编排侧据它找出此刻挂在它上面的会话）。
	Client *acp.Client
	// Runtime 该连接的 runtime id（日志与诊断用）。
	Runtime string
	// WasCurrent 被摘的是该 runtime 的**当前**连接。进程状态回落只在这时为真——
	// 旧连接收尾不得覆盖新连接的 running（空闲回收后立刻按需重拨的场景）。
	WasCurrent bool
	// Intentional 主动拆除（停机 / 空闲回收）而不是连接故障。收尾语义按它分叉：
	// 主动拆除不是故障，在途轮次归 interrupted、空闲会话不改写状态。
	Intentional bool
	// Cause 拆除原因标签（`intentional` / `unexpected`，见 ConnCauseLabel）。
	Cause string
}

// DropSink 连接拆除的**应用**面（编排侧实现）。
//
// 执行面只负责"摘除 + 通知"；会话状态怎么收尾、在途登记怎么作废、诊断怎么留痕，
// 都由编排侧按同一份事实决定。这条边是本刀唯一的真决策点（§10.5.7.1 问题 3）：
// 让池自己往会话域写的后果是**执行面认识会话表**，而它的端口上限只有 4 个方法。
//
// 实现约束：`OnConnDropped` 由池在**释放池锁之后**调用（实现里可以随便取别的锁）；
// `InUseOn` 同样不在池锁下调用——两条都是为了避免"池锁 ↔ 会话锁"互指。
type DropSink interface {
	// OnConnDropped 一条连接刚被摘除。
	OnConnDropped(d ConnDrop)
	// InUseOn 这条连接此刻还有人在用吗——**在途轮次、或挂着的会话（含已完成态）**。
	// 空闲回收与代际回收的唯一判据：回收一条还有人用的连接会把在线会话的句柄作废
	// （下一次 prompt 才重建），那不叫回收暖进程。
	InUseOn(conn *Conn) bool
}

// SetCredentialSource 装配期登记凭据面。
func (pl *Plane) SetCredentialSource(c CredentialSource) { pl.cred = c }

// SetLiveClientSource 装配期登记连接池读面（池由 `NewPool` 造出来后立刻登记）。
func (pl *Plane) SetLiveClientSource(l LiveClientSource) { pl.live = l }
