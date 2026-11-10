package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/appslot"
)

// ——— WP-C5：运行期 impl 只能经隔离 host 执行 / 陌生代码绝不进内核进程 ———
//
// 二期 C 的隔离契约（决策 v37 安全基线 + 设计 §17.2）：**运行期应用包是第三方代码**，它的 impl /
// 工具**只能**在 `mate app-host` 隔离子进程里执行，内核进程绝不同进程加载或直接调用包内代码。
// 「在册」可以扩为「编译期 ∪ 已装包清单」（C2/C5），但**执行**这条线不可退让——一旦内核进程里出现
// 「直接 import 运行期包加载 / plugin.Open / os/exec 跑包代码」的路径，隔离就形同虚设，而所有既有
// 测试仍会全绿（陌生代码没跑起来时不报错）。两条静态守卫把这条线拍成机器判据。

// TestRuntimeImplOnlyViaHost：运行期 impl 的执行只经 appslot.ImplResolver 端口（装配根用 C3 的
// ImplRunner 封成走 Host.ApplySlot RPC），内核包**不得**出现「运行期来源取到真函数直接调」的旁路。
//
// 判据（行为 + 静态两层）：
//  1. 行为钉：用一个「运行期来源」的 impl（只在 RuntimeSource 在册、SourceImplResolver 里故意**不**给
//     执行函数）喂给 ReconcileWithResolver，断言该 slot FAILED——证明「运行期在册」不等于「内核里能
//     拿到可执行函数直接跑」；要跑必须经 host（C3 ImplRunner 提供 exec）。
//  2. 静态钉：`internal/core/appslot` 与 `internal/core/apppkg` 的生产源码不得 import
//     `internal/infra/apphost`（执行/加载入口）——appslot 是最内层契约、apppkg 是包清单，二者都
//     不该反向依赖执行层；从依赖方向上也堵死「在内核直接跑包代码」。
//
// 反证：若给 SourceImplResolver 的运行期在册 id 塞一个真 exec（模拟「内核直接拿到了函数」），行为钉
// 不再 FAILED → 本条红——这正是要防的退化形态（内核直调运行期代码）。
func TestRuntimeImplOnlyViaHost(t *testing.T) {
	// ① 行为钉：运行期来源在册、但无内核可执行函数 → slot 必 FAILED（执行必须经 host 提供 exec）。
	const runtimeOnlyID = "archtest.c5.runtime.only"
	runtime := appslot.NewRuntimeSource([]appslot.RuntimeImplSpec{
		{ImplID: runtimeOnlyID, Kind: "slot", PackageID: "vendor-app"},
	})
	combined := appslot.CombinedSource(appslot.CompiledSource(), runtime)
	// 只声明来源在册，不给 exec —— 模拟「内核对运行期 impl 拿不到可执行函数，只能靠 host」。
	resolver := appslot.NewSourceImplResolver(combined, nil, nil)

	slots := []appslot.Slot{{ImplID: runtimeOnlyID, AppID: "vendor-app"}}
	results, dispose := appslot.ReconcileWithResolver(slots, resolver)
	defer dispose()
	if len(results) != 1 || results[0].Status != appslot.StatusFailed {
		t.Fatalf("运行期 impl 无 host exec 时必须 FAILED（内核不得直调包代码），got %+v", results)
	}

	// ② 静态钉：appslot / apppkg 生产源码不得 import 执行层 apphost。
	for _, pkg := range []string{"core/appslot", "core/apppkg"} {
		dir := filepath.Join(backendRoot(t), "internal", filepath.FromSlash(pkg))
		paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("扫 %s: %v", dir, err)
		}
		scanned := 0
		fset := token.NewFileSet()
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			scanned++
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("解析 %s: %v", path, err)
			}
			for _, imp := range f.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				if strings.Contains(p, "internal/infra/apphost") {
					t.Errorf("%s import 了 %s —— 运行期 impl 的执行只能经装配根的 ImplResolver 端口"+
						"（ImplRunner→Host.ApplySlot RPC）；appslot/apppkg 不得反向依赖执行层 host，"+
						"否则内核可能同进程直调包代码（隔离失效）", path, p)
				}
			}
		}
		if scanned == 0 {
			t.Fatalf("internal/%s 一个源文件都没扫到——目录改名了？（守卫空转）", pkg)
		}
	}
}

