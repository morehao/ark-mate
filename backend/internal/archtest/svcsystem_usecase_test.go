package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P4a 正文第一刀「导出用例归属」守卫 ———
//
// `ExportSessions` 是 P4a 正文里**第一个真正"收实现"**的动作：它从 `manager/export.go`
// （295 行，`*Service` 的方法）搬进 `internal/service/svcsystem`（P4a 新建的用例层包），
// 形状也换了——不再是一个大结构体的方法，而是
//
//	`svcsystem.Exporter`（用例：校验 / 错误码出口 / 响应装配 / 渲染）
//	  ← `svcsystem.Source` 端口（两个方法：会话元数据、事件序列）
//	      ← `manager` 的适配器（`export_source.go`：sqlite 走库、memory 走会话域镜像 + 事件总线回放）
//
// 这是 P4a 之后所有"收实现"的**模板**，所以它的两种失效都要钉住：
//
//	① **实现回流**：谁在 `manager` 里再写一遍校验或渲染（"顺手"最可能发生的地方就是它原来的文件），
//	   于是两份实现静默分叉——渲染口径（哪种事件渲染成什么标题）与上限口径各说各话，
//	   而两边都编译得过；
//	② **装配漏接**：`app.go` 忘了 `svcsystem.New(a.svc)`（或交的是 `a.svc`）——那会**编译不过**
//	   （`*Service` 不再实现 `SystemSvc`），所以这一条是"响亮"的；但**端口漏接**是静默的：
//	   `Source` 为 nil 时 `ExportSessions` 会在第一次取数时 nil panic，而单测若只测校验分支就看不见。
//
// 反证（实跑）：把渲染函数挪回 `manager` → 红；把 `invalid_export_format` 的错误出口挪回 `manager` → 红。

// exportUseCaseAnchors 用例层必须有的实现锚（**私有实现**，不是薄转发——见 §6.1 的锚点规则）。
var exportUseCaseAnchors = []string{
	"func (e *Exporter) ExportSessions(",
	"var _ SystemSvc = (*Exporter)(nil)",
	"func renderExport(",
	"func exportFilename(",
	`"invalid_export_format"`,
	`"export_too_many_ids"`,
}

// exportImplementationMarkers 实现回流到 `manager` 的痕迹（校验口径 + 渲染口径）。
var exportImplementationMarkers = []string{
	`"invalid_export_format"`,
	`"export_too_many_ids"`,
	"func renderExport(",
	"func exportFilename(",
	`"## 用户\n\n%s\n\n"`, // 渲染 Markdown 用户段的格式串
}

