package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 本文件是 **v24–v25「路由键隔离 + 容量声明」** 的四条守卫（设计
// `.dsh/docs/specs/2026-10-15-provider-model-identity-design.md` §5.1/§5.2 的 §7 守卫①②③④；
// 计划 `.dsh/docs/plans/2026-10-15-provider-model-identity-plan.md` §4.M2.T6）。
//
// 它们挡的失败模式**全是静默的**：
//
//	① 路由键没加保留前缀 ⇒ 交付的键命中 pi-ai 内置 provider，那一家的端点/协议/模型目录变成这条
//	   路由的逐字段默认值——"平台配了 A，实际按 A 的目录解析出 B"，配置里看不出任何异常；
//	② `displayName` 又写成路由键 ⇒ 用户给供应商起的名字在交付面上不可见（库里叫「内网网关 Alpha」，
//	   界面上显示 `alpha-gw`），而且这一点不会让任何别的测试变红；
//	③ 派生点分叉 ⇒ 配置里声明的键与 ACP 模型值的第一段不一致，**所有选了模型的会话立刻起不来**
//	   （`set_config_option` 找不到 provider）。这一条是本次最要紧的：它必须静态 + 动态各钉一次；
//	④ 容量列写成 `NOT NULL DEFAULT 0` ⇒ "未声明"与"声明成 0"在库里长得一模一样，而 0 会被底座的
//	   `min(1)` 校验门拒掉——交付被拒而平台侧自报正常。

// dshAdapterDir 返回 `pkg/runtime/dsh`（这四条守卫的扫描面）。
func dshAdapterDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(backendRoot(t), "pkg", "runtime", "dsh")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("adapter 目录不存在：%s（%v）——守卫失去对象，fail-closed", dir, err)
	}
	return dir
}

// dshProductionFiles 返回 `pkg/runtime/dsh` 下**进包**的 Go 文件（排除 `_test.go`）。
//
// 为什么排除测试：守卫③的判据是"**生产路径**上的派生点只有一个"——测试可以（也应该）用
// **写死的字面期望**去断言交付字节（golden 的价值就在期望不由被测实现生成）。
func dshProductionFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, p := range goFilesUnder(t, dshAdapterDir(t)) {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("一个生产文件都没扫到（fail-closed）")
	}
	return out
}

// readStripped 读一个文件并剥掉 `//` 行注释（守卫看的是代码）。
func readStripped(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v", path, err)
	}
	return stripLineComments(string(raw))
}

// TestDshRouteKeyIsNamespaced 守卫①：交给 dsh 的**每一个**路由键都必须带平台保留前缀。
//
// 判据落在**产物**上（`testdata/dsh-v12.patch.yml`，即交付字节的等价性基线），而不是"生成器里
// 写了前缀"——后者只要有一条分支漏掉就照样绿。解析不到 `providers:` 键即失败（fail-closed：
// 文件被挪走/改名后，这条守卫必须响，而不是静默空转）。
func TestDshRouteKeyIsNamespaced(t *testing.T) {
	path := filepath.Join(dshAdapterDir(t), "testdata", "dsh-v12.patch.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到交付面基线 %s: %v（fail-closed：守卫失去对象）", path, err)
	}
	// YAML 注释以 `#` 开头；这里只需要"哪些行是 providers 下的键"，所以按行解析。
	inProviders := false
	keys := []string{}
	for _, ln := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(ln, "    providers:") {
			inProviders = true
			continue
		}
		if !inProviders {
			continue
		}
		// 路由键是缩进 6 空格的 `"<k>":`（`yamlQuote` **总是**加引号，所以"带引号"就是它的判别
		// 特征——同级的 `models:` 是裸键，不能被算进来）。
		if strings.HasPrefix(ln, "      \"") && strings.HasSuffix(ln, "\":") {
			keys = append(keys, strings.TrimSuffix(strings.TrimPrefix(ln, "      \""), "\":"))
			continue
		}
		// 缩进更浅的非空行 = providers 段结束（`models:` 之类缩进更深的行继续跳过）。
		if strings.TrimSpace(ln) != "" && !strings.HasPrefix(ln, "      ") {
			inProviders = false
		}
	}
	if len(keys) == 0 {
		t.Fatalf("%s 里一个 `providers.<k>` 键都没解析到（fail-closed：判据自身失效了）", path)
	}
	const wantPrefix = "mate-"
	for _, k := range keys {
		if !strings.HasPrefix(k, wantPrefix) {
			t.Errorf("路由键 %q 没有带平台保留前缀 %q——命中 pi-ai 内置 provider 时，"+
				"那一家的端点/协议/模型目录会变成这条路由的逐字段默认值（静默错配）", k, wantPrefix)
		}
	}
	// 反向：`provider:` 值（默认模型那一行）同样必须是派生键——它也要与路由声明的键逐字相同。
	for _, ln := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(ln, "    provider: ") {
			continue
		}
		v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(ln, "    provider: ")), `"`)
		if !strings.HasPrefix(v, wantPrefix) {
			t.Errorf("`- id: acp` 的 provider 值 %q 没有带保留前缀——它与路由声明的键必须同源", v)
		}
	}
}

