package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/runtimeconn"
)

// ——— 执行面端口的实现（P4a 正文第五十四刀） ———
//
// 连接池搬进 `internal/runtimeconn` 之后，它需要三样**本层的事实**；本文件是那三个端口的
// 实现，**只有实现，没有逻辑**：
//
//	ProviderEnv(runtimeID)      —— 子进程 env（供应商域；同时是基础连接键的输入）
//	ConfigDigest(runtimeID)     —— 平台模型清单指纹（进代际键）
//	（DropSink 的两个方法在 `connection.go`：它们要读写会话状态，属编排面）
//
// 为什么适配器落在 manager 而不是 `internal/app`：端口的签名只吃 `pkg`/域类型，而事实住在
// `Service`（`providerPlane` 与 `runtimeConfigDigests`）。装配层交的是**同一个实例**（见
// `Service.RuntimePool`），不重造池——两份池 = 两个事实源。

// ProviderEnv 实现 `runtimeconn.ProviderEnvSource`：某 runtime 上给子进程注入的 env。
//
// 「注入哪个供应商的密钥」由平台默认模型决定（`InjectedProviderID`），与下发给底座的
// `resource.Set.DefaultModel` 同源。这里用 `context.Background()`：这是**连接建立**的输入，
// 不属于任何一个请求——绑请求 ctx 会让"拨号一开始就取消"变成"env 为空"（静默少注入密钥）。
func (s *Service) ProviderEnv(runtimeID string) []string {
	return s.providerPlane.ProviderEnvFor(runtimeID, s.providerPlane.InjectedProviderID(context.Background()))
}

// ConfigDigest 实现 `runtimeconn.ConfigDigestSource`：平台为某 runtime 物化的清单指纹。
func (s *Service) ConfigDigest(runtimeID string) string { return s.runtimeConfigDigest(runtimeID) }

// 编译期断言：执行面的三个端口必须由本层实现（改名/签名漂移当场编译失败，而不是等到
// 装配期拿到一个 nil 端口——那时表现是"少注入密钥"/"代际键少一维"，两者都静默）。
var (
	_ runtimeconn.ProviderEnvSource  = (*Service)(nil)
	_ runtimeconn.ConfigDigestSource = (*Service)(nil)
	_ runtimeconn.DropSink           = (*Orchestrator)(nil)
)