// TestExportUseCaseLivesInSystemSvc 见文件头。
func TestExportUseCaseLivesInSystemSvc(t *testing.T) {
	root := backendRoot(t)
	impl := filepath.Join(root, "internal", "service", "svcsystem", "export.go")
	src := stripLineComments(readFileOrFatal(t, impl))
	for _, a := range exportUseCaseAnchors {
		if !strings.Contains(src, a) {
			t.Errorf("internal/service/svcsystem/export.go 里找不到 %q：导出用例的实现被改名/搬走了？"+
				"（这条守卫的判据就挂在它身上，失锚等于空转）", a)
		}
	}

	// 端口必须真的在（`Source` 是这一刀的关键：用例层不认识 store/domain）。
	sourceSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "service", "svcsystem", "source.go")))
	for _, a := range []string{"type Source interface {", "SessionMeta(ctx context.Context, id string) (SessionMeta, bool)", "SessionEvents(ctx context.Context, id string, limit int) (events []eventbus.Event, truncated bool)"} {
		if !strings.Contains(sourceSrc, a) {
			t.Errorf("internal/service/svcsystem/source.go 里找不到 %q：端口被改名/搬走了？", a)
		}
	}
	// 用例**本体**不得 import 持久层（那正是端口的用途）。
	//
	// P4a 正文第八刀起这条判据只压在 `export.go` 上：事实来源的适配器（`source_facts.go`）
	// 与用例同包，而它**必须**读库——把 store 的禁令留在整包上，等于逼适配器搬到别处去
	// （那正是"实现与它的名字分家"的老毛病）。禁止项拆成两半：整包禁 `manager`（用例层
	// 不认识过渡层），`export.go` 另禁 `store`。
	if strings.Contains(src+sourceSrc, `"github.com/morehao/ark-mate/internal/sessionruntime"`) {
		t.Error("`internal/service/svcsystem` 里出现 `internal/sessionruntime`：用例层通过**端口**取事实（ADR-6），" +
			"它不认识过渡层。")
	}
	if strings.Contains(src, `"github.com/morehao/ark-mate/internal/store"`) {
		t.Error("`internal/service/svcsystem/export.go` 里出现 `internal/store`：用例本体只认 `Source` 端口，" +
			"读库是适配器（`source_facts.go`）的事。")
	}

	// manager 侧：不得再有实现痕迹。
	dir := filepath.Join(root, "internal", "sessionruntime")
	files := productionGoFiles(t, dir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", dir)
	}
	for _, path := range files {
		body := stripLineComments(readFileOrFatal(t, path))
		for _, m := range exportImplementationMarkers {
			if strings.Contains(body, m) {
				t.Errorf("internal/sessionruntime/%s 里出现导出用例的实现痕迹 %q：它**只有一份实现**，"+
					"在 `internal/service/svcsystem`（P4a 正文第一刀）。\n"+
					"manager 只留适配器（`export_source.go`：把库/内存镜像/事件总线读成 `Source` 要的形状）。\n"+
					"在这里再写一遍的后果是渲染口径与上限口径静默分叉，而两边都编译得过。",
					filepath.Base(path), m)
			}
		}
	}

	// 装配：用例实例必须在**控制器**里造（P4a 收口后 `app` 不认识 `service/svc*`）。
	//
	// 这条判据的位置换过一次：第一版钉的是 "`app.go` 里有 `svcsystem.New(`"，那是当时的事实。
	// 收口之后事实变成"`app.go` 交出的是**事实来源**、用例实例由 `ctrexport` 造"——判据跟着
	// 事实走，否则它会一边保护一个已经不成立的形状、一边放过真正的失效（没人造用例 ⇒ 端口为 nil）。
	ctr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrexport", "export.go")))
	if !strings.Contains(ctr, "svcsystem.New(") {
		t.Error("`internal/controller/ctrexport/export.go` 里找不到 `svcsystem.New(`：用例实例没人造。" +
			"`Source` 为 nil 时 `ExportSessions` 会在第一次取数时 nil panic——" +
			"而只测校验分支的单测看不见它。")
	}
	app := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "app", "app.go")))
	if strings.Contains(app, "svcsystem.") {
		t.Error("`internal/app/app.go` 里出现 `svcsystem.`：装配点不认识用例层（P4a 收口）。" +
			"它该交出的是**依赖事实**（`SessionPlane: a.svc.SessionPlane(), Bus: a.bus`），" +
			"事实来源与用例实例都由 `ctrexport` 造。")
	}
	// P4a 正文第八刀：适配器搬进用例层、Ports 换成三份事实。
	//
	// 这三种失效都不会让别的测试变红：
	//
	//	① 适配器回流到 `manager`（两份取数，sqlite/memory 的产物口径静默分叉）；
	//	② `Ports` 那一格写回 `svcsystem.Source`（router 又 import 用例包）；
	//	③ 控制器不再造事实来源（`Source` 为 nil ⇒ 第一次取数 nil panic，而只测校验分支看不见）。
	portsSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "router", "router.go")))
	if !regexp.MustCompile(`SessionPlane\s+\*session\.Plane`).MatchString(portsSrc) ||
		!regexp.MustCompile(`Bus\s+\*eventbus\.Bus`).MatchString(portsSrc) {
		t.Error("`internal/router/router.go` 的 `Ports` 里找不到 `SessionPlane *session.Plane` 与 " +
			"`Bus *eventbus.Bus`：导出的事实来源靠这两份依赖事实，用例与来源都在 `ctrexport` 里造。")
	}
	if strings.Contains(portsSrc, "svcsystem") {
		t.Error("`internal/router/router.go` 里出现 `svcsystem`：装配点只放依赖事实，" +
			"用例层的类型不外溢到 router。")
	}
	// 判据钉在 **`NewExportCtr` 的函数体**里，而不是整文件的字面量。
	//
	// 这条判据的第一版是整文件 `strings.Contains(ctr, "svcsystem.NewSource(")`，**实测假绿**：
	// 把构造挪进同包一个中间函数（`newSystemSource(st, plane, bus)`）之后，字面量仍在文件里，
	// 守卫照样通过——而那正是"一行转发"这条规矩要拦的形态。函数体是能被解析出来的，就按它判。
	body := funcBodyOf(t, ctr, "func NewExportCtr(")
	for _, a := range []string{"svcsystem.New(", "svcsystem.NewSource("} {
		if !strings.Contains(body, a) {
			t.Errorf("`internal/controller/ctrexport/export.go` 的 `NewExportCtr` 函数体里找不到 %q："+
				"三份依赖事实必须在**这里**造出事实来源与用例（挪进同包的中间函数 = 一行转发，"+
				"而跳过构造 ⇒ `Source` 为 nil，只测校验分支的单测看不见）。", a)
		}
	}
}

