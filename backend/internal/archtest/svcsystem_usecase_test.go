package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ——— P4a 正文第一刀「导出用例归属」守卫 ———
//
// `ExportSessions` 是 P4a 正文里**第一个真正"收实现"**的动作：它从 `manager/export.go`
// （295 行，`*Service` 的方法）搬进 `internal/app/system`（P4a 新建的用例层包），
// 形状也换了——不再是一个大结构体的方法，而是
//
//	`system.Exporter`（用例：校验 / 错误码出口 / 响应装配 / 渲染）
//	  ← `system.Source` 端口（两个方法：会话元数据、事件序列）
//	      ← `manager` 的适配器（`export_source.go`：sqlite 走库、memory 走会话域镜像 + 事件总线回放）
//
// 这是 P4a 之后所有"收实现"的**模板**，所以它的两种失效都要钉住：
//
//	① **实现回流**：谁在 `manager` 里再写一遍校验或渲染（"顺手"最可能发生的地方就是它原来的文件），
//	   于是两份实现静默分叉——渲染口径（哪种事件渲染成什么标题）与上限口径各说各话，
//	   而两边都编译得过；
//	② **装配漏接**：`app.go` 忘了 `system.New(a.svc)`（或交的是 `a.svc`）——那会**编译不过**
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
	impl := filepath.Join(root, "internal", "app", "system", "export.go")
	src := stripLineComments(readFileOrFatal(t, impl))
	for _, a := range exportUseCaseAnchors {
		if !strings.Contains(src, a) {
			t.Errorf("internal/app/system/export.go 里找不到 %q：导出用例的实现被改名/搬走了？"+
				"（这条守卫的判据就挂在它身上，失锚等于空转）", a)
		}
	}

	// 端口必须真的在（`Source` 是这一刀的关键：用例层不认识 store/domain）。
	sourceSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "app", "system", "source.go")))
	for _, a := range []string{"type Source interface {", "SessionMeta(ctx context.Context, id string) (SessionMeta, bool)", "SessionEvents(ctx context.Context, id string, limit int) (events []eventbus.Event, truncated bool)"} {
		if !strings.Contains(sourceSrc, a) {
			t.Errorf("internal/app/system/source.go 里找不到 %q：端口被改名/搬走了？", a)
		}
	}
	// 用例**本体**不得 import 持久层（那正是端口的用途）。
	//
	// P4a 正文第八刀起这条判据只压在 `export.go` 上：事实来源的适配器（`source_facts.go`）
	// 与用例同包，而它**必须**读库——把 store 的禁令留在整包上，等于逼适配器搬到别处去
	// （那正是"实现与它的名字分家"的老毛病）。禁止项拆成两半：整包禁 `manager`（用例层
	// 不认识过渡层），`export.go` 另禁 `store`。
	if strings.Contains(src+sourceSrc, `"github.com/morehao/ark-mate/internal/sessionruntime"`) {
		t.Error("`internal/app/system` 里出现 `internal/sessionruntime`：用例层通过**端口**取事实（ADR-6），" +
			"它不认识过渡层。")
	}
	if strings.Contains(src, `"github.com/morehao/ark-mate/internal/persistence/store"`) {
		t.Error("`internal/app/system/export.go` 里出现 `internal/persistence/store`：用例本体只认 `Source` 端口，" +
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
					"在 `internal/app/system`（P4a 正文第一刀）。\n"+
					"manager 只留适配器（`export_source.go`：把库/内存镜像/事件总线读成 `Source` 要的形状）。\n"+
					"在这里再写一遍的后果是渲染口径与上限口径静默分叉，而两边都编译得过。",
					filepath.Base(path), m)
			}
		}
	}

	// 装配：用例实例只在 `internal/bootstrap/assembly` 造；控制器只收接口（v3.1 §3.4）。
	asm := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "bootstrap", "assembly", "services.go")))
	if !strings.Contains(asm, "Export:             system.New(") &&
		!strings.Contains(asm, "system.New(") {
		t.Error("`internal/bootstrap/assembly/services.go` 里找不到 `system.New(`：" +
			"导出的事实来源与用例实例必须在唯一装配根造。")
	}
	ctr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "delivery", "controller", "ctrexport", "export.go")))
	if strings.Contains(ctr, "system.New") {
		t.Error("`ctrexport` 里又出现 `system.New`：控制器只收接口，不再造用例。")
	}
	// 按 **import 路径**判（C3 4.2 起用例包不再叫 `svcsystem`，`system.` 这种限定符文本
	// 在应用壳里已经指不到用例层了——按文本判会变成空转）。
	for _, imp := range importedByProductionPackage(t, filepath.Join(root, "internal", "bootstrap", "app")) {
		if strings.HasPrefix(imp, modulePrefix+"internal/app/") {
			t.Errorf("`internal/bootstrap/app` import 了用例层 %s：应用壳只消费 `assembly.BuildResult`。", imp)
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
		t.Error("`internal/sessionruntime/export.go` 又存在了：导出的实现归 `internal/app/system`" +
			"（P4a）。\n两边并存的表现是**渲染口径与上限口径分叉**——例如一边按 4 MiB 截断、" +
			"另一边没有，而两条路径都编译得过。")
	}
}

// systemSourceAdapterAnchors 事实来源适配器必须有的锚（在**用例层**，与用例同包）。
//
// 判据挂在具体符号上而不只挂"包里有 store 的 import"：后者在适配器被换成一层薄转发
// （例如把 `SessionEvents` 委托给别处）时照样通过，而那正是要拦的形态。
var systemSourceAdapterAnchors = []string{
	"func NewSource(st SourceFactsReader, plane *session.Plane, bus *eventbus.Bus) Source",
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
//	② `Ports` 那一格写回 svc 类型（`system.Source`）：装配点为了声明一格而 import 用例包，
//	   "只说依赖事实"这条口径当场破掉；
//	③ 用例层丢了适配器锚（改名/搬走）：判据失锚，守卫会静默空转。
func TestSystemSourceAdapterLivesInSystemSvc(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "app", "system")
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
			t.Errorf("`internal/app/system` 里找不到 %q：事实来源的适配器被改名/搬走了"+
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
					"`internal/app/system`（P4a 正文第八刀）。", filepath.Base(path), m)
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
