package code

import (
	"errors"
	"regexp"
	"strings"

	"github.com/morehao/golib/gerror"
)

// ——— 平台业务错误的**载体**与**投影** ———
//
// 为什么内圈需要自己的载体：`acp.NewError` 的四个参数里，第一个是 **错误分类**（可操作语义：
// 重试、登录、换参数、降级），后三个才是文案与 cause。用例层与领域层为了借这套语义曾被迫
// import 协议库，于是"内圈认识外圈协议"这条裂缝从错误出口重新长回来。
//
// B4 之后协议库在**适配器出口**就把协议错误折成平台视图（`RuntimeError`，见 runtime_error.go），
// 本包因此对 `internal/adapters/outbound/acp` 零依赖：
//
//	构造：内圈写 `code.NewError(code.ClassRequest, "session_not_found", …)` → `*Coded`；
//	投影：`FromError` 把平台视图/平台带码错误按分类映射成对外业务码（下面是唯一投影点）。
//
// 两份词汇（平台 Class ↔ 协议 Class）同样值，由 `TestErrorClassVocabularyMatchesACP` 逐条钉住。

// notFoundCodes 是"目标不存在"这一类稳定 code。
//
// 它们落在 ClassRequest 里（对上游而言确实是一次被拒的请求），但对调用方
// 而言语义是 404：再改参数也不会有结果，得先把资源建好。故单独提档到 NotFound，
// 让前端能按 40401 直接引导"去注册 agent / 去建 runtime"，而不是当成参数错误。
var notFoundCodes = map[string]bool{
	"agent_not_found":      true,
	"session_not_found":    true,
	"runtime_not_found":    true,
	"mcp_not_found":        true,
	"skill_not_found":      true,
	"provider_not_found":   true,
	"automation_not_found": true,
	// 队列三动作（ADR-10）：重排 / 立即发送 / 取出编辑 打到一个**已不在队列里**的队项。
	//
	// 它刻意**不是**幂等 no-op（与撤下那条相反）：撤下是"我不想要了"，重复撤下没有额外含义；
	// 而"移动一个不存在的项"若回成功，界面会真的去改本地顺序——那是**假成功**。
	"queued_input_not_found": true,
	// 设置 · 模型（v11）：PATCH/DELETE 打到不存在的模型条目——与"打错 id 的新建"是两件事，
	// 必须能被前端分辨（刷新列表 vs 改参数）。
	//
	// v26 起**同一个码**还承担第二种处境：会话引用了一个平台清单里查不到的模型行（`POST /sessions`
	// 与 `PATCH /sessions/:id` 的 `model`）。刻意**不新增码**（E9）：两者对用户是同一件事——
	// "这个 id 指不到一行，去刷新列表/重新选一个"；新增一个码要动 `internal/platform/code`↔README↔前端三条边
	// 加三条守卫，收益为零。文案在 manager 里分开写（`sessionModelNotFoundErr`）。
	"model_not_found": true,
	// 工作空间登记（v14）：PATCH/DELETE/`:id/move` 打到不存在的登记。
	//
	// 与 `workspace_invalid` **刻意不同码**：那个是"你给的路径不能用"（改参数还有救），
	// 而这一个是"这个 id 没有对象"（再改参数也不会有结果，得先刷新列表）——前端要据 40401
	// 引导刷新，而不是引导改路径。
	"workspace_not_found": true,
}

// classCodeOverrides 是"分类对、但对外码要换一个"的例外表（按稳定 code 名）。
//
// 为什么需要它：Class 表达的是**可操作语义**（重试/登录/换参数/降级），而对外码表达的是
// **用户该做什么**。绝大多数时候两者同向，但有一类故障它们的指向不同——
//
//	resource_sync_failed：平台把应用层清单交付给某个底座失败（v12 的同步链路；v11 叫
//	  config_write_failed）。
//	  它落 ClassService（确实是平台侧的失败，不是用户请求错），而 ClassService 默认映到
//	  BadGateway(50201)"上游连接失败"——那会把排障方向指向底座进程/网络，而真因可能在平台自己的
//	  生成器、校验门或磁盘上。改映到 UpstreamFailed(50001) 与它的实际语义一致：**服务侧失败、改参数无用**。
var classCodeOverrides = map[string]int{
	"resource_sync_failed": UpstreamFailed,
}

