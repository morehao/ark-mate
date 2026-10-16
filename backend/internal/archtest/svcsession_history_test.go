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
	if !regexp.MustCompile(`func NewSessionCtr\(svc svcsession\.SessionSvc,[\s\S]*?src svcsession\.InteractionSource\) SessionCtr`).MatchString(ctrSrc) {
		t.Error("`ctrsession.NewSessionCtr` 的签名变了：它必须拿到**依赖事实**（会话面过渡端口 + 库句柄 + " +
			"会话域平面 + 事件总线 + 交互事实来源），两个读面用例都在函数体里造。")
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

	// ——— P4a 正文第四十刀：会话**详情读面**（详情 + 三份交互快照）也在这个包里 ———
	//
	// 与历史面同一形状（真用例：请求校验 + 错误码出口 + 响应装配），而且**四条出口共用一组投影**
	// ——详情响应里的 `pending`/`memories` 就是那两条列表的结果。所以"搬一半"在这里没有中间态：
	// 只搬三条会让 `GetSession` 那边留下一份一模一样的投影，两份实现的分叉方式正是本仓反复吃亏的
	// 那类静默失效（一份带上 `TimeoutAt`、另一份忘了；一份把 `mode` 判成 schema/url、另一份两个都回）。
	//
	// 这一段的判据沿用本守卫的家族口径（**扩面不增条数**，与本仓其它"同名守卫累积多面"的做法一致）。
	detailAnchors := []string{
		"func NewDetail(plane *session.Plane, src InteractionSource) DetailSvc",
		"var _ DetailSvc = (*Detail)(nil)",
		"func (d *Detail) GetSession(_ context.Context, id string) (*dtosession.SessionDetailResp, error)",
		"func (d *Detail) ListPendingPermissions(_ context.Context, id string) ([]dtosession.PermissionResp, error)",
		"func (d *Detail) ListPermissionMemories(_ context.Context, id string) ([]dtosession.MemoryResp, error)",
		"func (d *Detail) ListPendingElicitations(_ context.Context, id string) ([]dtosession.ElicitationResp, error)",
		// 四条出口共用的唯一错误码出口：各写一遍会让同一个原因出现几个文案。
		`"session_not_found"`,
		// 两个投影只许在这个包里存在（manager 侧那一份随本刀删除）。
		"func toPermissionResp(p interaction.PermissionItem) dtosession.PermissionResp",
		"func toElicitationResp(p interaction.ElicitationItem) dtosession.ElicitationResp",
		// 窄端口：状态归执行面持有，用例层只认这三条**只读**事实。
		"type InteractionSource interface {",
		"PendingPermissions(sessionID string) []interaction.PermissionItem",
		"PermissionMemories(sessionID string) []interaction.MemoryItem",
		"PendingElicitations(sessionID string) []interaction.ElicitationItem",
	}
	for _, a := range detailAnchors {
		if !strings.Contains(svcBody, a) {
			t.Errorf("`internal/service/svcsession` 里找不到 %q：详情读面被改名/搬走了"+
				"（判据挂在它身上，失锚等于空转）。", a)
		}
	}
	// ②′ 接口不许把这一面收回去（四条声明回到 `SessionSvc` = 同一个用例两条取用路径）。
	for _, dead := range []string{"GetSession(ctx context.Context", "ListPendingPermissions(ctx context.Context",
		"ListPermissionMemories(ctx context.Context", "ListPendingElicitations(ctx context.Context"} {
		if strings.Contains(iface, dead) {
			t.Errorf("`SessionSvc` 里又声明了 `%s`：这一面有自己的接口（`DetailSvc`），"+
				"收回去等于同一个用例有两条取用路径，按面切分当场失效。", dead)
		}
	}
	// ①′ manager 侧不得再有这四条实现与两个投影（**投影只该有一份**）。
	for _, dead := range []string{"func (s *Service) GetSession(", "func (s *Service) ListPendingPermissions(",
		"func (s *Service) ListPermissionMemories(", "func (s *Service) ListPendingElicitations(",
		"func toPermissionResp(", "func toElicitationResp("} {
		if strings.Contains(mgrBody, dead) {
			t.Errorf("`internal/manager` 里又出现 `%s`：详情读面的实现与投影只有一份，"+
				"在 `internal/service/svcsession`（P4a 正文第四十刀）。", dead)
		}
	}
	// ③′ 用例实例同样必须在控制器构造函数里造，且装配点必须交出**事实来源**。
	if !strings.Contains(body, "svcsession.NewDetail(") {
		t.Errorf("`NewSessionCtr` 的函数体里找不到 `svcsession.NewDetail(`：详情读面用例必须在控制器里造"+
			"（它要吃域平面 + 交互事实来源两个依赖事实）。实际体：\n%s", body)
	}
	if !strings.Contains(routesSrc, "ports.InteractionSource") {
		t.Error("`internal/router/routes_session.go` 没有把 `ports.InteractionSource` 交给 `NewSessionCtr`：" +
			"漏交会让交互事实来源为 nil，三份快照取数时 nil 接口调方法直接 panic。")
	}
	if !regexp.MustCompile(`InteractionSource\s+svcsession\.InteractionSource`).MatchString(
		stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "router", "router.go")))) {
		t.Error("`router.Ports` 里找不到 `InteractionSource svcsession.InteractionSource`（容空白）：" +
			"装配点交的该是这三条窄端口事实，而不是执行面本身（`Orchestrator` 的私有状态）。")
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
