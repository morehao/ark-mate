package code

import (
	"regexp"
	"strings"

	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/golib/gerror"
)

// notFoundCodes 是 acl 层"目标不存在"这一类稳定 code。
//
// 它们落在 acl.ClassRequest 里（对上游而言确实是一次被拒的请求），但对调用方
// 而言语义是 404：再改参数也不会有结果，得先把资源建好。故单独提档到 NotFound，
// 让前端能按 40401 直接引导"去注册 agent / 去建 runtime"，而不是当成参数错误。
var notFoundCodes = map[string]bool{
	"agent_not_found":        true,
	"conversation_not_found": true,
	"runtime_not_found":      true,
	"mcp_not_found":          true,
	"skill_not_found":        true,
	"provider_not_found":     true,
	"automation_not_found":   true,
}

// fromACL 把 acl 层错误按其 Class 映射为业务码。
//
// 这张表是 acl 分类（可操作语义：重试/登录/换参数/降级）到对外码的单向投影，
// 因此**放在 code 包**而不是 acl 包：acl 是可复用的 ACP 客户端，不该知道
// ark-forge 的信封编号；反过来 code 包作为"编号唯一事实源"必须收下这张表，
// 否则同一个 acl 错误在控制器与领域层会映射成两个不同的码。
func fromACL(err error) (gerror.Error, bool) {
	ae, ok := acl.As(err)
	if !ok {
		return gerror.Error{}, false
	}
	c := UpstreamFailed
	switch ae.Class {
	case acl.ClassRequest:
		c = InvalidRequest
		if notFoundCodes[ae.Code] {
			c = NotFound
		}
	case acl.ClassLimit:
		c = Conflict
	case acl.ClassAccess:
		c = Unauthorized
	case acl.ClassUnsupported:
		c = NotSupported
	case acl.ClassService, acl.ClassConnection:
		c = BadGateway
	}
	// acl 的 Details 是脱敏后给用户看的一句话；缺省回落到稳定 code，
	// 保证 msg 永不为空（空 msg 在前端会渲染成一个没有内容的错误气泡）。
	msg := ae.Details
	if msg == "" {
		msg = ae.Code
	}
	if s := stderrMsgSuffix(ae.StderrTail); s != "" {
		// SDK 把"底座进程没了"裹成 JSON-RPC -32603 的通用文案（字面量 `Internal error`，
		// 见 acp-go-sdk errors.go）。这句话单独摆给用户没有信息量，而尾巴里有——换掉它。
		// 若将来 SDK 换了文案，这里只是不再替换（退回它自己的话），不会出错。
		if msg == "Internal error" {
			msg = "底座未完成 ACP 握手就断开"
		}
		msg += "；" + s
	}
	return Error(c, msg), true
}

// 信封文案里底座 stderr 的量纲（**与 acl 侧的尾巴上限不是一回事**）：
// acl 保留 200 行是给事件/诊断面板那种结构化字段用的；而 msg 会进前端错误气泡、访问日志
// 与用户截图，两者差一个数量级，所以这里另设一个更小的上限。
const (
	stderrMsgLines = 5   // 最多带几行
	stderrMsgRunes = 300 // 每行最多几个字符（rune，避免截断多字节）
)

// stderrCauseRe 认出"这一行就是失败原因"的常见形态：Node 的 `Error: <原因>`、Go 的 `panic:`、
// Python 的 `XxxError:`/`Traceback`、Java 的 `Exception in thread`、Rust 的 `error[E…]`，
// 以及通用的 `fatal:` / 日志里的 `ERROR:`。
var stderrCauseRe = regexp.MustCompile(`(?i)^\s*(error|fatal|panic|traceback|exception)\b|\b(error|exception)s?\s*:`)

// stderrMsgSuffix 把 acl 采集到的底座 stderr 尾巴并进对外文案。
//
// 为什么必须并：底座起不来时，ACP SDK 只回一句通用的 `Internal error`
// （JSON-RPC -32603 的固定文案，见 acp-go-sdk errors.go），真因在子进程 stderr 里
// （典型如 dsh 的 `.env` 保留键报错）。不并的话用户只看到 `50001 Internal error`，
// 完整原因却躺在核心日志里——排障得先知道去翻日志。
//
// **选行规则是先因后果，不是简单取尾部**：Node 把原因打在栈**之前**（结尾只剩一串 `at …`
// 与 `Node.js v24.x`），只取尾部会让用户看到一堆栈帧——实测从仓库根起核心建 dsh 会话正是
// 这个形态；而 Python 把原因打在**最后一行**。所以先找"原因行"并从它开始取，取不满时回退成
// 尾部窗口，两种形态都能落进窗口。
//
// 尾巴在 acl.TailLines 出口已过 pkg/redact 闸门，这里再经 code.Error 过一次，不必自行脱敏。
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
