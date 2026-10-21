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
// 执行面（连接池 / 代际 / 子进程回收 / 会话挂载 / 运行期诊断）落 `internal/drivers/runtimeconn`。
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
	// 第四十八刀：**子进程注册表**（登记 / 摘除 / 回收 / 读取）。
	"func (pl *Plane) SetProcRegistry(",
	"func (pl *Plane) ProcRegistry(",
	"func (pl *Plane) ReapOrphans(",
	"func (pl *Plane) RegisterProc(",
	"func (pl *Plane) UnregisterProc(",
	"procReg *procreg.Registry",
	// 第五十四刀：**连接池**（`Pool` / `Conn`）与它的三个新端口。
	// 阶段 0.4：`Conn` / `ConnPool` / `DropSink` 的**声明**搬进 `domain/session`（下方单独判据），
	// 执行面保留实现与两个来源端口（`ProviderEnvSource` / `ConfigDigestSource`）。
	"type Pool struct {",
	"type Conn struct {",
	"func (pl *Pool) Add(",
	"func (pl *Pool) Drop(",
	"func (pl *Pool) KeyFor(",
	"func (pl *Pool) AdmitGeneration(",
	"func (pl *Pool) ReapIdle(",
	"func (pl *Pool) HasConn(",
	"type ProviderEnvSource interface {",
	"type ConfigDigestSource interface {",
}

// TestRuntimeBindingLivesInRuntimeconn 绑定与诊断状态必须在 `internal/drivers/runtimeconn`，
// 且 manager 的转发文件里不得再出现状态或判定。
func TestRuntimeBindingLivesInRuntimeconn(t *testing.T) {
	root := backendRoot(t)
	domainDir := filepath.Join(root, "internal", "drivers", "runtimeconn")
	// 扫**整个目录**而不是一份写死的文件清单：第四十八刀加 `procreg.go` 时，写死的清单让新文件的
	// 锚**从来没被查过**（判据当场红，但根因是扫描面没跟上）。这一条与 `uiSrcRoots()` 那条教训同源：
	// 扫描面写死一处，新增的文件就静默不设防。
	joined := dirProductionBody(t, domainDir)
	if len(joined) < 500 {
		t.Fatalf("`internal/drivers/runtimeconn` 只扫到 %d 字节——扫描面解析错了？（fail-closed）", len(joined))
	}
	for _, a := range runtimeconnAnchors {
		if !strings.Contains(joined, a) {
			t.Errorf("internal/drivers/runtimeconn 里找不到 %q：执行面的绑定/诊断入口被改名/搬走了？"+
				"（这条守卫的判据就挂在它身上，失锚等于空转）", a)
		}
	}
	// 阶段 0.4：连接端口的**声明**归 `domain/session`（执行面实现它们；端口放 L7 会让 L6 的实现
	// 变成"向上 import"）。判据跟着落点走，否则搬走就静默失锚。
	portFile := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "session", "conn.go")))
	for _, a := range []string{"type Conn interface {", "type ConnPool interface {", "type DropSink interface {"} {
		if !strings.Contains(portFile, a) {
			t.Errorf("`internal/domain/session/conn.go` 里找不到 %q：连接端口的声明被改名/搬走了？"+
				"（阶段 0.4 的判据挂在它身上，失锚等于空转）", a)
		}
	}

	// 第四十九刀：执行面自身的**四条一行转发**清零（`runtime_binding.go` 整个文件随之删除）。
	//
	// 这一族与供应商那七条（第四十六刀）同形：一行转发的正确性完全等价于被转发的那一行，
	// 所以它写错与否都不会让任何测试变红，而它让「这段实现在哪一层」重新不可见。
	mgrAll := dirProductionBody(t, filepath.Join(root, "internal", "sessionruntime"))
	// 第五十四刀：**连接表与连接对象**也不许回到 manager。
	//
	// 三种失效都不让别的测试变红：
	//   - `conns map[string]*runtimeConn` 与池里的表并存 ⇒ 迁移判据读一张、拨号写另一张；
	//   - `type runtimeConn struct` 复活 ⇒ 两个连接对象、两套字段语义（`Client` vs `client`）；
	//   - `o.conns` 的任意读写 ⇒ 有人绕过池锁与键派生（"同一份配置共享暖进程"当场失效）。
	for _, dead := range []string{"conns map[string]*runtimeConn", "type runtimeConn struct", "o.conns"} {
		if strings.Contains(mgrAll, dead) {
			t.Errorf("`internal/sessionruntime` 里出现 %q：连接池与连接对象**只有一份**，在 `internal/drivers/runtimeconn`"+
				"（P4a 正文第五十四刀）。两份的表现不是报错：迁移判据读一张表、acquire 写另一张，"+
				"「同配置共享暖进程」静默失效。", dead)
		}
	}
	// 装配与两端的接线：池必须在 `NewService` 造出来（且有 env / 指纹两个事实来源），
	// 拆除的应用面必须在编排器回挂时接上。漏任何一个的表现都是静默的（键少维度 / 会话不收尾）。
	//
	// 阶段 0.4b：执行面的**构造与来源接线**随"NewService 不自造执行面"搬到装配根
	// （`bootstrap/assembly`）——判据跟落点走；`SetDropSink` 仍在编排器回挂处。
	for _, c := range []struct{ file, want, why string }{
		{"internal/bootstrap/assembly/ports.go", "runtimeconn.NewPool(", "执行面的连接池"},
		{"internal/bootstrap/assembly/ports.go", "SetProviderEnvSource(", "子进程 env 的来源（同时是基础键的输入）"},
		{"internal/bootstrap/assembly/ports.go", "SetConfigDigestSource(", "模型清单指纹的来源（进代际键）"},
		{"internal/bootstrap/assembly/ports.go", "SetLiveClientSource(", "诊断的连接池读面"},
		{"internal/sessionruntime/service.go", "SetDropSink(o)", "拆除的应用面（会话收尾）"},
		{"internal/sessionruntime/connection.go", "func (o *Orchestrator) OnConnDropped(", "拆除的应用实现"},
		{"internal/sessionruntime/connection.go", "func (o *Orchestrator) InUseOn(", "回收判据的应用实现"},
	} {
		if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, c.file))), c.want) {
			t.Errorf("`%s` 里找不到 `%s`（%s）：这是执行面端口的接线点，漏了不会有编译错——"+
				"表现是键少一维、或连接拆了会话却不收尾。", c.file, c.want, c.why)
		}
	}
	for _, dead := range []string{
		"func (s *Service) runtimeBindingSnapshot(",
		"func (s *Service) setRuntimeState(",
		"func (s *Service) ResolveRuntime(",
		"func (s *Service) selectedSpec(",
	} {
		if strings.Contains(mgrAll, dead) {
			t.Errorf("`internal/sessionruntime` 里又出现 `%s`：执行面自身的转发已随 P4a 正文第四十九刀清零"+
				"（调用点直连 `runtimePlane`）。", dead)
		}
	}
	//
	// 第五十四刀：前两条的锚**跟着对象走**——连接池搬进执行面之后，"建连/拆连时的进程状态"
	// 的**唯一写入方**就是池（`Add` → running、`Drop` 摘到当前连接 → stopped）。判据本身不变，
	// 仍是"这两个状态转移必须在执行面、且调用点直连"；留在 manager 的话它就只能是转发。
	for _, c := range []struct{ file, want, why string }{
		{"internal/drivers/runtimeconn/pool.go", "SetState(session.AgentStateRunning)", "建连时的进程状态"},
		{"internal/drivers/runtimeconn/pool.go", "SetState(session.AgentStateStopped)", "拆连时的状态回落"},
		{"internal/sessionruntime/orchestrator.go", "svc.runtimePlane.SelectedSpec", "建会话时的 spec 解析（装配点直连执行面）"},
		{"internal/domain/session/lifecycle.go", "spec, err := l.selectedSpec()", "建会话/恢复时的 spec 解析（域经端口调用）"},
	} {
		if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, c.file))), c.want) {
			t.Errorf("`%s` 里找不到 `%s`（%s）：调用点必须直连执行面。"+
				"漏了会让这一条链悄悄退回某个中间层。", c.file, c.want, c.why)
		}
	}
}

