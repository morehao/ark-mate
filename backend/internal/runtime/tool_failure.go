package runtime

// 工具失败分类的**第四张可执行端口表**（v27 / S4-③，ADR-16 的旁支）。
//
// 为什么需要它：参照产品的工具结果里有两类失败**从 status 上看不出来**——被沙箱拒绝的那一帧
// 是 `status:"completed"`（探针实测，2026-10-16），而 runner 坏了是 `status:"failed"`。也就是说
// "这一条工具调用到底为什么没成"这件事，**只在结果文本里有**。
//
// 归类属**底座私有知识**（签名串是那个底座的措辞），所以表在这里、实现在 `internal/infra/<adapter>`；
// 内核只问「这条文本该归成什么」，不认识任何具体串。

// ToolFailure 一次工具失败的**平台侧归类**（对外载荷键 `fail_reason` + `biz_code`）。
type ToolFailure struct {
	// Reason 稳定归类：`sandbox_denied` / `sandbox_unavailable` / `""`（未知即空）。
	//
	// 界面拿它决定"要不要给升权引导"——`sandbox_denied` 可以引导，`sandbox_unavailable` 不能
	// （宿主根本没有可用的沙箱后端，再点一次也一样失败）。
	Reason string
	// BizCode 可选的稳定业务码（供面板/日志对齐）。空 = 该归类没有更细的码。
	BizCode string
}

// ToolFailureClassifier 把一个底座的工具结果文本归类（纯函数；不认识就回零值）。
//
// 返回零值 = **不知道**，而不是"成功"。调用方据此不写 `fail_reason`
// （写一个猜的原因比不写更糟：用户会照着它去修一个不存在的问题）。
// 方法签名刻意只用**基础类型**：adapter 不能 import 本包（本包反过来要 import
// 每个 adapter 来登记端口，双向就成环），所以端口只能用原语表达。这与
// `CredentialSpec` 的取值面收窄成纯值是同一条讲究。
type ToolFailureClassifier interface {
	ClassifyToolFailure(text string) (reason, bizCode string)
}

// RegisterToolFailureClassifier 登记某 runtime 的工具失败分类实现（同 ID 后注册覆盖）。
func (r *Registry) RegisterToolFailureClassifier(id string, c ToolFailureClassifier) {
	if r == nil {
		return
	}
	if r.toolFailures == nil {
		r.toolFailures = map[string]ToolFailureClassifier{}
	}
	r.toolFailures[id] = c
}

// ClassifyToolFailure 取某 runtime 的归类结果；未登记 / 不认识 → `ok=false`。
//
// 两态刻意分开：`ok=false` 是"这个底座没接这张表"（不写键），而 `tf.Reason == ""` 是
// "接了、但这条文本不认识"（同样不写键，但语义上是 DEGRADED，值得在排障时区分）。
func (r *Registry) ClassifyToolFailure(id, text string) (ToolFailure, bool) {
	if r == nil || r.toolFailures == nil || text == "" {
		return ToolFailure{}, false
	}
	c, ok := r.toolFailures[id]
	if !ok || c == nil {
		return ToolFailure{}, false
	}
	reason, code := c.ClassifyToolFailure(text)
	return ToolFailure{Reason: reason, BizCode: code}, true
}
