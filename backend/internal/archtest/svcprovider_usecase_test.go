package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ——— P4a 正文第三/四刀「供应商自检 + 档案归属」守卫 ———
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
// **P4a 正文第四刀（档案面）**：`ListProviders`/`CreateProvider`/`UpdateProvider`/
// `DeleteProvider` 四条转发从 `manager/provider_forward.go` 删除，出口换成
// `svcprovider.Provider`（`archive.go`，实现直接拿域平面）。这一刀的判据是"**转发不许复活**"：
// `manager` 上再长出这四条方法，控制器就又有了一条绕过用例层的路，而两条路都能编译、都能跑，
// 只是其中一条没有生效面回执、也不在未来任何迁移的名单里。
//
// 反证（实跑）：把 `countModels` 搬回 `manager`（能编译的形态）→ 红；把
// `ctrprovider` 的 `svcprovider.NewProbe(` 抽掉（改成直接用 `archives`）→ 红；
// 把 `func (s *Service) ListProviders(` 加回 `manager`（真实现的形态，能编译）→ 红。

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

// archiveForwardMarkers 档案面四条转发（P4a 正文第四刀删掉的形状）。
//
// 判据是"**转发不许复活**"：`manager` 上再长出这四条方法，控制器就又有了一条绕过用例层的路，
// 而两条路都能编译、都能跑，只是其中一条没有生效面回执、也不在任何迁移名单里。
var archiveForwardMarkers = []string{
	"func (s *Service) ListProviders(",
	"func (s *Service) CreateProvider(",
	"func (s *Service) UpdateProvider(",
	"func (s *Service) DeleteProvider(",
}