// Coded 是平台自己的**带码错误**：分类（可操作语义）+ 稳定 code + 文案 + 可选诊断尾巴 + cause。
//
// 它是内圈表达"这是哪一种错"的唯一构造产物。`RuntimeError`（runtime_error.go）是同形的**上游
// 执行错误视图**；两者都由下面的 `codeOf` 投影成对外码，因此新增/改名一个 code 只需要动一处。
type Coded struct {
	// Class 可操作分类（重试 / 登录 / 改参数 / 降级）。
	Class Class
	// Code 稳定 code 名（进信封与诊断面；前端据此给动作）。
	Code string
	// Details 人话描述（出口统一过脱敏闸门；可直接展示）。
	Details string
	// StderrTail 需要外露的诊断尾巴（业务错误通常为空；空 = 未采集）。
	StderrTail []string
	// Err 原始错误（供日志与更深层判定；不进对外载荷）。
	Err error
}

// Error 实现 error（日志与 `%v` 用）。
func (e *Coded) Error() string {
	if e.Err == nil {
		return "code error: class=" + string(e.Class) + " code=" + e.Code + " " + e.Details
	}
	return "code error: class=" + string(e.Class) + " code=" + e.Code + " " + e.Details + ": " + e.Err.Error()
}

// Unwrap 让 `errors.As/Is` 能穿透到原始错误。
func (e *Coded) Unwrap() error { return e.Err }

// CodedOf 从错误链里取**平台带码错误**。
//
// 取外层而不是最内层：包装它的那一层才是"这是什么错"的答案（`errors.As` 深度优先命中第一个）。
func CodedOf(err error) (*Coded, bool) {
	if err == nil {
		return nil, false
	}
	var ce *Coded
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}

// codeOf 把**平台错误**投影成对外业务码（唯一投影点）。
//
// 这张表是分类（可操作语义：重试/登录/换参数/降级）到对外码的单向投影，因此放在本包而不是
// 协议库：协议是可复用的客户端，不该知道 ark-mate 的信封编号；反过来本包作为"编号唯一事实源"
// 必须收下这张表，否则同一个分类在控制器与领域层会映射成两个不同的码。
func codeOf(class Class, name string) int {
	c := UpstreamFailed
	switch class {
	case ClassRequest:
		c = InvalidRequest
		if notFoundCodes[name] {
			c = NotFound
		}
	case ClassLimit:
		c = Conflict
	case ClassAccess:
		c = Unauthorized
	case ClassUnsupported:
		c = NotSupported
	case ClassService, ClassConnection:
		c = BadGateway
	}
	if o, ok := classCodeOverrides[name]; ok {
		c = o
	}
	return c
}

// fromView 把平台错误视图投影成对外信封错误（视图为 nil 时 ok=false）。
func fromView(re *RuntimeError) (gerror.Error, bool) {
	if re == nil {
		return gerror.Error{}, false
	}
	return Error(codeOf(re.Class, re.Code), messageOf(re)), true
}

