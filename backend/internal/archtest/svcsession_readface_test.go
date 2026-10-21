package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P4a 正文第十一刀：**读模型不进用例层**（会话列表面）———
//
// 会话面里有一类方法和"用例"形状不同：列表 / 单条 / 轮次 / 排队输入 / 检索 / 用量。
// 它们没有请求校验、没有错误码出口、没有响应装配，而且实现与**域自己的锁与状态**绑死
// ——`Plane.ListSessions` 要持 `pl.mu` 遍历注册表、释放锁后再补齐归属展示字段。
//
// 给它们造一层用例只会得到**一行转发**（`return s.sessionPlane.ListSessions(...)`），
// 那正是 2026-10-16 被判定为"转发骚操作"的形状，也正是这一刀要拆掉的东西：
// `manager` 侧那句 `TODO(P4b): 删转发（会话面直接依赖 session.Plane）` 说的就是它。
//
// 于是这一面按 P4b 的原口径收口：**控制器直接持有域平面**（`ctrsession` 的 `plane` 字段），
// 读模型从域取；用例层只留真用例。这条判据把口径钉住，否则下一个人为了"层数一致"
// 会把转发再写回来——而它**不会让任何既有测试变红**（转发能编译、行为也对）。
//
// 三种失效：
//
//	① **转发回潮**：`SessionSvc` 里又出现 `ListSessions` / `SearchMessages` / `UsageSummary`
//	   （用例层成了纯转发；后两条见 P4a 正文第四十三刀）；
//	② **实现回流**：`manager` 里又出现 `func (s *Service) ListSessions(`；
//	③ **取用路径改错**：控制器不再经域平面取（或经同一个包里的中间函数绕开——
//	   整文件字面量判据会被那种写法喂绿，所以在**函数体**上判）。
//
// 判据四段，都 fail-closed。

