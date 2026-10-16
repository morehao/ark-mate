package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ——— P4a 正文第三刀「供应商自检归属」守卫 ———
//
// 供应商连接自检（M4）从 `manager/provider_probe.go`（`*Service` 的方法 + 域/模型的取数）
// 搬进 `internal/service/svcprovider`，形状换成与导出同一个模板：
//
//	`svcprovider.Probe`（用例：两段各自的检查项 / 结论口径 / 出路文案 / 响应装配）
//	  ← `svcprovider.Ports`（供应商域读面，4 个方法） + `Catalog`（模型目录读面，1 个方法）
//	      ← `ctrprovider` 把「域平面 + 模型面」接上并**造出**用例（`NewProviderCtr` 里
//	         `svcprovider.NewProbe(archives, catalog)`）
//
// 与导出那一刀（`svcsystem_usecase_test.go`）的**唯一差别**在这里，也是最容易记错的一点：
// 导出的适配器留在 `manager`（`export_source.go`，靠结构满足 Source），而自检的适配器落在
// **控制器**——因为自检的事实来源是**域平面**（`*provider.Plane`）而不是 `*Service` 上的方法，
// 把它接进用例层需要一个同时认识域与用例类型的位置，而那个位置只允许是控制器。
//
// 两种失效各有一条判据：
//
//	① **实现回流**：谁在 `manager` 里再写一遍探活（`countModels`/`locatorKind`/超时常量…），
//	   两份实现静默分叉——上游段的计数口径、超时口径、`delivery` 那条的措辞各说各话，而两边
//	   都编译得过；
//	② **装配漏接**：`ctrprovider` 忘了 `svcprovider.NewProbe(...)`，或 `manager` 不再交出
//	   `ProviderPlane()`——前者让自检端点 500（nil 用例），后者让自检永远报「档案不在册」
//	   （域读面恒空）。**两种都只在用户点"测试连接"时才现形**。
//
// 反证（实跑）：把 `countModels` 搬回 `manager`（能编译的形态）→ 红；把
// `ctrprovider` 的 `svcprovider.NewProbe(` 抽掉（改成直接用 `archives`）→ 红。

// probeUseCaseAnchors 用例层必须有的实现锚（**私有实现**，不是薄转发——见 §6.1 的锚点规则）。
var probeUseCaseAnchors = []string{
	"func (p *Probe) ProbeProvider(",
	"func (p *Probe) probeLocal(",
	"func (p *Probe) probeUpstream(",
	"probeUpstreamTimeout",
	`"delivery"`,
}

// probePortAnchors 两个消费方声明的窄端口必须真的在（用例层不认识域平面的锁、也不认识模型面）。
var probePortAnchors = []string{
	"type Ports interface {",
	"Snapshot(id string) (*provider.Def, bool)",
	"type Catalog interface {",
	"ModelCatalog(ctx context.Context) (*dtomodel.ModelCatalogResp, error)",
}

// probeImplementationMarkers 实现回流到 `manager` 的痕迹（判据口径 + 上游段口径）。
var probeImplementationMarkers = []string{
	"probeUpstreamTimeout",
	"countModels(",
	"locatorKind(",
	"func (p *Probe)",
}