// messageOf 取平台错误视图的对外文案。
//
// Details 是脱敏后给用户看的一句话；缺省回落到稳定 code，保证 msg 永不为空（空 msg 在前端会
// 渲染成一个没有内容的错误气泡）。有 stderr 尾巴时并进文案（见 stderrMsgSuffix）。
func messageOf(re *RuntimeError) string {
	msg := re.Details
	if msg == "" {
		msg = re.Code
	}
	if s := stderrMsgSuffix(re.StderrTail); s != "" {
		// SDK 把"底座进程没了"裹成 JSON-RPC -32603 的通用文案（字面量 `Internal error`，
		// 见 acp-go-sdk errors.go）。这句话单独摆给用户没有信息量，而尾巴里有——换掉它。
		// 若将来 SDK 换了文案，这里只是不再替换（退回它自己的话），不会出错。
		if msg == "Internal error" {
			msg = "底座未完成 ACP 握手就断开"
		}
		msg += "；" + s
	}
	return msg
}

// 信封文案里底座 stderr 的量纲（**与协议侧的尾巴上限不是一回事**）：
// 协议侧保留 200 行是给事件/诊断面板那种结构化字段用的；而 msg 会进前端错误气泡、访问日志
// 与用户截图，两者差一个数量级，所以这里另设一个更小的上限。
const (
	stderrMsgLines = 5   // 最多带几行
	stderrMsgRunes = 300 // 每行最多几个字符（rune，避免截断多字节）
)

// stderrCauseRe 认出"这一行就是失败原因"的常见形态：Node 的 `Error: <原因>`、Go 的 `panic:`、
// Python 的 `XxxError:`/`Traceback`、Java 的 `Exception in thread`、Rust 的 `error[E…]`，
// 以及通用的 `fatal:` / 日志里的 `ERROR:`。
var stderrCauseRe = regexp.MustCompile(`(?i)^\s*(error|fatal|panic|traceback|exception)\b|\b(error|exception)s?\s*:`)

// stderrMsgSuffix 把底座 stderr 尾巴并进对外文案。
//
// 为什么必须并：底座起不来时，协议 SDK 只回一句通用的 `Internal error`
// （JSON-RPC -32603 的固定文案，见 acp-go-sdk errors.go），真因在子进程 stderr 里
// （典型如 dsh 的 `.env` 保留键报错）。不并的话用户只看到 `50001 Internal error`，
// 完整原因却躺在核心日志里——排障得先知道去翻日志。
//
// **选行规则是先因后果，不是简单取尾部**：Node 把原因打在栈**之前**（结尾只剩一串 `at …`
// 与 `Node.js v24.x`），只取尾部会让用户看到一堆栈帧——实测从仓库根起核心建 dsh 会话正是
// 这个形态；而 Python 把原因打在**最后一行**。所以先找"原因行"并从它开始取，取不满时回退成
// 尾部窗口，两种形态都能落进窗口。
//
// 尾巴在协议侧 TailLines 出口已过 redact 闸门，这里再经 code.Error 过一次，不必自行脱敏。
func stderrMsgSuffix(tail []string) string {
	lines := make([]string, 0, len(tail))
	for _, l := range tail {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	start := len(lines) - stderrMsgLines
	if start < 0 {
		start = 0
	}
	if i := lastCauseIndex(lines); i >= 0 {
		// 从原因行开始给（紧随其后的通常就是栈的开头，够定位）；取不满 5 行时把窗口往前挪，
		// 而不是让窗口越界——Python 这类"原因在最后一行"就靠这条回退拿到完整上下文。
		start = i
		if start+stderrMsgLines > len(lines) {
			start = len(lines) - stderrMsgLines
		}
		if start < 0 {
			start = 0
		}
	}
	window := lines[start:]
	if len(window) > stderrMsgLines {
		window = window[:stderrMsgLines]
	}
	out := make([]string, 0, len(window))
	for _, l := range window {
		out = append(out, truncateRunes(l, stderrMsgRunes))
	}
	return "底座 stderr 尾部: " + strings.Join(out, " | ")
}

// lastCauseIndex 取**最后**一条"原因行"的下标（-1 = 没认出来）。
//
// 取最后而不是最先：底座可能先打一串噪声/告警再报最终失败，最先那条常常不是死因。
func lastCauseIndex(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if stderrCauseRe.MatchString(lines[i]) {
			return i
		}
	}
	return -1
}

