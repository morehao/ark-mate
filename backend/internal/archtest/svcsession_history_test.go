package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P4a 正文第十刀：会话历史面在 `svcsession`，不在 `manager` ———
//
// 这一面前身是 `manager/sessions.go` 里的 `func (s *Service) SessionHistory`（2.5KB 的真实现：
// 游标哨兵、两条载体、首尾位次与 `has_more` 装配）。它是**第一刀动会话域**——会话面还有 18 个
// 方法留待逐刀搬，所以这一刀按**面**切接口（`HistorySvc` 与 `SessionSvc` 分开），
// 控制器同时持两面；不这样做就只能等 19 个方法全搬完才动，等于把这面继续挂在过渡端口上。
//
// 三种失效都不会让既有测试变红：
//
//	① **两份实现**：谁在 `manager` 里又留一个 `SessionHistory`（或把 `history_read_failed`
//	   这个错误码在那边再铸一次）——两份游标语义立刻分叉（一份把缺省 `after` 当 `0`、另一份当
//	   `-1`），而两边各自都有测试；
//	② **接口回潮**：`SessionHistory` 声明回 `SessionSvc`——控制器于是有两条路拿到它
//	   （`svc.SessionHistory` / `hist.SessionHistory`），"面"又没了；
//	③ **装配漏接**：`NewSessionCtr` 不再造 `svcsession.NewHistory(...)`（同一个包里的中间函数
//	   也能把整文件的字面量判据喂绿）——于是历史那半只剩测试里的替身。
//
// 判据四段，都 fail-closed。

// sessionHistoryAnchors 用例层必须有的实现锚。
var sessionHistoryAnchors = []string{
	"func NewHistory(st *store.Store, plane *session.Plane, bus *eventbus.Bus) HistorySvc",
	"var _ HistorySvc = (*History)(nil)",
	"func (h *History) SessionHistory(ctx context.Context, id string, q dtosession.HistoryQuery) (*dtosession.SessionHistoryResp, error)",
	// 位次哨兵：缺省 after = `-1`（早于一切事件），而 `0` 是合法位次——
	// 把它写成 `0` 会让"翻更早"那条路径静默丢掉第一条事件。
	"h.bus.EventsAfter(id, -1)",
	`"history_read_failed"`,
}

// TestSessionHistoryUseCaseLivesInSessionSvc 见上方注释。
func TestSessionHistoryUseCaseLivesInSessionSvc(t *testing.T) {
	root := backendRoot(t)
	svcDir := filepath.Join(root, "internal", "service", "svcsession")
	svcBody := dirProductionBody(t, svcDir)
	for _, a := range sessionHistoryAnchors {
		if !strings.Contains(svcBody, a) {
			t.Errorf("`internal/service/svcsession` 里找不到 %q：用例面被改名/搬走了"+
				"（判据挂在它身上，失锚等于空转）。", a)
		}
	}
	if !strings.Contains(svcBody, `"github.com/morehao/ark-mate/internal/domain/session"`) {
		t.Error("`internal/service/svcsession` 没有 import `internal/domain/session`：" +
			"历史读面要用域平面的会话存在性，不能自己再实现一遍。")
	}

	// ② 接口不许把这一面收回去（一句话里两处锚：声明在 `SessionSvc` 里 = 两条路）。
	iface := interfaceBodyOf(t, readFileOrFatal(t, filepath.Join(svcDir, "session.go")), "type SessionSvc interface {")
	if strings.Contains(iface, "SessionHistory(") {
		t.Error("`SessionSvc` 里又声明了 `SessionHistory(`：这一面有自己的接口（`HistorySvc`），" +
			"收回去等于同一个用例有两条取用路径，按面切分当场失效。")
	}
	// 反向锚（防"接口体读成空也绿"）：只取**留在用例层**的真用例——
	// 读模型（列表 / 单条 / 轮次 / 排队输入 / 检索 / 用量）刻意不在 `SessionSvc` 里
	// （见 `svcsession_readface_test.go`），所以锚不能取它们。
	if !strings.Contains(iface, "CreateSession(") && !strings.Contains(iface, "PromptSession(") {
		t.Errorf("`SessionSvc` 的接口体里找不到任何已知方法：判据失锚（接口被改名/搬走了？）。实际体：\n%s", iface)
	}

	// ① manager 侧不得再有这份实现。
	mgrBody := dirProductionBody(t, filepath.Join(root, "internal", "manager"))
	if strings.Contains(mgrBody, "func (s *Service) SessionHistory(") {
		t.Error("`internal/manager` 里又出现 `func (s *Service) SessionHistory(`：历史读面的实现" +
			"只有一份，在 `internal/service/svcsession`（P4a 正文第十刀）。")
	}
	if strings.Contains(mgrBody, `"history_read_failed"`) {
		t.Error("`internal/manager` 里又出现 `history_read_failed`：错误码出口跟实现走，" +
			"两边各铸一次会让同一个失败在两条路径上给出不同的码。")
	}

	// ③ 用例实例必须在**控制器的构造函数里**造（判据钉在函数体上，不是整文件）。
	ctrSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrsession", "session.go")))
	if !regexp.MustCompile(`func NewSessionCtr\(svc svcsession\.SessionSvc, st \*store\.Store, plane \*session\.Plane, bus \*eventbus\.Bus\) SessionCtr`).MatchString(ctrSrc) {
		t.Error("`ctrsession.NewSessionCtr` 的签名变了：它必须拿到**依赖事实**（会话面过渡端口 + 库句柄 + " +
			"会话域平面 + 事件总线），历史读面在函数体里造。")
	}
	body := funcBodyOf(t, ctrSrc, "func NewSessionCtr(")
	if !strings.Contains(body, "svcsession.NewHistory(") {
		t.Errorf("`NewSessionCtr` 的函数体里找不到 `svcsession.NewHistory(`：用例实例必须在控制器里造"+
			"（交给 `sessionCtr` 的必须是**用例**，不是域平面与总线的散装组合）。实际体：\n%s", body)
	}
	routesSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "router", "routes_session.go")))
	if !strings.Contains(routesSrc, "ports.Store, ports.SessionPlane, ports.Bus") {
		t.Error("`internal/router/routes_session.go` 没有把三个依赖事实交给 `NewSessionCtr`：" +
			"漏交会让历史读面的库句柄/平面/总线为 nil，取数时 nil panic。")
	}
}

// interfaceBodyOf 取出 `type X interface {` 到顶格 `}` 之间的正文（含方法签名行）。
//
// fail-closed：找不到起始锚即 Fatal。
func interfaceBodyOf(t *testing.T, src, header string) string {
	t.Helper()
	i := strings.Index(src, header)
	if i < 0 {
		t.Fatalf("找不到接口头 %q：判据失锚（接口被改名/搬走了？）", header)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n}\n"); j >= 0 {
		return rest[:j]
	}
	if j := strings.Index(rest, "\n}"); j >= 0 {
		return rest[:j]
	}
	t.Fatalf("接口 %q 没有结束花括号：源码解析失败", header)
	return ""
}
