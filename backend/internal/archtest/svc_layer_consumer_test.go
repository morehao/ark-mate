package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// ——— P4a/P4b：「只有控制器引用用例层」守卫 ———
//
// 依赖链是 `cmd → app → router → controller → service(端口) ← manager(实现)`。这条链里
// `service/svc*` 只该被**控制器**认识：控制器消费用例（它当然要认识端口），而
// `app`/`router`/`manager`/`domain`/`mechanism` 都不该 import 它。
//
// 这条边为什么必须有机器判据——它的失效**完全静默**：
//
//	① `manager` 为了写一行 `var _ svcX.XxxSvc = (*Service)(nil)` 而 import 用例层：断言看着
//	   更牢了，实际是把单向边变成双向的——用例层任何改名都牵动实现层，而"实现到底搬到了哪一层"
//	   重新变成不可见的（这一批把 5 条这样的断言删掉，断言上移到装配点）。
//	② `app`/`router` 为了造用例实例（`svcsystem.New(a.svc)`）而 import 用例层：装配点顺手
//	   多认识一层，于是"用例实例由谁造"没有答案，P4b/P5a 再想动装配顺序就得同时改三个包。
//
// 两种都不会让别的测试变红（编译得过、行为不变），所以只能由这条守卫拦。
//
// **判据只扫生产文件**（跳过 `_test.go`）：`manager` 的历史用例要**真跑**用例实现
// （`svcsystem.New(svc).ExportSessions(...)`、`svcruntime.New(svc.runtimePlane)`）来验
// "装配起来的那份 Service + 真用例"这条链路，那正是端到端用例该做的事；把它们算进来只会逼
// 守卫加一张越来越长的白名单，白名单一长这条判据就废了。生产文件的边界是清楚的：**进二进制的那部分**。
//
// 反证（实跑）：往 `internal/manager` 的任一生产文件加一行 `import ".../internal/service/svcsystem"`
// （并用一下那个符号，否则只是 build failure）→ 红；把扫描根指到不存在的目录 → Fatal。

// svcImportPrefix 用例层包的 import 路径前缀（`internal/service/svc*`）。
const svcImportPrefix = modulePrefix + "internal/service/svc"

// transitionalServiceImporters **过渡期白名单**：今天还在 `service/svc*` 上挂着类型名的包。
//
// 口径（AGENTS.md 的「三条硬规矩」第 1 条）是「只有 controller 可以引用 `service/svc*`」，而
// `router.Ports` 现在仍按域持有 svc 接口——那是**尚未迁移**的域留下的形状：实现还在 `manager`，
// controller 拿不到可构造的依赖，只能由装配点把实例递下去。这批迁移的目标正是把每一格换成
// 「依赖事实」（store / 注册表 / 执行面），controller 在自己构造函数里造 service。
//
// **每迁一个域就删一条**，全迁完这张表为空（`router` 只留依赖事实）。判据是**双向**的：
//   - 表里的包出现了 `service/svc*` 的 import ⇒ 允许（但要在下面写清为什么、什么时候能删）；
//   - 表里的包**不再** import 任何 `service/svc*` ⇒ **红**（条目过期了，删掉它——否则这张表会
//     慢慢变成"什么都放行"的摆设，与 §10.5.4 那个「清单自己烂掉」的教训同形）。
var transitionalServiceImporters = map[string]string{
	"internal/router": "装配点仍按域持有 svc 接口（该域的实现还在 manager，controller 造不出来）。" +
		"每迁一个域，`router.Ports` 的那一格换成依赖事实、controller 改成自己造 service，条目随之减少",
}

