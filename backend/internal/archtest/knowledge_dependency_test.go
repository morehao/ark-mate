package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dsh"
)

// ——— v18「知识归属」三条判据（第二个适配下线 + adapter 层收口）———
//
// 这一批解决的是同一类**静默**失效：知识回流到内核之后，代码照样编译、测试照样绿，
// 只是"接一个新 runtime"变成"回内核改 switch"——那正是"没有 adapter 层"的定义。
//
// 它们与前一批（`knowledge_boundary_test.go` 的 A9）的分工：A9 禁的是**字面量**
// （`OPENCODE_CONFIG_CONTENT` 这类底座配置格式），本文件禁的是**依赖**。
// 为什么要升级到依赖：字面量判据的扫描对象会随底座被删除而**一起消失**，
// 于是判据"零命中 → 绿"，而它实际已经空转；依赖判据不会，因为它的对象是"内核 → adapter"这条边本身。

// adapterImportRE 在剥注释后的 Go 源码里找 `internal/runtime/<adapter>` 的 import 路径。
//
// 采集根是 `internal/runtime/` 下的**子包**：`internal/runtime` 根包正是"父包汇入"发生的地方
// （它必须 import 各 adapter 才能构造 Spec 与执行表），而子包（= adapter 本体）被 import 到哪里
// 才是知识归属的判据。
var adapterImportRE = regexp.MustCompile(`"github\.com/morehao/ark-mate/internal/runtime/([a-z0-9_]+)"`)

// TestInternalDoesNotImportRuntimeAdapters 内核（`internal/**`）不得 import 任何 runtime adapter 子包。
//
// 为什么这条是"adapter 层"的机器判据：内核要用的只是**端口**（`runtime.Registry` 的 Spec /
// Syncer / NativePermission），而端口类型住在父包 `internal/runtime`。一旦内核 import 了
// `internal/infra/dsh`，它多半是在按 id 分派具体底座的知识（本次收口前内核的
// `native_permission.go` 就是那个样子：`switch runtimeID` + `opencodeActionAliases` +
// `opencodePermissionConfig`）——症状不是报错，而是
// **下一个底座接进来时必须回内核改代码**。
//
// 判据 fail-closed：扫不到任何 `internal/**` 的 .go 文件即失败（扫描根写错时不许静默空转）。
func TestInternalDoesNotImportRuntimeAdapters(t *testing.T) {
	root := filepath.Join(backendRoot(t), "internal")
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	if len(files) == 0 {
		t.Fatalf("在 %s 下没扫到任何 .go 文件：判据已失效（fail-closed），请修扫描根", root)
	}

	// 唯一豁免：archtest 自身。它的任务就是**逐条比对**三份隔离表 / 端口登记表，
	// 拿不到 adapter 的常量就没法做这件事（同 knowledge_boundary_test.go 对 A9 的自豁免）。
	selfDir := filepath.Join(root, "archtest")
	checked := 0
	for _, f := range files {
		if strings.HasPrefix(f, selfDir+string(filepath.Separator)) {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读不到 %s: %v", f, err)
		}
		src := stripGoComments(string(raw))
		checked++
		for _, m := range adapterImportRE.FindAllStringSubmatch(src, -1) {
			rel, _ := filepath.Rel(backendRoot(t), f)
			t.Errorf("%s import 了 runtime adapter 子包 %q——内核只许依赖父包 `internal/runtime` 的**端口**，"+
				"不得依赖任何具体底座的实现。按 id 分派底座知识的后果不是报错，而是"+
				"「下一个 runtime 接进来时要回内核改 switch」。把那段知识搬进该 adapter 自己的包，"+
				"经父包的执行表（Syncer / NativePermission）暴露", rel, m[1])
		}
	}
	if checked < 10 {
		t.Fatalf("只检查了 %d 个内核文件：扫描逻辑可能错了（fail-closed）", checked)
	}
}

// nativeModeConstRE 抠出 `NativeModeXxx = "value"` 形式的常量取值。
var nativeModeConstRE = regexp.MustCompile(`(?m)^\s*NativeMode[A-Za-z]*\s*=\s*"([^"]*)"`)

