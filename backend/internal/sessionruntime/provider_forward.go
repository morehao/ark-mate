package sessionruntime

import (
	"context"
)

// ——— 供应商域：只剩**端口适配器**（P4a 正文第四十六刀）———
//
// 载体/写面/读面（约 1128 行）已落 `domain/provider`（P3a-1b），域自持一把只保护域内字段的锁
// （并发审查见计划 §3.1.35：29 处临界区零跨域），并持 `st`/`reg`/`creds` 三个注入件与两个端口。
//
// P4a 正文第四刀删掉**档案面四条**（用例出口换成 `service/svcprovider`），本刀再把**剩下七条**
// 转发清掉：四条装配入口（`SetProviderDir` / `LoadProviderDefinitions` /
// `LoadProviderDefinitionsFromStore` / `ReloadProviderDefinitionsFromStore`）改由调用点**直连域平面**
// （`app.go` 与 `gateway/source.go`），三条包内转发（`providerEnvFor` / `injectedProviderID` /
// `providerModelRefsReadOnly`）的调用点也改成 `s.planes.Providers.X(...)`。
//
// **为什么必须删**：转发层每留一条，"这段实现到底在哪一层"就少一处可见；而它的存在本身没有
// 判据价值——一行转发的正确性完全等价于被转发的那一行。

// CredentialProblem 是本文件**唯一**留下的一条，且它不是转发：它实现 `runtimeconn.CredentialSource`
// 端口（执行面诊断的"怎么修"要问供应商域"将被注入的凭据够不够"），装配期经 `SetCredentialSource(s)`
// 登记。也就是说它的消费者是**另一个域的执行面**，不是本包的调用点——端口实现随装配点走，
// 与"过渡期转发"是两件事（判据：`runtimeconn` 不许 import 供应商域，所以适配器只能留在这里）。
func (s *Service) CredentialProblem(ctx context.Context) error {
	return s.planes.Providers.CredentialProblem(ctx)
}