// truncateRunes 按字符（不是字节）截断，超出部分用 … 收尾。
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ——— 构造面：调用方只写"这是什么错"，不写协议怎么描述它 ———

// Class 是平台自己的**可操作错误分类**（重试 / 登录 / 改参数 / 降级）。
//
// 它**不是**协议分类的别名：内圈写 `code.ClassRequest`，适配器边界做同值转换（协议侧的
// `acp.Class` 与本词汇逐个同值）。取值与协议词汇一一对应，由
// `TestErrorClassVocabularyMatchesACP` 逐条钉住——两份词汇分叉的表现是"这个错该重试还是该登录"
// 在控制器与领域两侧给出不同答案，而两边各自的测试都绿。
type Class string

const (
	// ClassConnection 拉起/传输层故障（可重试）→ 50201。
	ClassConnection Class = "connection"
	// ClassAccess 鉴权/权限不足（登录）→ 40101。
	ClassAccess Class = "access"
	// ClassLimit 配额/并发限制（等一等再试）→ 40901。
	ClassLimit Class = "limit"
	// ClassRequest 请求被拒（参数/语义错误）→ 40001（少数 code 提档 40401）。
	ClassRequest Class = "request_rejected"
	// ClassService 平台/底座侧内部错误 → 50001（少数 code 映射见 classCodeOverrides）。
	ClassService Class = "service"
	// ClassUnsupported 该形态不支持 → 40501。
	ClassUnsupported Class = "unsupported"
	// ClassUnknown 未归类（保留与协议词汇同宽）。
	ClassUnknown Class = "unknown"
)

// NewError 按平台分类造一个带业务语义的错误（cause 可为 nil）。
//
// 这是**唯一**把平台分类变成错误的构造入口：其余构造入口（RequestError 等）都走它。
// 返回值是平台载体 `*Coded`——协议错误类型不再出现在内圈（B4）。
func NewError(class Class, name, msg string, cause error) error {
	return &Coded{Class: class, Code: name, Details: msg, Err: cause}
}

// RequestError 请求侧错误：调用方改参数/换资源后可以重试（Class=request）。
func RequestError(name, msg string) error {
	return NewError(ClassRequest, name, msg, nil)
}

// LimitError 与当前状态冲突或超出额度（Class=limit → 40901）。
func LimitError(name, msg string) error {
	return NewError(ClassLimit, name, msg, nil)
}

// AccessError 鉴权/授权不通过（Class=access → 40101）。
func AccessError(name, msg string) error {
	return NewError(ClassAccess, name, msg, nil)
}

// UnsupportedError 当前形态明确不支持（Class=unsupported → 40501）。
func UnsupportedError(name, msg string) error {
	return NewError(ClassUnsupported, name, msg, nil)
}

// ServiceError 平台自身失败（Class=service → 50001/50201，按 codeOf 的例外表）。
func ServiceError(name, msg string) error {
	return NewError(ClassService, name, msg, nil)
}

// ConnectionError 与上游的连接建立/传输失败（Class=connection → 50201）。
func ConnectionError(name, msg string) error {
	return NewError(ClassConnection, name, msg, nil)
}

// WrapServiceError 同 ServiceError，但把底层错误挂成 cause（诊断面据此取 stderr 尾巴）。
//
// 为什么不直接传 `code.FromError(err)`：分类必须在**构造那一刻**就带上，控制器出口只做投影；
// 把裸 error 交给 FromError 会退化成 50000，把"上游连接失败"与"平台内部错误"混成一档。
func WrapServiceError(name, msg string, cause error) error {
	return NewError(ClassService, name, msg, cause)
}

// 编译期断言：平台带码错误与上游错误视图都是 error（共用同一套投影）。
var (
	_ error = (*Coded)(nil)
	_ error = (*RuntimeError)(nil)
)
