package run

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
)

// ——— 执行面端口的实现（P4a 正文第五十四刀） ———
//
// 连接池搬进 `internal/adapters/outbound/runtimeconn` 之后，它需要三样**本层的事实**；本文件是那三个端口的
// 实现，**只有实现，没有逻辑**：
//
//	ProviderEnv(runtimeID)      —— 子进程 env（供应商域；同时是基础连接键的输入）
//	NativePermissionEnv(...)     —— 原生权限模式的子进程 env（进基础键）
//	（ConfigDigest 的实现已随同步面在 P5a 搬出编排层（C3 4.5 后半落 `adapters/outbound/models.Resources`）——那份状态与同步编排
//	  是同一件事，分开就是"同步写了一半"；装配点在 NewService 里直接交给池。）
//	（DropSink 的两个方法在 `connection.go`：它们要读写会话状态，属编排面）
//
// 为什么适配器落在 manager 而不是 `internal/bootstrap/app`：端口的签名只吃 `pkg`/域类型，而事实住在
// `Service`（`providerPlane`）。装配层交的是**同一个实例**（见 `Service.RuntimePool`），
// 不重造池——两份池 = 两个事实源。

// ProviderEnv 实现 `runtimeconn.ProviderEnvSource`：某 runtime 上给子进程注入的 env。
//
// 「注入哪个供应商的密钥」由平台默认模型决定（`InjectedProviderID`），与下发给底座的
// `resource.Set.DefaultModel` 同源。这里用 `context.Background()`：这是**连接建立**的输入，
// 不属于任何一个请求——绑请求 ctx 会让"拨号一开始就取消"变成"env 为空"（静默少注入密钥）。
func (s *Service) ProviderEnv(runtimeID string) []string {
	return s.facts.Providers.ProviderEnvFor(runtimeID, s.facts.Providers.InjectedProviderID(context.Background()))
}

// NativePermissionEnv 实现 `runtimeconn.NativeEnvSource`：该 runtime 在给定原生权限模式下
// 的**进程 env**（P1-f 下半 / B5 的接活点）。
//
// 这是 `NativePermissionAdapter.Env()` 在生产代码里的**唯一调用点**——它此前是一段写得很完整
// 却没有任何调用者的死代码（守卫 `TestNativePermissionModeLiteralsMatchWire` 只管它的取值，
// 不管它有没有人喊）。
//
// 三个入参的来源都在这里收口，调用方只给模式：
//
//	mode  —— 会话钉住的那一档（`session.SessionRec.NativeMode`；空 = 不注入）
//	def   —— 权限策略的默认动作（空 = 未声明，由 adapter 按自己的词表处理）
//	tools —— 工具级 allow/ask/deny 表（本层暂时不提供，adapter 侧已容忍 nil）
//
// **空模式直接短路**（不调 adapter）：`”` 的语义是"不注入原生配置"，那是平台级开关，
// 不属于任何底座（adapter 那边的 `NativeModeOff` 分支也写着同一句话）。
func (s *Service) NativePermissionEnv(runtimeID, mode string) ([]string, string) {
	if mode == "" {
		return nil, "native_mode 空（不注入原生权限配置）"
	}
	adapter, ok := s.reg.NativePermission(runtimeID)
	if !ok {
		return nil, "该底座未登记原生权限投影（不注入）"
	}
	return adapter.Env(mode, "", nil)
}

// 编译期断言：拆除的应用面必须由编排侧实现（改名/签名漂移当场编译失败）。
//
// 另两个来源端口（`ProviderEnv` / `NativePermissionEnv`）的断言随阶段 0.4b 删掉：
// 本层不再 import `runtimeconn`，改由装配根的 `SetProviderEnvSource(s)` / `SetNativeEnvSource(s)`
// 调用做**同一份**编译期检查（装配点漏接会直接编译不过）。
var _ session.DropSink = (*Orchestrator)(nil)

// ——— 供应商域：只剩**端口适配器**（P4a 正文第四十六刀）———
//
// 载体/写面/读面（约 1128 行）已落 `domain/provider`（P3a-1b），域自持一把只保护域内字段的锁
// （并发审查见计划 §3.1.35：29 处临界区零跨域），并持 `st`/`reg`/`creds` 三个注入件与两个端口。
//
// P4a 正文第四刀删掉**档案面四条**（用例出口换成 `service/svcmodel`），本刀再把**剩下七条**
// 转发清掉：四条装配入口（`SetProviderDir` / `LoadProviderDefinitions` /
// `LoadProviderDefinitionsFromStore` / `ReloadProviderDefinitionsFromStore`）改由调用点**直连域平面**
// （`app.go` 与 `gateway/source.go`），三条包内转发（`providerEnvFor` / `injectedProviderID` /
// `providerModelRefsReadOnly`）的调用点也改成 `s.facts.Providers.X(...)`。
//
// **为什么必须删**：转发层每留一条，"这段实现到底在哪一层"就少一处可见；而它的存在本身没有
// 判据价值——一行转发的正确性完全等价于被转发的那一行。

// CredentialProblem 是本文件**唯一**留下的一条，且它不是转发：它实现 `runtimeconn.CredentialSource`
// 端口（执行面诊断的"怎么修"要问供应商域"将被注入的凭据够不够"），装配期经 `SetCredentialSource(s)`
// 登记。也就是说它的消费者是**另一个域的执行面**，不是本包的调用点——端口实现随装配点走，
// 与"过渡期转发"是两件事（判据：`runtimeconn` 不许 import 供应商域，所以适配器只能留在这里）。
func (s *Service) CredentialProblem(ctx context.Context) error {
	return s.facts.Providers.CredentialProblem(ctx)
}