// archiveUseCaseAnchors 档案用例层的实现锚（**实现**，不是转发：域不变量在域里，用例出口在这）。
var archiveUseCaseAnchors = []string{
	"func NewProvider(plane *provider.Plane) ProviderSvc",
	"var _ ProviderSvc = (*Provider)(nil)",
	"func (p *Provider) ListProviders(",
	"func (p *Provider) CreateProvider(",
	"func (p *Provider) UpdateProvider(",
	"func (p *Provider) DeleteProvider(",
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
	for _, banned := range []string{`"github.com/morehao/ark-mate/internal/store"`, `"github.com/morehao/ark-mate/internal/sessionruntime"`} {
		if strings.Contains(probSrc+portSrc, banned) {
			t.Errorf("`internal/service/svcprovider` 里出现 %s：用例层通过**端口**取事实（ADR-6），"+
				"它不 import 持久层、也不 import `manager`。", banned)
		}
	}

	// manager 侧：不得再有实现痕迹。
	mgr := filepath.Join(root, "internal", "sessionruntime")
	files := productionGoFiles(t, mgr)
	if len(files) == 0 {
		t.Fatalf("%s 下没有生产 .go 文件：守卫失去检查对象", mgr)
	}
	for _, path := range files {
		body := stripLineComments(readFileOrFatal(t, path))
		for _, m := range probeImplementationMarkers {
			if strings.Contains(body, m) {
				t.Errorf("internal/sessionruntime/%s 里出现自检用例的实现痕迹 %q：它**只有一份实现**，"+
					"在 `internal/service/svcprovider`（P4a 正文第三刀）。\n"+
					"manager 交出的是域平面（`ProviderPlane()`），适配在 `ctrprovider`。\n"+
					"在这里再写一遍的后果是上游段计数口径/超时口径静默分叉，而两边都编译得过。",
					filepath.Base(path), m)
			}
		}
		for _, m := range archiveForwardMarkers {
			if strings.Contains(body, m) {
				t.Errorf("internal/sessionruntime/%s 里又长出了档案面转发 %q：档案用例的出口是 "+
					"`internal/service/svcprovider.Provider`（P4a 正文第四刀，控制器自己造）。\n"+
					"转发复活的后果是控制器多一条绕过用例层的路——两条都能编译、都能跑，"+
					"只是其中一条没有生效面回执，也不在未来任何迁移名单里。",
					filepath.Base(path), m)
			}
		}
	}

	// 旧实现文件不得复活（比"文件里的痕迹"更硬：两份实现并存的起点）。
	old := filepath.Join(mgr, "provider_probe.go")
	if _, err := os.Stat(old); err == nil {
		t.Error("`internal/sessionruntime/provider_probe.go` 又存在了：供应商自检的实现归 " +
			"`internal/service/svcprovider`（P4a 正文第三刀）；适配在 `ctrprovider`。")
	}

	// 档案面（第四刀）：实现必须在本包，且四个动作一个不少。
	archSrc := stripLineComments(readFileOrFatal(t, filepath.Join(dir, "archive.go")))
	for _, a := range archiveUseCaseAnchors {
		if !strings.Contains(archSrc, a) {
			t.Errorf("internal/service/svcprovider/archive.go 里找不到 %q：档案用例的实现被改名/搬走了？"+
				"（这条守卫的判据就挂在它身上，失锚等于空转）", a)
		}
	}
	// 档案用例层同样只经域平面取事实：不 import 持久层、也不 import `manager`。
	for _, banned := range []string{`"github.com/morehao/ark-mate/internal/store"`, `"github.com/morehao/ark-mate/internal/sessionruntime"`} {
		if strings.Contains(archSrc, banned) {
			t.Errorf("`internal/service/svcprovider/archive.go` 里出现 %s：档案用例拿的是域平面"+
				"（`*provider.Plane`，装配点交下来），不 import 持久层、也不 import `manager`。", banned)
		}
	}
	// v3.1：装配根造两个用例；控制器只收接口。
	asm := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "assembly", "services.go")))
	for _, a := range []string{"svcprovider.NewProvider(", "svcprovider.NewProbe("} {
		if !strings.Contains(asm, a) {
			t.Errorf("`internal/assembly/services.go` 里找不到 %q：用例必须在唯一装配根造出来"+
				"（漏了它 ⇒ 自检端点直接 500 / 档案面 nil panic，而只测另一条的用例看不见）。", a)
		}
	}
	ctr := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "controller", "ctrprovider", "provider.go")))
	for _, a := range []string{"svcprovider.NewProbe(", "svcprovider.NewProvider("} {
		if strings.Contains(ctr, a) {
			t.Errorf("`ctrprovider` 里又出现 %q：用例实例只在装配根造，控制器只收接口。", a)
		}
	}
	// P4b：`ProviderPlane()` 删了；事实出口是 `Planes()` + `registry.Planes.Providers`。
	svcSrc := stripLineComments(readFileOrFatal(t, filepath.Join(mgr, "service.go")))
	planesSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "registry", "planes.go")))
	if !strings.Contains(svcSrc, "func (s *Service) Planes() *registry.Planes") ||
		!strings.Contains(planesSrc, "Providers    *provider.Plane") {
		t.Error("`Planes()` / `registry.Planes.Providers` 有一处不在：控制器拿不到供应商域平面 ⇒ " +
			"自检的域读面恒空，表现是**永远**报「档案不在册」（只在用户点\"测试连接\"时才现形）。")
	}

	// ——— P4a 正文第四十六刀：**剩下七条转发清零**，调用点直连域平面 ———
	//
	// 失效方式与前面几条同源但更细：转发层每留一条，"这段实现到底在哪一层"就少一处可见，
	// 而一行转发的正确性完全等价于被转发的那一行——**没有任何行为会因此变红**，所以判据只能
	// 钉"它不在"与"调用点直连"两头。七条是：四条装配入口（`SetProviderDir` /
	// `LoadProviderDefinitions` / `LoadProviderDefinitionsFromStore` /
	// `ReloadProviderDefinitionsFromStore`）与三条包内转发（`providerEnvFor` /
	// `injectedProviderID` / `providerModelRefsReadOnly`）。
	mgrAll := dirProductionBody(t, mgr)
	for _, dead := range []string{
		"func (s *Service) SetProviderDir(",
		"func (s *Service) LoadProviderDefinitions(",
		"func (s *Service) LoadProviderDefinitionsFromStore(",
		"func (s *Service) ReloadProviderDefinitionsFromStore(",
		"func (s *Service) providerEnvFor(",
		"func (s *Service) injectedProviderID(",
		"func (s *Service) providerModelRefsReadOnly(",
	} {
		if strings.Contains(mgrAll, dead) {
			t.Errorf("`internal/sessionruntime` 里又出现 `%s`：供应商域的过渡转发已随 P4a 正文第四十六刀清零"+
				"（调用点直连 `provider.Plane`）。转发层的存在没有判据价值——一行转发的正确性等价于"+
				"被转发的那一行，而它让「实现在哪一层」重新不可见。", dead)
		}
	}
	// 调用点那一头：装配与 gateway 必须**直连域平面**（少了这条，删掉转发只会把调用点改成
	// 另一个名字的转发，判据形同虚设）。
	for _, c := range []struct{ file, want, why string }{
		{"internal/assembly/meta.go", "as.planes.Providers.LoadProviderDefinitionsFromStore(",
			"serve 的装载入口"},
		{"internal/assembly/assembly.go", "as.planes.Providers.SetProviderDir(",
			"serve 的目录入口"},
		{"internal/gateway/source.go", "s.planes.Providers.ReloadProviderDefinitionsFromStore(",
			"gateway 的换代重载"},
		// 第五十四刀：建连时的密钥注入经**执行面端口**取（池的 `ProviderEnvSource`），
		// 适配器在 `runtime_pool.go`。锚跟着对象走，判据不变：调用点必须直连域平面。
		{"internal/sessionruntime/runtime_pool.go", "s.planes.Providers.ProviderEnvFor(",
			"建连时的密钥注入"},
		{"internal/sessionruntime/provider_effect.go", "s.planes.Providers.ProviderModelRefsReadOnly(",
			"写面回执的引用统计"},
	} {
		if !strings.Contains(stripLineComments(readFileOrFatal(t, filepath.Join(root, c.file))), c.want) {
			t.Errorf("`%s` 里找不到 `%s`（%s）：调用点必须直连域平面。"+
				"漏了会让这一条链悄悄退回某个中间层。", c.file, c.want, c.why)
		}
	}
	// 顺带把**日志转发**那一条钉住（同批清零的另一个文件）：`logAt` 是 `pkg/logging.At` 的一行转发，
	// manager 的 `logging.go` 已随本刀删除。
	if strings.Contains(mgrAll, "func logAt(") {
		t.Error("`internal/sessionruntime` 里又出现 `func logAt(`：日志出口的唯一实现是 `pkg/logging.At`" +
			"（P4a 正文第四十六刀删掉了 manager 的一行转发）。多出来的这一层会让「日志走哪个出口」" +
			"重新有两处答案，而 `TestOnlyLoggingPackageImportsSlog` 只看 slog 那一头，看不见它。")
	}
}