// TestDshDisplayNameComesFromProviderName 守卫②：`displayName` 交付**供应商展示名**（空则回落），
// 不再是路由键本身。
//
// fail-closed：解析不到 `displayName:` 那一行即失败——改了生成器的写法（比如拆成两行）时这条守卫
// 必须响，而不是变成一句没人执行的话。
func TestDshDisplayNameComesFromProviderName(t *testing.T) {
	cfg := filepath.Join(dshAdapterDir(t), "config.go")
	src := readStripped(t, cfg)

	var line string
	for _, ln := range strings.Split(src, "\n") {
		if strings.Contains(ln, "displayName: ") {
			if line != "" {
				t.Fatalf("%s 里有多处 `displayName:` 写入点：本条守卫只认一处（多一处就要多一条判据）", cfg)
			}
			line = ln
		}
	}
	if line == "" {
		t.Fatalf("%s 里找不到写 `displayName:` 的那一行（fail-closed：形态变了，守卫不许静默空转）", cfg)
	}
	if !strings.Contains(line, "displayNameOf(p, route)") {
		t.Errorf("`displayName` 的取值必须经 `displayNameOf(p, route)`（供应商展示名，空则回落路由键），"+
			"实际那一行是：%s", strings.TrimSpace(line))
	}
	// 反向：旧写法（键与值都是 `route`）不许回来——那正是"展示名不可见"的那个形态。
	if strings.Contains(line, "yamlQuote(route), yamlQuote(route)") {
		t.Errorf("`displayName` 退回了路由键本身（旧形态）：%s", strings.TrimSpace(line))
	}

	// 回落链的另一半：`displayNameOf` 必须真的读 `p.Name`。
	helper := filepath.Join(dshAdapterDir(t), "route_key.go")
	hsrc := readStripped(t, helper)
	if !strings.Contains(hsrc, "p.Name") {
		t.Errorf("%s 里的 `displayNameOf` 没有读 `p.Name`——展示名不再来自供应商的书", helper)
	}
}

// TestDshRouteKeyDerivationIsSinglePoint 守卫③（静态那半）：路由键的派生**只有一个点**，且
// 配置面与 ACP 值的调用点恰好三处。
//
// 为什么必须这么紧：`config.go` 里声明的键（两处：`providers.<k>` 与 `- id: acp` 的 `provider:`）
// 与 `model_value.go` 里编出的 ACP 值的第一段必须**逐字相同**。它们分布在两个文件里，只改一处
// 就是"所有选了模型的会话立刻起不来"——而这件事在静态上完全看不出来。
//
// 动态那半是 `pkg/runtime/dsh` 的 `TestDshRouteKeyAgreesBetweenPatchAndModelValue`（真值比对，
// 不占 archtest 的条数）。
func TestDshRouteKeyDerivationIsSinglePoint(t *testing.T) {
	files := dshProductionFiles(t)

	callSites := map[string]int{}
	prefixDecls, prefixFiles, literalFiles := 0, map[string]bool{}, []string{}
	for _, path := range files {
		base := filepath.Base(path)
		src := readStripped(t, path)
		// 调用点 = `routeKey(` 的出现次数减去**定义**那一处（`func routeKey(`）。
		if c := strings.Count(src, "routeKey("); c > 0 {
			defs := strings.Count(src, "func routeKey(")
			if c-defs > 0 {
				callSites[base] += c - defs
			}
		}
		if c := strings.Count(src, "const routePrefix"); c > 0 {
			prefixDecls += c
		}
		if strings.Contains(src, "routePrefix") {
			prefixFiles[base] = true
		}
		if strings.Contains(src, `"mate-"`) {
			literalFiles = append(literalFiles, base)
		}
	}

	if prefixDecls != 1 {
		t.Fatalf("`const routePrefix` 应恰好声明 1 处，实际 %d 处——保留前缀必须只有一个事实源", prefixDecls)
	}
	if len(prefixFiles) != 1 || !prefixFiles["route_key.go"] {
		t.Fatalf("`routePrefix` 只许出现在 route_key.go（唯一派生点），实际出现在 %v——"+
			"散出去的那一刻，派生就开始分叉", sortedKeys(prefixFiles))
	}
	for _, f := range literalFiles {
		if f != "route_key.go" {
			t.Errorf("`\"mate-\"` 字面量出现在 %s——保留前缀只能由 routePrefix 给出"+
				"（手写拼串 = 第二个事实源）", f)
		}
	}
	want := map[string]int{"config.go": 2, "model_value.go": 1}
	if len(callSites) != len(want) {
		t.Fatalf("`routeKey(` 的调用点分布不符：want %v，got %v", want, callSites)
	}
	for f, n := range want {
		if callSites[f] != n {
			t.Fatalf("`routeKey(` 在 %s 的调用点应为 %d 处，实际 %d 处（全部分布：%v）"+
				"——少一处就是配置面与 ACP 值分叉，所有选了模型的会话起不来", f, n, callSites[f], callSites)
		}
	}
}

