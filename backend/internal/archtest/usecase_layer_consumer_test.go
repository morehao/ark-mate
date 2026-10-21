package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// ——— v3.1：「只有装配根与控制器引用用例层」守卫 ———
//
// 依赖链是 `cmd → cli → app → assembly → controller → 用例接口`。这条链里
// 用例层（`app/<领域>`）只该被两类包认识：
//
//	① `internal/bootstrap/assembly` —— 唯一装配根，**造**用例实例（`system.New(...)`）；
//	② `internal/adapters/inbound/http/controller/ctr<领域>` —— 消费用例，但只写**接口类型名**，不再构造。
//
// `bootstrap`/`router`/`domain`/`mechanism`/`sqlite/*` 都不该 import 它。
//
// 这条边为什么必须有机器判据——它的失效**完全静默**：
//
//	① `app`/`router` 为了造用例实例而 import 用例层：装配点顺手多认识一层，于是"用例实例由谁造"
//	   没有答案，下一次动装配顺序就得同时改三个包；
//	② `domain`/`mechanism` 认识用例层：用例层"可以随便改"这条消失（反向依赖把两者焊死）。
//
// 两种都不会让别的测试变红（编译得过、行为不变），所以只能由这条守卫拦。
//
// **判据只扫生产文件**（跳过 `_test.go`）：端到端用例要真跑用例实现，那正是它们该做的事；
// 把它们算进来只会逼守卫加一张越来越长的白名单。
//
// 反证（实跑）：往 `internal/bootstrap/app/app.go` 加一行 `import ".../internal/app/system"`
// （并用一下那个符号）→ 红；把扫描根指到不存在的目录 → Fatal。

// svcImportPrefix 用例层包的 import 路径前缀（`internal/app/*`）。
//
// C3 4.2 起用例环就是 `internal/app/<领域>`（原 `service/svc<领域>`）：前缀少了 `svc`，
// 判据不变——"只有装配根与控制器可以引用用例层"。
const svcImportPrefix = modulePrefix + "internal/app/"

// TestOnlyAssemblyAndControllersImportServicePackages 见文件头。
func TestOnlyAssemblyAndControllersImportServicePackages(t *testing.T) {
	root := backendRoot(t)
	scanned, fromAllowed, violations := 0, 0, 0
	for _, base := range []string{"internal"} {
		for _, path := range productionGoFiles(t, filepath.Join(root, base)) {
			scanned++
			// 剥注释后只看**引号包着的** import 路径：注释里解释这条规则的文字不算命中。
			body := stripLineComments(readFileOrFatal(t, path))
			if !strings.Contains(body, `"`+svcImportPrefix) {
				continue
			}
			slashed := "/" + filepath.ToSlash(rel(root, path))
			switch {
			case strings.Contains(slashed, "/internal/adapters/inbound/http/controller/"):
				fromAllowed++
				continue
			case strings.Contains(slashed, "/internal/bootstrap/assembly/"):
				fromAllowed++
				continue
			case strings.Contains(slashed, "/internal/adapters/inbound/cli/maintenance/"):
				// `mate demo` / `mate doctor` 是**运维与验证命令**，与 cliops 同性质的入站面：
				// demo 刻意只要"最小执行面"（不起 HTTP、不建路由），所以它直接构造编排器。
				// 它们不参与产品装配，也不持有路由，放行不改变"用例实例由装配根造"这条。
				fromAllowed++
				continue
			case strings.Contains(slashed, "/internal/app/"):
				// 用例层自己（`svc*` 之间零互相 import 由 layer_isolation_test.go 单独钉）。
				continue
			case strings.Contains(slashed, "/internal/archtest/"):
				// 守卫自己的禁词表就写着这些路径（与其它词汇类守卫同一手法）。
				continue
			}
			violations++
			t.Errorf("只有装配根与控制器可以引用用例层：%s import 了 %s。\n"+
				"依赖链是 `assembly → controller → service(接口)`：\n"+
				"  · 用例实例只在 `internal/bootstrap/assembly` 造；控制器只写接口类型名；\n"+
				"  · `app`/`cli`/`router` 不认识用例层（它们只消费 `assembly.BuildResult` 与控制器接口）；\n"+
				"  · 领域层/机制层更不该认识用例层（那会让「用例可以随便改」这条消失）。",
				rel(root, path), svcImportPrefix)
		}
	}
	if scanned == 0 {
		t.Fatalf("扫描面一个生产 .go 文件都没扫到（扫描根被搬走/改名了？）：判据失锚，守卫不许静默通过")
	}
	if fromAllowed == 0 {
		t.Fatal("没有任何包 import 用例层：要么用例层（`app/<域>`）被改名，要么装配根已改依赖具体类型——" +
			"两种都会让这条判据变成空转（它只证明「没人违规」，而违规门槛已经不对应现实）")
	}
	if violations > 0 {
		t.Fatalf("越界 import 共 %d 处（见上）；扫描了 %d 个生产文件、其中 %d 处在允许面",
			violations, scanned, fromAllowed)
	}
}