// nativeModeWireFileIn 在 `internal/**` 下**按符号**找出声明原生权限模式 wire 常量的那个文件
// （`NativeMode*` 这一族就是对外契约 `permission_policy.native_mode` 的事实源）。
//
// 为什么不钉文件路径：这一族常量随重构搬进领域层（`native_permission.go` 归 `domain/interaction`），
// 钉路径的判据搬完就成陈旧指针。改成"按符号 + 恰好一处"以后，搬家不影响判据，而在第二个文件里
// 再抄一份模式词表会被直接数出来——原来的写法只读钉死的那一个文件，看不到第二份。
//
// fail-closed：一处都找不到即失败（改名/搬迁会让这条判据静默空转）。
func nativeModeWireFileIn(t *testing.T, backendRootDir string) string {
	t.Helper()
	return uniqueSourceFile(t, filepath.Join(backendRootDir, "internal"),
		map[string]bool{"archtest": true, "dsh": true}, `(?m)^\s*NativeMode[A-Za-z]*\s*=\s*"`,
		"内核侧 `NativeMode*` wire 常量的声明处")
}

// nativeModeValuesOf 读一个文件并抠出 `NativeMode*` 常量取值（解析出 0 个即失败——fail-closed）。
func nativeModeValuesOf(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v（改名/搬迁请同步本判据）", path, err)
	}
	vals := nativeModeConstRE.FindAllStringSubmatch(stripGoComments(string(raw)), -1)
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, v[1])
	}
	if len(out) == 0 {
		t.Fatalf("%s 里解析出 0 个 NativeMode* 取值：判据已失效（fail-closed）", path)
	}
	sort.Strings(out)
	return out
}

// TestNativePermissionModeLiteralsMatchWire adapter 侧的原生权限模式字面量必须与内核的
// wire 常量**逐字相等**。
//
// 为什么会有两份：`permission_policy.native_mode` 是**对外契约**（定义文件 / REST / 前端表单），
// 事实源在内核侧声明 `NativeMode*` 常量那一处（现 `manager` 包，重构后随 `native_permission.go`
// 落 `domain/interaction`）；而 adapter 子包不能 import 内核（上一条判据钉的就是这个方向），
// 所以它只能自带字面量。同形先例：`dsh.SkillFormat` 与 `capability.FormatSkillMDFrontmatter`。
//
// 代价是"两边可能悄悄分叉"，而分叉的表现是**静默**的：内核把一个取值传给 adapter，adapter 落进
// `default` 分支报"未知 native_mode"——那句话只写进 `desc`，不注入就不落日志，于是
// "我设了 native_mode 却没反应"看起来像底座自己的默认行为。
//
// 判据 fail-closed：任一侧解析出 0 个取值即失败（改名/搬迁会让它静默空转，不许放过）；
// 内核侧**按符号定位**（不钉文件路径），且要求它全仓恰好一处。
func TestNativePermissionModeLiteralsMatchWire(t *testing.T) {
	root := backendRoot(t)
	wire := nativeModeValuesOf(t, nativeModeWireFileIn(t, root))
	// 目前的内置适配里只有 dsh 声明了"模式"这一维（原生面是逐动作表的底座不认 mode，
	// 那种形态的实现按端口契约自己处理——内核不参与）。
	adapter := nativeModeValuesOf(t, filepath.Join(root, "internal", "infra", "dsh", "native_permission.go"))

	if strings.Join(wire, "\x00") != strings.Join(adapter, "\x00") {
		t.Errorf("原生权限模式字面量已分叉：\n内核(wire) %v\nadapter    %v\n"+
			"两侧必须逐字相同——adapter 认不出的取值会被它当成「未知值」跳过，而那句话不落日志"+
			"（不注入时只写 desc），表现为「我设了 native_mode 却没反应」", wire, adapter)
	}
	// 取值集合应当封闭且非空（少一个 multiset 就会让上面那条相等判定失去意义）。
	if len(wire) < 5 {
		t.Errorf("wire 侧只解析出 %d 个模式取值（%v）：取值集应当至少覆盖 auto/off/read-only/workspace-write/danger-full-access", len(wire), wire)
	}
}

// runtimeIDCaseRE 抠出「按**字面量**底座 id 分派」的两种形态：
//
//	case "dsh":        / case "dsh", "rtx":
//	== "dsh" / != "dsh"
//
// 两种都要求引号**紧贴**等号或 `case`，所以 `runtime.SelectedRuntime()`、
// `spec.ID == other.ID`、`"dsh"` 作为普通数据（日志文案、字段取值）都不会被误伤。
var runtimeIDCaseRE = regexp.MustCompile(`(?:case\s+|==\s*|!=\s*)"([a-z0-9][a-z0-9_-]*)"`)