// TestOnlyControllersImportServicePackages 见文件头。
func TestOnlyControllersImportServicePackages(t *testing.T) {
	root := backendRoot(t)
	scanned, fromController, violations := 0, 0, 0
	allowedByAllowlist := map[string]bool{}
	for _, base := range []string{"internal", "pkg"} {
		for _, path := range productionGoFiles(t, filepath.Join(root, base)) {
			scanned++
			// 剥注释后只看**引号包着的** import 路径：注释里解释这条规则的文字不算命中
			// （本文件与若干被守的文件都在注释里写着 `service/svc*`）。
			body := stripLineComments(readFileOrFatal(t, path))
			if !strings.Contains(body, `"`+svcImportPrefix) {
				continue
			}
			slashed := "/" + filepath.ToSlash(rel(root, path))
			relDir := filepath.ToSlash(filepath.Dir(rel(root, path)))
			switch {
			case strings.Contains(slashed, "/internal/controller/"):
				fromController++
				continue
			case strings.Contains(slashed, "/internal/service/"):
				// 用例层自己（`svc*` 之间零互相 import 由 layer_isolation_test.go 单独钉）。
				continue
			case strings.Contains(slashed, "/internal/archtest/"):
				// 守卫自己的禁词表就写着这些路径（与其它词汇类守卫同一手法）。
				continue
			}
			if reason, ok := transitionalServiceImporters[relDir]; ok {
				_ = reason
				allowedByAllowlist[relDir] = true
				continue
			}
			violations++
			t.Errorf("只有控制器可以引用用例层：%s import 了 %s。\n"+
				"依赖链是 `router → controller → service(端口) ← manager(实现)`：\n"+
				"  · 实现层（manager）不要为了写编译期断言而 import 用例层——断言该写在做装配的那一行\n"+
				"    （`app.go` 的 `router.Ports` 字面量）；\n"+
				"  · 装配面（app/router）不要把用例实例造好再交进来——交**端口要的事实**，实例由控制器造；\n"+
				"  · 领域层/机制层更不该认识用例层（那会让「用例可以随便改」这条消失）。",
				rel(root, path), svcImportPrefix)
		}
	}
	if scanned == 0 {
		t.Fatalf("扫描面一个生产 .go 文件都没扫到（扫描根被搬走/改名了？）：判据失锚，守卫不许静默通过")
	}
	if fromController == 0 {
		t.Fatal("没有任何控制器 import 用例层：要么 `service/svc*` 被改名，要么控制器已改依赖具体类型——" +
			"两种都会让这条判据变成空转（它只证明「没人违规」，而违规门槛已经不对应现实）")
	}
	// 反向判据：白名单条目过期（那个包已经不再引用用例层）⇒ 红。没有这一半，白名单只会越来越长，
	// 最后变成"凡是导入过的都放行"——判据也就死了。
	for pkg, reason := range transitionalServiceImporters {
		if !allowedByAllowlist[pkg] {
			t.Errorf("过渡期白名单里的 %q 已经不再 import 用例层了：**删掉这条**（理由原样记着：%s）。\n"+
				"这张表只该随迁移**变短**；留着过期条目等于把这条判据慢慢改成摆设。", pkg, reason)
		}
	}
	if violations > 0 {
		t.Fatalf("越界 import 共 %d 处（见上）；扫描了 %d 个生产文件、其中 %d 个控制器文件合规",
			violations, scanned, fromController)
	}
}

// TestUseCaseInstancesAreBuiltByControllers 钉住"用例实例由谁造"。
//
// 三个用例实现（导出 / 底座 / 供应商自检）都不再是"装配点造好交进来"，而是**控制器在构造函数里
// 自己造**：控制器是唯一同时认识域与用例的层。这条改动的失效方式是静默的——把
// `svcsystem.New(a.svc)` 挪回 `app.go` 照样能编译、行为也一样，只是依赖链的边又多了一条。
//
// 判据两侧一起钉（都 fail-closed：文件读不到直接 Fatal）：
//
//	① 控制器侧必须有建造点（`svcsystem.New(` / `svcruntime.New(` / `svcprovider.NewProbe(`）——
//	   删掉它，用例实例就永远不会存在（而 nil 端口只在跑到那条路时才炸）；
//	② `app.go` 侧不得出现这三个包名——它就是"装配点不认识用例层"的判据。
//
// 为什么不去钉 `router`：`router` 的端口字段类型取自控制器包（`ctrsession.SessionSvc`…），
// 由 `TestOnlyControllersImportServicePackages` 统一拦；这里只管"谁 new 用例"。
func TestUseCaseInstancesAreBuiltByControllers(t *testing.T) {
	root := backendRoot(t)
	builders := []struct {
		name, file, anchor string
	}{
		{"导出用例", filepath.Join("internal", "controller", "ctrexport", "export.go"), "svcsystem.New("},
		{"底座用例", filepath.Join("internal", "controller", "ctrruntime", "runtime.go"), "svcruntime.New("},
		{"供应商自检用例", filepath.Join("internal", "controller", "ctrprovider", "provider.go"), "svcprovider.NewProbe("},
	}
	for _, b := range builders {
		src := stripLineComments(readFileOrFatal(t, filepath.Join(root, b.file)))
		if !strings.Contains(src, b.anchor) {
			t.Errorf("%s 的建造点不在控制器里：%s 找不到 %q。\n"+
				"用例实例必须由**控制器**造——`app`/`router` 不认识 `service/svc*`。\n"+
				"把它挪回装配点时不会有任何测试变红，只会让依赖链多一条边。", b.name, b.file, b.anchor)
		}
	}

	app := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "app", "app.go")))
	for _, banned := range []string{"svcsystem.", "svcruntime.", "svcprovider.", "service/svc"} {
		if strings.Contains(app, banned) {
			t.Errorf("`internal/app/app.go` 里出现 %q：装配点不认识用例层。\n"+
				"它该交出的是**端口要的事实**（执行面 `a.rconn`、事实来源 `a.svc`、域平面 "+
				"`a.svc.ProviderPlane()`），用例实例由控制器造（见本用例上半段）。", banned)
		}
	}
}