// TestUntrustedCodeNeverInProc：`internal/**` 生产源码里，**起陌生代码子进程 / 加载运行时插件**的入口
// （`os/exec`、`plugin`）只允许出现在白名单包内。
//
// 例外集（先 grep 现状定的合法用法，逐条登记理由，别误伤）：
//   - `infra/apphost/`：app-host 子进程的**父侧**——用 os/exec 拉起**自己的**子进程
//     （`mate app-host --pkg`，内核自身二进制的另一子命令），这正是隔离执行通道，合法。
//   - `infra/host/`、`infra/installed/`、`infra/dsh/`、`infra/protocol/`、`infra/officeconv/`：
//     既有底座适配层，各自拉起 dsh/ACP/MCP/office 等**已知**子进程（非运行期应用包的陌生代码），
//     不属 C 的威胁面。
//   - `bootstrap/maintenance/`：`mate app-host` 子命令入口（AppHost），它就是承载陌生代码的**那一侧**
//     进程，允许（读包清单/校验完整性，跑在子进程里）。
//
// 判据只盯「跑**陌生代码**」的入口，不搞一刀切禁 os/exec——那会把上述合法底座适配全打红。
// fail-closed：一个 .go 文件都扫不到即失败（防目录改名空转）。
//
// 反证：在 `internal/core/appslot`（非白名单）里 import `plugin` 或写 `exec.Command` → 红。
func TestUntrustedCodeNeverInProc(t *testing.T) {
	root := filepath.Join(backendRoot(t), "internal")

	// 允许「起陌生代码入口」的包前缀（相对 backend/internal，正斜杠）。见函数头逐条理由。
	allowedPrefixes := []string{
		"infra/apphost/",         // 隔离 host 父侧：拉起 mate app-host 自己的子进程（执行通道，合法）
		"infra/host/",            // 底座 host 适配（拉起 dsh/zenity 等已知子进程）
		"infra/installed/",       // 客户端探测（跑已知 CLI）
		"infra/dsh/",             // dsh 底座适配（拉起 dsh 子进程）
		"infra/protocol/",        // ACP/MCP stdio 协议层（拉起已知协议的子进程）
		"infra/officeconv/",      // office 转换（跑 soffice 等已知工具）
		"bootstrap/maintenance/", // mate app-host 子命令入口（AppHost，承载侧进程）
	}

	// 视为「跑陌生代码 / 加载运行时插件」的导入。
	dangerImports := map[string]bool{
		"os/exec": true,
		"plugin":  true,
	}

	scanned := 0
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		relSlash := filepath.ToSlash(rel)
		scanned++
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("解析 %s: %v", path, perr)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if !dangerImports[p] {
				continue
			}
			if !hasAnyPrefix(relSlash, allowedPrefixes) {
				t.Errorf("internal/%s import 了 %q —— 内核进程里的陌生代码执行入口（os/exec/plugin）"+
					"只允许出现在隔离侧/已知底座适配（白名单：%v）。运行期应用包只能在 app-host 子进程里跑，"+
					"内核进程不得起第三方进程/加载 plugin（隔离失效）", relSlash, p, allowedPrefixes)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历 %s: %v", root, walkErr)
	}
	if scanned == 0 {
		t.Fatalf("internal/ 下一个生产 .go 文件都没扫到——守卫空转")
	}
}

// hasAnyPrefix 报告 s 是否以 prefixes 中任一为前缀。
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
