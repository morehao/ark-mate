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
	// 用例层不得 import 持久层或领域（那正是端口的用途）。
	for _, banned := range []string{`"github.com/morehao/ark-mate/internal/store"`, `"github.com/morehao/ark-mate/internal/manager"`} {
		if strings.Contains(sourceSrc+src, banned) {
			t.Errorf("`internal/service/svcsystem` 里出现 %s：用例层通过**端口**取事实（ADR-6），"+
				"它不 import 持久层、也不 import `manager`。", banned)
		}
	}

	// manager 侧：不得再有实现痕迹。
	dir := filepath.Join(root, "internal", "manager")
	files := productionGoFiles(t, dir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", dir)
	}
	for _, path := range files {
		body := stripLineComments(readFileOrFatal(t, path))
		for _, m := range exportImplementationMarkers {
			if strings.Contains(body, m) {
				t.Errorf("internal/manager/%s 里出现导出用例的实现痕迹 %q：它**只有一份实现**，"+
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
			"它该交出的是**事实来源**（`System: a.svc`），用例实例由 `ctrexport` 造。")
	}
}

// TestManagerExportFileIsGone 旧实现文件不得复活。
//
// 它比"文件里的痕迹"更硬：`manager/export.go` 回来 = 两份实现并存的起点。
func TestManagerExportFileIsGone(t *testing.T) {
	root := backendRoot(t)
	old := filepath.Join(root, "internal", "manager", "export.go")
	if _, err := os.Stat(old); err == nil {
		t.Error("`internal/manager/export.go` 又存在了：导出的实现归 `internal/service/svcsystem`" +
			"（P4a）。\n两边并存的表现是**渲染口径与上限口径分叉**——例如一边按 4 MiB 截断、" +
			"另一边没有，而两条路径都编译得过。")
	}
}