// TestRuntimeBindingIsNotDuplicatedOnService 绑定状态不许在 `Service` 上再放一份。
//
// 它是 ① 的判据主体：状态在平面上，`Service` 只持平面引用。再放一个字段的后果是
// "谁在建/拆连接时改状态"与"读面读到哪一份"变成两件事——而表现是诊断报 stopped 而连接活着。
func TestRuntimeBindingIsNotDuplicatedOnService(t *testing.T) {
	root := backendRoot(t)
	service := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "sessionruntime", "service.go")))
	if bindingFieldRe.MatchString(service) {
		t.Error("`internal/sessionruntime/service.go` 上出现了 `binding *runtimeBinding` 字段：" +
			"绑定的状态归 `internal/drivers/runtimeconn`（`Service` 只持平面引用）。\n" +
			"两份状态的表现是 `/runtime/diagnostics` 的 State 与真实连接状态分叉——" +
			"一边说 stopped、另一边连接活着，而两边都编译得过。")
	}
	// 阶段 0.4b：构造与来源接线归装配根（`NewService` 不再自造执行面）。
	build := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "bootstrap", "assembly", "ports.go")))
	if !strings.Contains(build, "runtimeconn.NewPlaneFor(") {
		t.Error("`internal/bootstrap/assembly/ports.go` 里找不到 `runtimeconn.NewPlaneFor(`：" +
			"执行面没有装配。\n" +
			"表现是绑定恒为 nil、`Snapshot()` 回落到「构建期 id + stopped」——看起来完全正常，" +
			"只是进程状态永远是 stopped。")
	}
	if !strings.Contains(build, "SetCredentialSource(") {
		t.Error("装配根里找不到 `SetCredentialSource(`：诊断的「怎么修」少一条出路" +
			"（凭据悬空/缺密钥时不再提示）——而诊断面照常 200，只是少说一句话。")
	}
	// 连接池读面：漏了的表现是诊断永远报"未连接"。
	if !strings.Contains(build, "SetLiveClientSource(") {
		t.Error("装配根里找不到 `SetLiveClientSource(`：连接池读面没有接给执行面。\n" +
			"表现是诊断永远报「未连接」、stderr 尾巴恒空——而它只在**底座真的在跑**时才该现形，" +
			"开发态多半看不见。")
	}
}