// TestProviderProbeUseCaseLivesInProviderSvc 见文件头。
func TestProviderProbeUseCaseLivesInProviderSvc(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "service", "svcprovider")

	// 断言跟着实现走：实现方在本包，端口与实现的同包断言必须在（`Probe` 搬家时它会先报）。
	portSrc := stripLineComments(readFileOrFatal(t, filepath.Join(dir, "provider.go")))
	for _, a := range []string{
		"type Prober interface {",
		"ProbeProvider(ctx context.Context, id string, req dtoprovider.ProviderProbeReq) (*dtoprovider.ProviderProbeResp, error)",
		"var _ Prober = (*Probe)(nil)",
	} {
		if !strings.Contains(portSrc, a) {
			t.Errorf("internal/service/svcprovider/provider.go 里找不到 %q：自检的端口/断言被改名或搬走了？"+
				"（判据挂在它身上，失锚等于空转）", a)
		}
	}
	// 自检的方法只该声明一次（在 `Prober` 上）：并回 `ProviderSvc` 等于把"档案"与"自检"
	// 重新绑在一份实现上——而这一刀的全部理由正是它们各有主人。档案四动作仍留在 `ProviderSvc`
	// 上（它们今天仍由 `manager` 实现），P4a 是"收一条、少一条"。
	if n := strings.Count(portSrc, "ProbeProvider("); n != 1 {
		t.Errorf("internal/service/svcprovider/provider.go 里 `ProbeProvider(` 出现 %d 次（期望 1：只在 `Prober` 上）："+
			"自检的方法并回 `ProviderSvc` 了？那会让「档案」与「自检」两条各有主人的用例重新绑在一份实现上。", n)
	}

	probSrc := stripLineComments(readFileOrFatal(t, filepath.Join(dir, "probe.go")))
	for _, a := range append(append([]string{}, probeUseCaseAnchors...), probePortAnchors...) {
		if !strings.Contains(probSrc, a) {
			t.Errorf("internal/service/svcprovider/probe.go 里找不到 %q：自检用例的实现被改名/搬走了？"+
				"（这条守卫的判据就挂在它身上，失锚等于空转）", a)
		}
	}
	// 用例层不得 import 持久层或实现层（那正是两个端口的用途）。
	for _, banned := range []string{`"github.com/morehao/ark-mate/internal/store"`, `"github.com/morehao/ark-mate/internal/manager"`} {
		if strings.Contains(probSrc+portSrc, banned) {
			t.Errorf("`internal/service/svcprovider` 里出现 %s：用例层通过**端口**取事实（ADR-6），"+
				"它不 import 持久层、也不 import `manager`。", banned)
		}
	}

	// manager 侧：不得再有实现痕迹。
	mgr := filepath.Join(root, "internal", "manager")
	files := productionGoFiles(t, mgr)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", mgr)
	}
	for _, path := range files {
		body := stripLineComments(readFileOrFatal(t, path))
		for _, m := range probeImplementationMarkers {
			if strings.Contains(body, m) {
				t.Errorf("internal/manager/%s 里出现自检用例的实现痕迹 %q：它**只有一份实现**，"+
					"在 `internal/service/svcprovider`（P4a 正文第三刀）。\n"+
					"manager 交出的是域平面（`ProviderPlane()`），适配在 `ctrprovider`。\n"+
					"在这里再写一遍的后果是上游段计数口径/超时口径静默分叉，而两边都编译得过。",
					filepath.Base(path), m)
			}
		}
	}

	// 旧实现文件不得复活（比"文件里的痕迹"更硬：两份实现并存的起点）。
	old := filepath.Join(mgr, "provider_probe.go")
	if _, err := os.Stat(old); err == nil {
		t.Error("`internal/manager/provider_probe.go` 又存在了：供应商自检的实现归 " +
			"`internal/service/svcprovider`（P4a 正文第三刀）；适配在 `ctrprovider`。")
	}

	// 装配：控制器必须真的把用例造出来，manager 必须交出域平面。
	ctr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrprovider", "provider.go")))
	if !strings.Contains(ctr, "svcprovider.NewProbe(") {
		t.Error("`internal/controller/ctrprovider/provider.go` 里找不到 `svcprovider.NewProbe(`：" +
			"自检用例没人造 ⇒ `POST /providers/:id/probe` 直接 500（而只测档案四动作的用例看不见）。")
	}
	svcSrc := stripLineComments(readFileOrFatal(t, filepath.Join(mgr, "service.go")))
	if !strings.Contains(svcSrc, "func (s *Service) ProviderPlane() *provider.Plane") {
		t.Error("`internal/manager/service.go` 里找不到 `ProviderPlane()`：控制器拿不到供应商域平面 ⇒ " +
			"自检的域读面恒空，表现是**永远**报「档案不在册」（只在用户点\"测试连接\"时才现形）。")
	}
}
