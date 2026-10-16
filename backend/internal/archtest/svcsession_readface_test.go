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
//	① **转发回潮**：`SessionSvc` 里又出现 `ListSessions`（用例层成了纯转发）；
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
		"func (pl *Plane) ListSessions(archived bool, cwd, workspaceID string) ([]dtosession.SessionResp, error)",
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
	readModels := []string{"ListSessions", "SearchSessions", "ListRuns", "ListQueuedInputs"}
	ctrSrcAll := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrsession", "session.go")))
	mgrAll := dirProductionBody(t, filepath.Join(root, "internal", "manager"))
	ifaceAll := interfaceBodyOf(t, readFileOrFatal(t, filepath.Join(root, "internal", "service", "svcsession", "session.go")), "type SessionSvc interface {")
	for _, m := range readModels {
		if !strings.Contains(domBody, "func (pl *Plane) "+m+"(") {
			t.Errorf("域平面里找不到 `func (pl *Plane) %s(`：读模型本体必须只有一份且在域里。", m)
		}
		if strings.Contains(ifaceAll, m+"(") {
			t.Errorf("`SessionSvc` 里又声明了读模型 `%s(`：进用例层只能是一行转发（被判定为骚操作的形状）。", m)
		}
		if strings.Contains(mgrAll, "func (s *Service) "+m+"(") {
			t.Errorf("`internal/manager` 里又出现 `func (s *Service) %s(`：那一行转发已按 P4b 口径删除。", m)
		}
		if !strings.Contains(ctrSrcAll, "h.plane."+m+"(") {
			t.Errorf("`ctrsession` 里没有任何 `h.plane.%s(` 调用点：读模型要从域平面取，不能走转发。", m)
		}
	}

	// ① 用例层不得把读模型收成转发。
	iface := interfaceBodyOf(t, readFileOrFatal(t, filepath.Join(root, "internal", "service", "svcsession", "session.go")), "type SessionSvc interface {")
	if strings.Contains(iface, "ListSessions(") {
		t.Error("`SessionSvc` 里又声明了 `ListSessions(`：它是**读模型**（无校验、无错误码、无装配，" +
			"实现与域锁绑死），进用例层只能是一行转发——那正是被判定为骚操作的形状。" +
			"控制器直接经域平面取（`ctrsession.plane`）。")
	}
	if !strings.Contains(iface, "CreateSession(") {
		t.Errorf("`SessionSvc` 的接口体里找不到 `CreateSession(`：判据失锚（接口被改名/搬走了？）。实际体：\n%s", iface)
	}

	// ② manager 侧不得再有这份实现（TODO(P4b) 的转发已删）。
	mgrBody := dirProductionBody(t, filepath.Join(root, "internal", "manager"))
	if strings.Contains(mgrBody, "func (s *Service) ListSessions(") {
		t.Error("`internal/manager` 里又出现 `func (s *Service) ListSessions(`：它是一行转发，" +
			"读模型的实现只有一份且在域里（P4a 正文第十一刀）。")
	}

	// ③ 控制器的取用路径：判据钉在 `List` 的**函数体**上（同包中间函数喂不绿它）。
	ctrSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrsession", "session.go")))
	if !regexp.MustCompile(`plane\s+\*session\.Plane`).MatchString(ctrSrc) {
		t.Error("`ctrsession` 里找不到 `plane *session.Plane` 字段：读模型必须由域平面提供" +
			"（容空白匹配——gofmt 会把字段对齐成多空格，定长写法会假绿）。")
	}
	body := funcBodyOf(t, ctrSrc, "func (h *sessionCtr) List(")
	if !strings.Contains(body, "h.plane.ListSessions(") {
		t.Errorf("`sessionCtr.List` 的函数体里找不到 `h.plane.ListSessions(`："+
			"读模型要么走域平面、要么走一层转发，没有第三条路。实际体：\n%s", body)
	}
}