// TestManagerExportFileIsGone 旧实现文件不得复活。
//
// 它比"文件里的痕迹"更硬：`manager/export.go` 回来 = 两份实现并存的起点。
func TestManagerExportFileIsGone(t *testing.T) {
	root := backendRoot(t)
	old := filepath.Join(root, "internal", "sessionruntime", "export.go")
	if _, err := os.Stat(old); err == nil {
		t.Error("`internal/sessionruntime/export.go` 又存在了：导出的实现归 `internal/service/svcsystem`" +
			"（P4a）。\n两边并存的表现是**渲染口径与上限口径分叉**——例如一边按 4 MiB 截断、" +
			"另一边没有，而两条路径都编译得过。")
	}
}

// systemSourceAdapterAnchors 事实来源适配器必须有的锚（在**用例层**，与用例同包）。
//
// 判据挂在具体符号上而不只挂"包里有 store 的 import"：后者在适配器被换成一层薄转发
// （例如把 `SessionEvents` 委托给别处）时照样通过，而那正是要拦的形态。
var systemSourceAdapterAnchors = []string{
	"func NewSource(st *store.Store, plane *session.Plane, bus *eventbus.Bus) Source",
	"var _ Source = (*Facts)(nil)",
	"func (f *Facts) SessionMeta(ctx context.Context, id string) (SessionMeta, bool)",
	"func (f *Facts) SessionEvents(ctx context.Context, id string, limit int) ([]eventbus.Event, bool)",
	"session.DecodePayload(",
}

// TestSystemSourceAdapterLivesInSystemSvc 事实来源的适配器归用例层（P4a 正文第八刀）。
//
// 它挡的是三种静默失效：
//
//	① 适配器回流 `manager`（`func (s *Service) SessionEvents(` 回来）：两份取数的产物口径会分叉，
//	   而两边都编译得过、都有一套自己的测试；
//	② `Ports` 那一格写回 svc 类型（`svcsystem.Source`）：装配点为了声明一格而 import 用例包，
//	   "只说依赖事实"这条口径当场破掉；
//	③ 用例层丢了适配器锚（改名/搬走）：判据失锚，守卫会静默空转。
func TestSystemSourceAdapterLivesInSystemSvc(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "service", "svcsystem")
	files := goFilesUnder(t, dir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有 .go 文件：守卫失去检查对象", dir)
	}
	var body strings.Builder
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body.WriteString(stripLineComments(readFileOrFatal(t, path)))
	}
	src := body.String()
	for _, a := range systemSourceAdapterAnchors {
		if !strings.Contains(src, a) {
			t.Errorf("`internal/service/svcsystem` 里找不到 %q：事实来源的适配器被改名/搬走了"+
				"（它是这一刀的判据本身，失锚等于空转）。", a)
		}
	}

	// ① 适配器不得回流 manager。
	mgr := filepath.Join(root, "internal", "sessionruntime")
	mgrFiles := productionGoFiles(t, mgr)
	if len(mgrFiles) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", mgr)
	}
	for _, path := range mgrFiles {
		mgrBody := stripLineComments(readFileOrFatal(t, path))
		for _, m := range []string{"func (s *Service) SessionMeta(", "func (s *Service) SessionEvents("} {
			if strings.Contains(mgrBody, m) {
				t.Errorf("internal/sessionruntime/%s 里出现 %q：事实来源的适配器只有一份，在 "+
					"`internal/service/svcsystem`（P4a 正文第八刀）。", filepath.Base(path), m)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(mgr, "export_source.go")); err == nil {
		t.Error("`internal/sessionruntime/export_source.go` 又存在了：两份取数口径（sqlite/memory）会静默分叉。")
	}
}

// funcBodyOf 取出以 `sig` 开头的那个**顶格函数**的函数体（到下一个顶格 `}` 为止）。
//
// 与 `interfaceBody` 同一手法：判据要落在函数体上时，先把体切出来——否则同包里的中间函数、
// 或另一个函数里的同名字面量都会把守卫喂绿（`manager_lock_order_test.go` 的函数边界那套同理）。
// 找不到 `sig` 即 Fatal：判据失锚不许静默。
func funcBodyOf(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("找不到 %q：判据失锚（函数被改名/搬走了）", sig)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n}"); j >= 0 {
		return rest[:j]
	}
	t.Fatalf("%q 的函数体没有顶格 `}` 收尾：源码形态变了，判据需要跟着改", sig)
	return ""
}
