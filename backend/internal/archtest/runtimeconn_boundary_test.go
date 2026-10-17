package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— P3a-2 收口守卫：执行面**只放机制，不放编排** ———
//
// 实测（2026-10-16，P3a-2 第三刀之后）把这件事钉死了：`Orchestrator` 的 8 个文件里，
// 7 个都在**大量**回调 `Service`——`o.svc.*` 的去重计数是 **31 个不同方法**：
//
//	orchestrator.go 6 · connection.go 8 · run.go 16 · session_start.go 25 · generation.go 6 ·
//	event_sink.go 4 · inputqueue.go 4 ·（只有 `orphan_reap.go` 是 0）
//
// 那 31 个方法横跨会话域（`publish`/`markSession`/`sessionRec`/`openRun`/输入队列/transcript）、
// 能力面（`capabilityFingerprint`/`validateSelection`/`McpServersFor`）、供应商面
// （`providerEnvFor`/`CredentialProvider`…）、模型目录（`catalog`）与记忆面。
//
// **结论：`Orchestrator` 不是执行面，它是用例编排层**——它"拿执行面的连接去做会话的事"。
// 把它搬进 `runtimeconn` 只有两条路，两条都是错的：
//
//	① 给执行面一个 **31 方法的 god port**——端口一宽，执行面就重新认识每一个域，
//	   "可替换的 occupant"这层意义当场消失（它也再没法被单独测试）；
//	② 把 `Service` 整个塞进来——那只是把 manager 改个名字，且立刻产生"两份连接池"的风险
//	   （`runtimeConn` 的字段全是未导出的，任何"一半搬走"都会得到编译不过或双份状态）。
//
// 所以 `Orchestrator` 的归宿是 **P4/P5 与 `manager` 一起dissolve**（编排进用例层、状态进各域），
// 不是 P3a-2。本文件的两条守卫就是这条决定的机器判据——它挡的是"下一个人按附录 A 的分工表
// 硬把编排搬进执行面"。
//
// 反证（实跑）：往 `runtimeconn` 加一个含 `o.svc.` 的文件 → 红（第一条）；
// 给它加一个 6 方法的接口 → 红（第二条）。
//
// 第五十四刀扩面（仍两条，不增条数）：第一条的禁词表加上 `*session.Plane` /
// `session.MutateAll(` / `session.Mutate(`——连接池搬进来之后，最容易被顺手写下的编排痕迹
// 就是"池自己把会话写成什么状态"（`OnConnDropped` 负控的判据）。

// maxRuntimeconnPortMethods 执行面声明的端口方法数上限。
//
// 取 4 是**按实测**定的：第五十四刀之后本域共五个端口——`CredentialSource`（2）、
// `LiveClientSource`（1）、`ProviderEnvSource`（1）、`ConfigDigestSource`（1）、`DropSink`（2），
// 最宽 2 个方法；4 仍是"建连/拆连这类事实"够用的上限（拆连接时真正需要的第二、三条事实被
// 拆成了独立的窄端口，而不是往同一个接口上堆）。真正的判据不是这个数字，而是
// **"端口一旦宽到能表达编排，就说明搬错了东西"**——31 是反例的规模。
const maxRuntimeconnPortMethods = 4

// runtimeconnOrchestrationMarkers 编排层的痕迹（只该出现在 `manager`，等着 P4 分流）。
var runtimeconnOrchestrationMarkers = []string{
	"o.svc.",
	"type Orchestrator",
	// 第五十四刀：**执行面不许认识会话表**。连接被摘除之后怎么收尾是编排侧的事，池只发通知
	// （`DropSink`）。在池里直接改会话表的表现是静默的：编译过、测试绿，而"谁拥有会话状态"
	// 重新没有答案——那正是这条守卫要拦的形态。
	"*session.Plane",
	"session.MutateAll(",
	"session.Mutate(",
}

// TestRuntimeconnHoldsNoOrchestration 执行面里不得出现编排层的形态。
func TestRuntimeconnHoldsNoOrchestration(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "runtimeconn")
	files := productionGoFiles(t, dir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：这条守卫失去了检查对象（目录改名/搬走了？）", dir)
	}
	for _, path := range files {
		src := stripLineComments(readFileOrFatal(t, path))
		for _, m := range runtimeconnOrchestrationMarkers {
			if strings.Contains(src, m) {
				t.Errorf("internal/runtimeconn/%s 里出现编排层痕迹 %q：执行面**只放机制，不放编排**"+
					"（P3a-2 收口，理由见本文件头）。\n"+
					"`Orchestrator` 实测回调 `Service` 的 **31 个不同方法**（会话域/能力面/供应商面/"+
					"模型目录/记忆面），它是**用例编排层**——搬进执行面只能靠一个 31 方法的 god port，"+
					"而那会让执行面重新认识每一个域。它的归宿是 P4/P5 与 `manager` 一起 dissolve。",
					filepath.Base(path), m)
			}
		}
	}
}

var interfaceHeadRe = regexp.MustCompile(`^type\s+([A-Za-z_][A-Za-z0-9_]*)\s+interface\s*\{`)

// TestRuntimeconnPortsStayNarrow 执行面声明的每个端口都必须窄。
//
// 实现取的是**够用即止**的形状：按行扫 `type X interface {` 到顶格 `}`，把其间"含 `(` 且非注释"
// 的行当作方法。**内嵌接口**（如 `io.Reader`，不带 `(`）不计入——这条守卫要拦的是"方法一多就
// 说明搬错了东西"，内嵌一个小接口不会改变这个判断。
//
// fail-closed：一个接口都没扫到也判失败——那说明端口被删/改名了，判据失锚。
func TestRuntimeconnPortsStayNarrow(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "runtimeconn")
	files := productionGoFiles(t, dir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：判据失去检查对象", dir)
	}
	total := 0
	for _, path := range files {
		src := stripLineComments(readFileOrFatal(t, path))
		lines := strings.Split(src, "\n")
		for i := 0; i < len(lines); i++ {
			m := interfaceHeadRe.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			total++
			methods := 0
			for j := i + 1; j < len(lines) && lines[j] != "}"; j++ {
				l := strings.TrimSpace(lines[j])
				if l == "" || strings.Contains(l, "(") {
					if strings.Contains(l, "(") {
						methods++
					}
				}
			}
			if methods > maxRuntimeconnPortMethods {
				t.Errorf("internal/runtimeconn/%s 的 `%s` 声明了 %d 个方法（上限 %d）：执行面的端口一宽，"+
					"就说明搬进来的不是机制而是编排。\n"+
					"对照实测：`Orchestrator` 回调 `Service` 的 **31 个不同方法**——那正是这条路走到头的样子。\n"+
					"处置：把该动作留在编排层（P4 分流），或拆成几个窄端口只取真正需要的那一两个事实。",
					filepath.Base(path), m[1], methods, maxRuntimeconnPortMethods)
			}
		}
	}
	if total == 0 {
		t.Fatal("`internal/runtimeconn` 里一个 interface 都没扫到：端口被删/改名了？" +
			"这条守卫的判据挂在它们身上，失锚等于空转。")
	}
}