// TestUseCaseInstancesAreBuiltByAssembly 钉住"用例实例由谁造"。
//
// v3.1 §3.4：用例实例**只在装配根**造（`internal/bootstrap/assembly/services.go`），控制器只收接口。
// 这条改动的失效方式是静默的——把 `system.New(...)` 挪回控制器照样能编译、行为也一样，
// 只是"依赖方向可验证"这条目标又失效了。
//
// 判据两侧一起钉（都 fail-closed：文件读不到直接 Fatal）：
//
//	① 装配根必须有建造点（`system.New(` / `runtime.New(` / `model.New(`）；
//	② `internal/bootstrap/app` 与 `internal/adapters/inbound/http/router` 侧不得出现用例层包名。
func TestUseCaseInstancesAreBuiltByAssembly(t *testing.T) {
	root := backendRoot(t)
	builders := []struct {
		name, file, anchor string
	}{
		{"导出用例", filepath.Join("internal", "bootstrap", "assembly", "services.go"), "system.New("},
		{"底座用例", filepath.Join("internal", "bootstrap", "assembly", "services.go"), "runtime.New("},
		{"供应商自检用例", filepath.Join("internal", "bootstrap", "assembly", "services.go"), "model.NewProbe("},
		{"会话读面用例", filepath.Join("internal", "bootstrap", "assembly", "services.go"), "session.NewRead("},
	}
	for _, b := range builders {
		src := stripLineComments(readFileOrFatal(t, filepath.Join(root, b.file)))
		if !strings.Contains(src, b.anchor) {
			t.Errorf("%s 的建造点不在装配根里：%s 找不到 %q。\n"+
				"用例实例必须由 `internal/bootstrap/assembly` 造——`app`/`router` 不认识用例层（`app/<域>`）。",
				b.name, b.file, b.anchor)
		}
	}

	// ② 进程壳与路由不得 import 用例层。
	//
	// 判据按 **import 路径**而不是限定符文本：用例包名去掉 `svc` 前缀之后（C3 4.2），
	// `runtime.` / `session.` / `model.` 在装配面本来就是别的包（`internal/runtime`、
	// `domain/session`、`adapters/outbound/sqlite/model`）的限定符——按文本扫会既误伤又漏判。
	for _, dir := range []string{
		filepath.Join(root, "internal", "bootstrap", "app"),
		filepath.Join(root, "internal", "adapters", "inbound", "http", "router"),
	} {
		imports := importedByProductionPackage(t, dir)
		if len(imports) == 0 {
			t.Fatalf("%s 未解析到任何 import：判据失去对象", dir)
		}
		for _, imp := range imports {
			if strings.HasPrefix(imp, svcImportPrefix) {
				t.Errorf("%s import 了用例层 %s：装配面不认识用例层。\n"+
					"它该交出/消费的是**已构造的接口**（`assembly.BuildResult.Services` 与控制器接口）。",
					rel(root, dir), imp)
			}
		}
	}
}