// ——— P4a 正文第二刀：`runtime_not_found` 的**单点铸码** ———
//
// 搬 `RuntimeIdentity`/`RuntimeDiagnostics` 时发现的一处**真实的重复铸码**：`runtime_not_found`
// 在"构建期选定的那个 runtime 没注册"这条判据上被铸了**三处**——
//
//	runtimeconn/binding.go         ✓ 保留（唯一铸码点）
//	manager/session_start.go       ✗ 已删（`startSessionOn`：`runtimeID` 就是刚解析出来的那个，
//	                                 紧接着 `reg.Get(runtimeID)` 不可能不命中 ⇒ **永远不可达**）
//	manager/session_start.go       ✗ 已删（恢复路径，同上）
//
// 同一批还发现**同因异码**（**尚未定案，刻意没动**）：manager/model_catalog.go 的 `ModelCatalog`
// 对**同一个条件**铸的是 `runtime_not_registered`——而那个码**不在** `internal/core/code/business.go` 的映射表里
// （只有 `runtime_not_found` 在），于是它落到 ClassRequest 的兜底码 40001，而 runtimeconn 那条是
// 40401。**改它是对外契约变更**（要走 `internal/core/code` + README + 前端 `BizCode` 三处同步），
// 所以登记在 §3.1.61 而不是顺手改；本条守卫只钉**已收敛的那一处**。
//
// 判据两段，fail-closed：
//
//	① `runtime_not_found` 的 `code.NewError` 铸码点在 `internal/sessionruntime` 里**为零**、
//	   在 `internal/drivers/runtimeconn` 里**恰好一处**；
//	② `resource_sync.go` 里那个 `runtime_not_found`（校验**用户请求同步的那个 runtime**）
//	   是**另一个输入**，刻意放行——但它必须仍在（否则说明有人把请求参数校验也删了）。
func TestRuntimeNotFoundHasSingleMintPoint(t *testing.T) {
	root := backendRoot(t)
	internalDir := filepath.Join(root, "internal")
	files := productionGoFiles(t, internalDir)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：判据失去检查对象", internalDir)
	}
	mint := `code.NewError(code.ClassRequest, "runtime_not_found"`
	inRuntimeconn, inManager, inResourceSync := 0, 0, 0
	for _, path := range files {
		body := readFileOrFatal(t, path)
		n := strings.Count(body, mint)
		if n == 0 {
			continue
		}
		slash := filepath.ToSlash(rel(root, path))
		switch {
		case strings.HasPrefix(slash, "internal/drivers/runtimeconn/"):
			inRuntimeconn += n
		case filepath.ToSlash(rel(root, path)) == "internal/drivers/registry/resources.go":
			inResourceSync += n
		case strings.HasPrefix(slash, "internal/sessionruntime/"):
			inManager += n
			t.Errorf("internal/sessionruntime/%s 里铸了 `runtime_not_found`：这条判据的铸码点在执行面"+
				"（`runtimeconn.SelectedSpec`）。\n"+
				"同一条判据的第二、第三份文本迟早只改一处——用户看到的「为什么没装配」就会分成两套说法。",
				filepath.Base(path))
		}
	}
	if inRuntimeconn != 1 {
		t.Errorf("`internal/drivers/runtimeconn` 里 `runtime_not_found` 的铸码点是 %d 处（期望恰好 1）："+
			"这条判据失锚或被人复制了。", inRuntimeconn)
	}
	if inManager != 0 {
		t.Errorf("`internal/sessionruntime` 里仍有 %d 处 `runtime_not_found` 铸码。", inManager)
	}
	if inResourceSync != 1 {
		t.Error("`internal/drivers/registry/resources.go` 里那个 `runtime_not_found` 不见了：" +
			"它校验的是**用户请求同步的那个 runtime**（另一个输入，故放行）——删掉它 = " +
			"请求一个不在册的 runtime 会被静默当成「同步全部」。")
	}
}
