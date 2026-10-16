package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bindingFieldRe 抓 `Service` 上可能重新长出来的绑定字段。
//
// **必须容空白**：gofmt 会把 `binding *runtimeBinding` 与旁边的字段对齐成
// `binding      *runtimeBinding`（多空格）。第一版判据写的是定长单空格，于是**假绿**——
// 实测把字段加回去，守卫照样通过。与 §3.1.53 那条锁序守卫的 `}` 判定是同一类毛病：
// **判据要对"格式"免疫**，否则它只在某一种排版下成立。
var bindingFieldRe = regexp.MustCompile(`binding\s+\*runtimeBinding`)

// ——— P3a-2「执行面归属」守卫（第一刀：底座绑定） ———
//
// 执行面（连接池 / 代际 / 子进程回收 / 会话挂载 / 运行期诊断）落 `internal/runtimeconn`。
// 第一刀只搬"本进程认的那个底座"的绑定与诊断**状态**。
//
// 两种静默失效：
//
//	① **两份绑定**：谁在 manager 里再放一个 `binding *runtimeBinding` 字段——读面（`/meta`
//	   的底座身份、诊断的 State）与"谁在建/拆连接时改状态"从此可能读到两份值，而两边都编译得过。
//	   症状是 `/runtime/diagnostics` 报 stopped 而连接明明活着（或反过来）。
//	② **装配漏接**：`NewService` 漏了 `runtimeconn.NewPlane(...)` ⇒ 绑定恒为 nil，
//	   `Snapshot()` 回落到"构建期 id + stopped"——**看起来完全正常**，只是进程状态永远是 stopped。
//
// 判据三段，fail-closed（锚找不到即失败）。

// runtimeconnAnchors 执行面必须有的入口符号。
var runtimeconnAnchors = []string{
	"func (pl *Plane) Snapshot(",
	"func (pl *Plane) SetState(",
	"func (pl *Plane) Resolve(",
	"func (pl *Plane) SelectedSpec(",
	"func (pl *Plane) Identity(",
	"func (pl *Plane) RecordError(",
}

// TestRuntimeBindingLivesInRuntimeconn 绑定与诊断状态必须在 `internal/runtimeconn`，
// 且 manager 的转发文件里不得再出现状态或判定。
func TestRuntimeBindingLivesInRuntimeconn(t *testing.T) {
	root := backendRoot(t)
	domainDir := filepath.Join(root, "internal", "runtimeconn")
	joined := ""
	for _, name := range []string{"binding.go", "diagnostics.go"} {
		joined += stripLineComments(readFileOrFatal(t, filepath.Join(domainDir, name)))
	}
	for _, a := range runtimeconnAnchors {
		if !strings.Contains(joined, a) {
			t.Errorf("internal/runtimeconn 里找不到 %q：执行面的绑定/诊断入口被改名/搬走了？"+
				"（这条守卫的判据就挂在它身上，失锚等于空转）", a)
		}
	}

	// manager 侧：转发文件里不得再出现状态与判定。
	thin := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "manager", "runtime_binding.go")))
	for _, banned := range []string{"sync.Mutex", "runtime.SelectedRuntime()", "acl.NewError("} {
		if strings.Contains(thin, banned) {
			t.Errorf("internal/manager/runtime_binding.go 里出现 %q：绑定的状态与判定**只有一份实现**，"+
				"在 `internal/runtimeconn`（P3a-2）。在这里再写一遍会让 `/meta` 的底座身份与诊断的 State "+
				"读到两份值，而两边都编译得过。", banned)
		}
	}
}

// TestRuntimeBindingIsNotDuplicatedOnService 绑定状态不许在 `Service` 上再放一份。
//
// 它是 ① 的判据主体：状态在平面上，`Service` 只持平面引用。再放一个字段的后果是
// "谁在建/拆连接时改状态"与"读面读到哪一份"变成两件事——而表现是诊断报 stopped 而连接活着。
func TestRuntimeBindingIsNotDuplicatedOnService(t *testing.T) {
	root := backendRoot(t)
	service := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "manager", "service.go")))
	if bindingFieldRe.MatchString(service) {
		t.Error("`internal/manager/service.go` 上出现了 `binding *runtimeBinding` 字段：" +
			"绑定的状态归 `internal/runtimeconn`（`Service` 只持平面引用）。\n" +
			"两份状态的表现是 `/runtime/diagnostics` 的 State 与真实连接状态分叉——" +
			"一边说 stopped、另一边连接活着，而两边都编译得过。")
	}
	if !strings.Contains(service, "runtimeconn.NewPlane(") {
		t.Error("`internal/manager/service.go` 的 `NewService` 里找不到 `runtimeconn.NewPlane(`：" +
			"执行面没有装配。\n" +
			"表现是绑定恒为 nil、`Snapshot()` 回落到「构建期 id + stopped」——看起来完全正常，" +
			"只是进程状态永远是 stopped。")
	}
}
