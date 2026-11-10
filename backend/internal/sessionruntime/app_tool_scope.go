package sessionruntime

// ——— WP5：app 业务工具独立注入通道的唯一端口 ———
//
// 设计 §5.3(a)/§5.5：app 的业务工具走**独立的、逐会话的**注入通道，与平台内置 server（恒注入）
// 完全分开。会话 app scope（`CapabilitySelection.Apps`）是**唯一来源**：nil/空 → 零 app 工具；
// 非空 → 只注入这些 app 的工具域。
//
// 本层只回答一个问题："这些 app id 各自绑了哪些已编入的工具域？"——域 handler 的实现体（`mcp.Handler`）
// 编在 `infra/<app>mcp`、由 `infra/apptool` 聚合，都在**子进程**里跑；核心进程不需要拿到 handler，
// 只需要知道"要注入哪条域集合"，据此拼一条 `mate mcp app --apps=...` 的 stdio 条目。
//
// 端口由装配根注入（`NewService` 后 `SetAppToolScope`）：核心进程不认识 svcapp，也不该认识——
// app 定义的事实源在用例层，本层只消费一个"id→工具域集合"的窄问答。

// AppToolScope app 工具域解析端口（装配根实现；nil = 未登记，等价于"没有 app 工具"）。
type AppToolScope interface {
	// EnabledToolDomains 返回**这些 app id 里已启用者**绑定的工具域并集（去重、升序稳定）。
	//
	// 语义（fail-closed 在实现侧）：
	//   - 未知 app id（不存在/已删除/已停用）→ **不贡献任何域**（自然收窄到 0，不是错误：
	//     app 被停用/删除后历史会话恢复时该 app 的工具就该消失，属预期的"关得干净"）。
	//   - 返回空切片 = 没有任何可注入域（含"app 存在但没绑工具域"），调用方据此零注入。
	EnabledToolDomains(appIDs []string) []string
}

// SetAppToolScope 注入 app 工具域解析端口（装配根在 app 定义就绪后接线）。
//
// 与 `SetAttachmentResolver` 同款：Service 在 Build 阶段构造，app 定义面稍后才就绪。
func (s *Service) SetAppToolScope(sc AppToolScope) { s.appToolScope = sc }