// sortedKeys 把 set 的键排序后返回（报错信息要确定）。
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestModelCapacityColumnsAreNullable 守卫④：容量两列**可空、无 DEFAULT**，且 adapter 只在非空时
// 下发该字段。
//
// 判据落在**真 schema**（跑完整迁移后读 `PRAGMA table_info`）——DDL 文本在 25 条迁移里被改了又改，
// 文本扫描要么把历史名字算进来（假红），要么要写一个 DDL 模拟器（自己成了新的出错面）。
//
// 反证（实跑）：给 `models.max_input_tokens` 加 `DEFAULT 0` → 本用例红。
func TestModelCapacityColumnsAreNullable(t *testing.T) {
	db := migratedSchemaDB(t)
	for _, col := range []string{"max_input_tokens", "max_output_tokens"} {
		var (
			notNull int
			dflt    *string
			ctype   string
		)
		if err := db.Raw(`SELECT "notnull", dflt_value, type FROM pragma_table_info('models') WHERE name = ?`, col).
			Row().Scan(&notNull, &dflt, &ctype); err != nil {
			t.Fatalf("读 models.%s 的列定义失败（fail-closed）：%v", col, err)
		}
		if ctype != "INTEGER" {
			t.Errorf("models.%s 的类型应为 INTEGER，实际 %q", col, ctype)
		}
		if notNull != 0 {
			t.Errorf("models.%s 必须可空：NOT NULL 会逼着每一行携带一个假值（0 不是合法容量，"+
				"底座的 schema 是 min(1)）", col)
		}
		if dflt != nil {
			t.Errorf("models.%s 必须**无 DEFAULT**（实际 %q）：写了默认值之后，"+
				"「未声明」与「声明成那个值」在库里长得一模一样", col, *dflt)
		}
	}

	// 实体 gorm 标签同步：带 `default:` 的标签会让 gorm 在插入时补值（编译期无感，运行期才看得出来）。
	entity := readStripped(t, filepath.Join(backendRoot(t), "internal", "model", "model.go"))
	for _, col := range []string{"max_input_tokens", "max_output_tokens"} {
		needle := "column:" + col
		idx := strings.Index(entity, needle)
		if idx < 0 {
			t.Fatalf("ModelEntity 里找不到 `%s` 列标签", needle)
		}
		// 标签的结束位置 = 反引号或换行。
		rest := entity[idx:]
		if end := strings.IndexAny(rest, "`\n"); end >= 0 {
			rest = rest[:end]
		}
		if strings.Contains(rest, "default") {
			t.Errorf("`%s` 的 gorm 标签带了默认值：%s", needle, rest)
		}
	}

	// adapter：只在非空时写。少了这个判空，nil 会被 `%d` 解引用（panic）或在别的写法下写成 `0`。
	cfg := readStripped(t, filepath.Join(dshAdapterDir(t), "config.go"))
	for _, guard := range []string{"m.MaxInputTokens != nil", "m.MaxOutputTokens != nil"} {
		if !strings.Contains(cfg, guard) {
			t.Errorf("config.go 里找不到 %q 的判空——空容量必须**不下发**那个字段", guard)
		}
	}
	for _, banned := range []string{"contextWindow: 0", "maxTokens: 0"} {
		if strings.Contains(cfg, banned) {
			t.Errorf("config.go 里出现 %q：0 不是合法容量（底座 schema 是 min(1)），必须整个字段不写", banned)
		}
	}
}