// TestInternalHasNoRuntimeIDSpecialCase 内核（`internal/**`）不得按**内置底座 id** 做特判。
//
// # 它挡的是什么
//
// `TestInternalDoesNotImportRuntimeAdapters` 钉的是"内核 → adapter 包"这条 **import 边**；
// 但同一类知识还有第二种回流形态：**不 import、只按 id 分派**。本次收口前内核的 `model_list.go`
// （`modelValueFor`，现归 `domain/provider`）就是教科书样本：
//
//	func modelValueFor(runtimeID, providerID, modelID string) (string, error) {
//	    switch runtimeID {
//	    case "dsh":      b, _ := json.Marshal([]string{providerID, modelID}); return string(b), nil
//	    case "opencode": return providerID + "/" + modelID, nil
//	    default:         return "", …model_value_unsupported…
//	    }
//	}
//
// 那段代码**没有 import 任何 adapter 包**（它认识的是字符串），所以上一条判据抓不到它；
// 而它的失效方式与 import 那条一模一样：接第三个底座的人在 `default` 撞墙，唯一自然的动作是
// **回内核加一个 `case "新底座"`**。这正是"没有 adapter 层"的定义，也正是本仓
// 「接一个新 runtime = 新建一个目录 + 一个 `xxxSpec()` + 挂端口实现」这句话要排除的东西。
//
// # 判据
//
// 对每个内置 spec 的 id（`dsh.Specs()`——**不是**写死的清单，所以加底座时本守卫
// 自动覆盖它），扫 `internal/**` 的非测试 Go 源码（剥注释），命中 `case "<id>"` / `== "<id>"` /
// `!= "<id>"` 即失败。
//
// # 刻意扫的面
//
//   - **只扫非 `_test.go`**：测试里按 id 命名运行时是**故意的**（夹具要造"某个具体底座的声明"
//     才能验分发），而那些字符串一个都不进产品代码路径。
//   - **只扫 `internal/**`**：`internal/runtime` 根包与各 adapter 子包正是**允许**认识 id 的地方
//     （父包负责汇入 Spec 与登记执行表，adapter 就是那个底座本身）。
//   - **豁免 `internal/archtest` 自身**：它的任务就是逐条比对 id 与声明（同上一条判据的自豁免）。
//
// fail-closed：任一扫描面为空即失败——判据自身失效时不许静默变绿。
func TestInternalHasNoRuntimeIDSpecialCase(t *testing.T) {
	specs := dsh.Specs()
	if len(specs) == 0 {
		t.Fatal("AllBuiltinSpecs() 返回空：本判据没有可比的 id（fail-closed），先修内置清单")
	}
	ids := make(map[string]bool, len(specs))
	for _, sp := range specs {
		ids[sp.ID] = true
	}

	root := filepath.Join(backendRoot(t), "internal")
	selfDir := filepath.Join(root, "archtest")
	var files []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasPrefix(path, selfDir+string(filepath.Separator)) {
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	if len(files) < 10 {
		t.Fatalf("只扫到 %d 个内核非测试文件：扫描根/过滤逻辑写错了（fail-closed）", len(files))
	}

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读不到 %s: %v", f, err)
		}
		src := stripGoComments(string(raw))
		rel, _ := filepath.Rel(backendRoot(t), f)
		for i, line := range strings.Split(src, "\n") {
			for _, m := range runtimeIDCaseRE.FindAllStringSubmatch(line, -1) {
				if !ids[m[1]] {
					continue
				}
				t.Errorf("%s:%d 按字面量底座 id %q 做特判——内核不得认识任何具体底座的名字。"+
					"后果不是报错，而是「接下一个 runtime 时要回内核再加一个 case」"+
					"（那正是没有 adapter 层的定义）。把这段知识搬进该 adapter 自己的包，经父包的执行表暴露："+
					"纯数据进 `Spec`，行为挂 `Registry` 的某张表（`Syncer` / `NativePermissionAdapter` /"+
					"`ModelValueAdapter`…）。要按 id 分派时，依据应当是**注册表里有没有这张表的实现**"+
					"（`reg.ModelValue(id)` 这类），而不是字符串比较。\n    源码：%s",
					rel, i+1, m[1], strings.TrimSpace(line))
			}
		}
	}
}