// TestSessionReadFaceSkipsEmptyUseCaseLayer 见上方注释。
func TestSessionReadFaceSkipsEmptyUseCaseLayer(t *testing.T) {
	root := backendRoot(t)

	// ③′ 读模型本体必须还在域里（判据失锚的最坏表现是"守卫照样绿、什么都不检查"）。
	domBody := dirProductionBody(t, filepath.Join(root, "internal", "domain", "session"))
	for _, a := range []string{
		"func (pl *Plane) ListSessions(archived bool, cwd, workspaceID string) ([]SessionItem, error)",
		// 归属过滤的两条语义（ADR-6）与"持锁折叠、释放锁后补展示字段"的分界。
		"func workspaceMatches(recWorkspaceID, wantID, defaultID string) bool",
		"pl.fillWorkspaceDisplay(out)",
	} {
		if !strings.Contains(domBody, a) {
			t.Errorf("`internal/domain/session` 里找不到 %q：读模型本体被搬走/改名了"+
				"（这条守卫的判据挂在它身上，失锚等于空转）。", a)
		}
	}

	// ①② 同一判据按**读模型名单**跑一遍：名单在此单点声明。
	//
	// 读模型是**枚举得出**的（无校验 / 无错误码 / 无装配、实现与域锁绑死），不是"看着像"，
	// 所以名单写死在这里——新增一个读模型进 `SessionSvc` 会立刻红，而不是等人去发现。
	// 第十一刀后半场 +3：`SearchSessions` / `ListRuns` / `ListQueuedInputs`（前两个替身恒
	// 返回空、第三个替身返回固定行，路由测试改成真平面后都换成了真事实）。
	// 第四十三刀 +2：`SearchMessages` / `UsageSummary`——两条都是纯转发到域平面（检索的实现与
	// transcript 目录绑死、用量的校验与分组口径全在 `domain/session`），声明与转发随之删除。
	// 它们的控制器**不在** `ctrsession` 里，所以下面的"控制器取用路径"要扫三个文件。
	// v3.1 §3.2.1：控制器不得 import `internal/domain`，所以读模型有了**窄用例接口**
	// `session.ReadSvc`（会话列表/检索/轮次/待投递）+ `svcsearch` / `svcusage`。
	// 本体仍在域平面；这里两侧一起钉：域里有实体、用例接口有声明、控制器只走接口。
	readModels := []struct {
		plane, iface, call string
	}{
		{"ListSessions", "List(", "h.read.List("},
		{"SearchSessions", "Search(", "h.read.Search("},
		{"ListRuns", "Runs(", "h.read.Runs("},
		{"ListQueuedInputs", "QueuedInputs(", "h.read.QueuedInputs("},
		{"SearchMessages", "Messages(", "h.svc.Messages("},
		{"UsageSummary", "Summary(", "h.svc.Summary("},
	}
	ctrSrcAll := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "inbound", "http", "controller", "ctrsession", "session.go"))) +
		stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "inbound", "http", "controller", "ctrsearch", "search.go"))) +
		stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "inbound", "http", "controller", "ctrusage", "usage.go")))
	mgrAll := dirProductionBody(t, filepath.Join(root, "internal", "app", "run"))
	readIface := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "app", "session", "read.go")))
	searchIface := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "app", "search", "search.go")))
	usageIface := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "app", "usage", "usage.go")))
	for _, m := range readModels {
		if !strings.Contains(domBody, "func (pl *Plane) "+m.plane+"(") {
			t.Errorf("域平面里找不到 `func (pl *Plane) %s(`：读模型本体必须只有一份且在域里。", m.plane)
		}
		if !strings.Contains(readIface+searchIface+usageIface, m.iface) {
			t.Errorf("用例层找不到 `%s`：控制器只能经用例接口取，而该接口必须声明这个方法。", m.iface)
		}
		if strings.Contains(mgrAll, "func (s *Service) "+m.plane+"(") {
			t.Errorf("`internal/app/run` 里又出现 `func (s *Service) %s(`：那一行转发已按 P4b 口径删除。", m.plane)
		}
		if !strings.Contains(ctrSrcAll, m.call) {
			t.Errorf("控制器里没有 `%s` 调用点：读模型要经用例接口取（v3.1 §3.2.1）。", m.call)
		}
	}

	// ① `SessionSvc` 不得收读模型（它们进的是 `ReadSvc`）。
	iface := interfaceBodyOf(t, readFileOrFatal(t, filepath.Join(root, "internal", "app", "session", "session.go")), "type SessionSvc interface {")
	for _, m := range []string{"ListSessions(", "SearchSessions(", "ListRuns(", "ListQueuedInputs("} {
		if strings.Contains(iface, m) {
			t.Errorf("`SessionSvc` 里又声明了 `%s`：读模型归 `ReadSvc`，两个接口各说各话。", m)
		}
	}
	if !strings.Contains(iface, "CreateSession(") {
		t.Errorf("`SessionSvc` 的接口体里找不到 `CreateSession(`：判据失锚（接口被改名/搬走了？）。实际体：\n%s", iface)
	}

	// ② manager 侧不得再有这份实现。
	if strings.Contains(mgrAll, "func (s *Service) ListSessions(") {
		t.Error("`internal/app/run` 里又出现 `func (s *Service) ListSessions(`：它是一行转发，" +
			"读模型的实现只有一份且在域里（P4a 正文第十一刀）。")
	}

	// ③ 控制器只持用例接口。
	ctrSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "adapters", "inbound", "http", "controller", "ctrsession", "session.go")))
	if strings.Contains(ctrSrc, "*session.Plane") {
		t.Error("`ctrsession` 里又出现 `*session.Plane`：控制器不 import domain（v3.1 §3.2.1 规则 1）。")
	}
	if !regexp.MustCompile(`read\s+session\.ReadSvc`).MatchString(ctrSrc) {
		t.Error("`ctrsession` 里找不到 `read session.ReadSvc` 字段（容空白匹配）。")
	}
	body := funcBodyOf(t, ctrSrc, "func (h *sessionCtr) List(")
	if !strings.Contains(body, "h.read.List(") {
		t.Errorf("`sessionCtr.List` 的函数体里找不到 `h.read.List(`。实际体：\n%s", body)
	}
}
