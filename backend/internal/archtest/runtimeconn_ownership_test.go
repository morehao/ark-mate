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
	// 第二刀：诊断的组装面 + 两端口的声明。
	"func (pl *Plane) Diagnostics(",
	"func (pl *Plane) RemediationFor(",
	"func (pl *Plane) RecordRuntimeError(",
	"type CredentialSource interface {",
	"type LiveClientSource interface {",
}

// TestRuntimeBindingLivesInRuntimeconn 绑定与诊断状态必须在 `internal/runtimeconn`，
// 且 manager 的转发文件里不得再出现状态或判定。
func TestRuntimeBindingLivesInRuntimeconn(t *testing.T) {
	root := backendRoot(t)
	domainDir := filepath.Join(root, "internal", "runtimeconn")
	joined := ""
	for _, name := range []string{"binding.go", "diagnostics.go", "ports.go"} {
		joined += stripLineComments(readFileOrFatal(t, filepath.Join(domainDir, name)))
	}
	for _, a := range runtimeconnAnchors {
		if !strings.Contains(joined, a) {
			t.Errorf("internal/runtimeconn 里找不到 %q：执行面的绑定/诊断入口被改名/搬走了？"+
				"（这条守卫的判据就挂在它身上，失锚等于空转）", a)
		}
	}

	// manager 侧：两个转发文件里不得再出现状态与判定（组装面第二刀之后也不该留判据文案）。
	for _, f := range []string{"runtime_binding.go", "diagnostics.go"} {
		thin := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "manager", f)))
		for _, banned := range []string{"sync.Mutex", "runtime.SelectedRuntime()", "acl.NewError(",
			"runtime.Installed(", "looksLikeAuthFailure("} {
			if strings.Contains(thin, banned) {
				t.Errorf("internal/manager/%s 里出现 %q：绑定的状态与诊断的判据**只有一份实现**，"+
					"在 `internal/runtimeconn`（P3a-2）。在这里再写一遍会让 `/meta` 的底座身份、"+
					"诊断的 State 与「怎么修」的出路各说各话，而两边都编译得过。", f, banned)
			}
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
	if !strings.Contains(service, "SetCredentialSource(s)") {
		t.Error("`NewService` 里找不到 `SetCredentialSource(s)`：诊断的「怎么修」少一条出路" +
			"（凭据悬空/缺密钥时不再提示）——而诊断面照常 200，只是少说一句话。")
	}
	// 连接池读面挂钩在编排器回挂那一刻（`NewService` 时它还不存在），漏了的表现是诊断永远报"未连接"。
	// 判据扫 `manager` 的**生产**文件而不是某一条固定路径：接线点会随搬迁挪窝
	// （`attachOrchestrator` 现在在 `service.go`，但它属于装配面，P4b/P5a 会再动它）。
	wired := false
	for _, path := range productionGoFiles(t, filepath.Join(root, "internal", "manager")) {
		if strings.Contains(readFileOrFatal(t, path), "SetLiveClientSource(") {
			wired = true
			break
		}
	}
	if !wired {
		t.Error("`internal/manager` 的生产文件里找不到 `SetLiveClientSource(`（接线点在 `attachOrchestrator`）：" +
			"连接池读面没有接给执行面。\n" +
			"表现是诊断永远报「未连接」、stderr 尾巴恒空——而它只在**底座真的在跑**时才该现形，" +
			"开发态多半看不见。")
	}
}
