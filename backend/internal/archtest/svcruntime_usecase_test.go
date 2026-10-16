package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P4a 正文第七刀「底座身份与诊断面归属」守卫 ———
//
// 这一刀的**实现**早在 P4a 收口前一批就进了 `internal/service/svcruntime`
// （`Runtime` + `Plane` 端口 + `svcruntime.New`），剩下的最后一格是**装配面**：
// `router.Ports.Runtime` 此前写着 `svcruntime.Plane`（一个 svc 接口类型），
// 于是 router 为了声明一格字段而 import 了 svc 包；同一格里装的其实是执行面本身
// （`*runtimeconn.Plane`）。改完之后 `app` 交的是**依赖事实**、用例在 `ctrruntime` 里造——
// 这是 P4a 以来第二格从 `router.Ports` 消失的 svc 类型（第一格是第六刀的 `Library`）。
//
// 三种失效各有一条判据：
//
//	① **端口类型回潮**：`Ports` 那一格写回 `svcruntime.Plane`（或任何 svc 接口）——它照样编译、
//	   照样能跑，但"装配点只放依赖事实"这条口径就破了，且 router 又得 import svc 包；
//	② **用例没造**：`ctrruntime` 忘了 `svcruntime.New(`（直接把执行面当用例用）——今天两者的
//	   方法集恰好够用，所以**编译得过**，而命名适配那一层（执行面说 Identity/Diagnostics、
//	   对外契约说 RuntimeIdentity/RuntimeDiagnostics）就静默消失了；
//	③ **转发回流**：`manager` 又长回 `RuntimeIdentity`/`RuntimeDiagnostics`（P4a 前它是
//	   `*Service` 的方法）——两份实现里那份没人用的会先烂，而两个入口的读面就此分叉。
//
// 反证（实跑）：把 `Ports.Runtime` 改回 `svcruntime.Plane`（**能编译**的形态：字段类型改回接口）
// → 红；抽掉 `ctrruntime` 的 `svcruntime.New(` → 红；在 `manager` 里加回
// `func (s *Service) RuntimeIdentity(` → 红。

// runtimeSvcAnchors 用例层的实现锚（实现，不是转发）。
var runtimeSvcAnchors = []string{
	"func New(plane Plane) *Runtime",
	"var _ RuntimeSvc = (*Runtime)(nil)",
	"func (r *Runtime) RuntimeIdentity(",
	"func (r *Runtime) RuntimeDiagnostics(",
	"type Plane interface {",
	"Identity(ctx context.Context) dtoruntime.IdentityResp",
	"Diagnostics(ctx context.Context) (*dtoruntime.DiagnosticsResp, error)",
}

func TestRuntimeIdentityUseCaseLivesInRuntimeSvc(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "service", "svcruntime")
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
	for _, a := range runtimeSvcAnchors {
		if !strings.Contains(src, a) {
			t.Errorf("internal/service/svcruntime 里找不到 %q：底座面的用例实现被改名/搬走了？"+
				"（判据挂在它身上，失锚等于空转）", a)
		}
	}
	// 用例层只认那个两方法端口：它不该认识执行面的实现（`internal/runtimeconn`）——
	// 认识之后"可替换 occupant"就没得换了（测试也没法塞一份假执行面）。
	if strings.Contains(src, `"github.com/morehao/ark-mate/internal/runtimeconn"`) {
		t.Error("internal/service/svcruntime 里出现 `internal/runtimeconn` 的 import：" +
			"用例层只认 `Plane` 端口，执行面的形态由装配点交下来。")
	}

	// ① 装配面：`Ports` 那一格是依赖事实，不许写回 svc 接口类型。
	ports := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "router", "router.go")))
	if !regexp.MustCompile(`Runtime\s+\*runtimeconn\.Plane`).MatchString(ports) {
		t.Error("internal/router/router.go 的 `Ports` 里找不到 `Runtime *runtimeconn.Plane`：" +
			"装配点交的是**执行面本身**（依赖事实），用例实例由 `ctrruntime` 造。")
	}
	if strings.Contains(ports, "svcruntime") {
		t.Error("internal/router/router.go 里出现 `svcruntime`：`Ports` 只放依赖事实，" +
			"svc 接口类型不外溢到装配点（P4a 那批端口别名已判定为骚操作，别再开这条路）。")
	}
	routerFiles := goFilesUnder(t, filepath.Join(root, "internal", "router"))
	if len(routerFiles) == 0 {
		t.Fatal("internal/router 下没有 .go 文件：守卫失去检查对象")
	}
	for _, path := range routerFiles {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		if strings.Contains(stripLineComments(readFileOrFatal(t, path)), "svcruntime") {
			t.Errorf("internal/router/%s 里出现 `svcruntime`：这一格交的是执行面，不是用例接口。",
				filepath.Base(path))
		}
	}

	// ② 用例必须在控制器里造出来。
	ctr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrruntime", "runtime.go")))
	if !strings.Contains(ctr, "svcruntime.New(") {
		t.Error("internal/controller/ctrruntime/runtime.go 里没有 `svcruntime.New(`：用例必须在控制器里造出来" +
			"（那一层是 API 口语与执行面口语之间的命名适配，跳过它两个名字就静默分叉）。")
	}

	// ③ manager 侧不得长回那两条转发。
	mgr := filepath.Join(root, "internal", "manager")
	mgrFiles := productionGoFiles(t, mgr)
	if len(mgrFiles) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", mgr)
	}
	for _, path := range mgrFiles {
		mgrBody := stripLineComments(readFileOrFatal(t, path))
		for _, m := range []string{"func (s *Service) RuntimeIdentity(", "func (s *Service) RuntimeDiagnostics("} {
			if strings.Contains(mgrBody, m) {
				t.Errorf("internal/manager/%s 里出现 %q：底座面的用例只有一份实现，在 "+
					"`internal/service/svcruntime`（P4a 正文第七刀）。", filepath.Base(path), m)
			}
		}
	}
}
