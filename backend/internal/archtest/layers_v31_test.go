package archtest

import (
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ——— 后端架构 v3.1 的层表守卫（P0 起） ———
//
// 设计口径见 `.dsh/docs/specs/2026-10-17-backend-architecture-v3.1.md` §3.2：
// 全应用一套 `controller → service → store/dao/model`；`assembly` 是唯一装配根，
// `app` 只是应用壳，`cli` 是命令树。
//
// 与历史上"逐条名字扫描"的守卫不同，这里只维护**一张层表**：
//
//	层号 ↑ 越高，只能 import 更低层；同层 import 必须显式声明；
//	每层另有显式 deny 前缀（用于「虽然更低但也不许」的边，例如 domain ↛ dao）。
//
// 失效方式与 `TestFrontendUiScanRootsCoverAllUi` 同形：层表搬走/改名后判据会**静默空转**
// （照样绿，只是不再检查任何东西），所以本文件同时 fail-closed：
//   - 扫到 0 个包 → 失败；
//   - 某个包映射不到任何层 → 失败；
//   - 例外清单里出现**新增**边 → 失败；例外降到 0 时条目必须删除（双向判据）。
//
// P0–P5 期间 `v31LayerGuardBlocking = false`（只报告，不失败）；P6 收尾时改为 true。

// v31LayerGuardBlocking 是否把违规判红。P6 起为 true。
const v31LayerGuardBlocking = false

// v31Layer 是层表的一行。
type v31Layer struct {
	// layer 层号（0 = 最底层）。
	layer int
	// sameLayer 是否允许同层 import（默认只允许 import 严格更低层）。
	sameLayer bool
	// deny 显式禁止的 import 前缀（覆盖"更低层即可 import"的默认规则）。
	deny []string
	// name 人类可读的层名（错误文案用）。
	name string
}

// v31Layers 是 §3.2.2 的层序 + §3.2.3 的允许/禁止矩阵。
//
// key 是**目标路径**（internal/...）；过渡期的 `pkg/...` 由 v31LegacyPaths 映射到同一个 key。
var v31Layers = map[string]v31Layer{
	// ——— 0 层：纯值对象 / 版本 ———
	"internal/object":   {layer: 0, name: "object"},
	"internal/redact":   {layer: 0, name: "redact"},
	"internal/identity": {layer: 0, name: "identity"},
	"internal/resource": {layer: 0, name: "resource"},
	"internal/version":  {layer: 0, name: "version"},

	// ——— 1 层：schema / 总线 / 基础设施 ———
	"internal/model":             {layer: 1, name: "model", deny: []string{"internal/store", "internal/dao", "internal/domain", "internal/service", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},
	"internal/eventbus":          {layer: 1, name: "eventbus", deny: []string{"internal/domain", "internal/service", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},
	"internal/capability":        {layer: 1, name: "capability"},
	"internal/infra/logging":     {layer: 1, name: "infra/logging"},
	"internal/infra/credentials": {layer: 1, name: "infra/credentials"},
	"internal/infra/procreg":     {layer: 1, name: "infra/procreg"},
	"internal/infra/dbclient":    {layer: 1, name: "infra/dbclient"},

	// ——— 2 层：协议客户端 / 错误码 / 跨域机制门面 ———
	"internal/infra/acp":  {layer: 2, name: "infra/acp"},
	"internal/infra/mcp":  {layer: 2, name: "infra/mcp"},
	"internal/code":       {layer: 2, name: "code"},
	"internal/automation": {layer: 2, name: "automation", sameLayer: true},
	"internal/credential": {layer: 2, name: "credential", deny: []string{"internal/service", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},

	// ——— 3 层：adapter 契约 + 注册表 ———
	"internal/runtime": {layer: 3, name: "runtime", deny: []string{"internal/infra/dsh", "internal/domain", "internal/service", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},

	// ——— 4 层：唯一 adapter（只能向下） ———
	"internal/infra/dsh": {layer: 4, name: "infra/dsh", deny: []string{"internal/model", "internal/eventbus", "internal/capability", "internal/credential", "internal/code", "internal/automation", "internal/config", "internal/dto", "internal/dao", "internal/store", "internal/mechanism", "internal/domain", "internal/runtimeconn", "internal/registry", "internal/service", "internal/sessionruntime", "internal/middleware", "internal/gateway", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},

	// ——— 5 层：应用配置 ———
	"internal/config": {layer: 5, name: "config", deny: []string{"internal/domain", "internal/service", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},

	// ——— 6 层：对外契约 / 单表访问 ———
	"internal/dto": {layer: 6, name: "dto", deny: []string{"internal/store", "internal/domain", "internal/service", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},
	"internal/dao": {layer: 6, name: "dao", deny: []string{"internal/store", "internal/domain", "internal/service", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},

	// ——— 7 层：跨表事务 + 读模型 ———
	"internal/store": {layer: 7, name: "store", deny: []string{"internal/domain", "internal/runtimeconn", "internal/registry", "internal/service", "internal/sessionruntime", "internal/middleware", "internal/gateway", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},

	// ——— 8 层：跨域机制 ———
	"internal/mechanism": {layer: 8, name: "mechanism", sameLayer: true, deny: []string{"internal/domain", "internal/runtimeconn", "internal/registry", "internal/service", "internal/sessionruntime", "internal/middleware", "internal/gateway", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},

	// ——— 9 层：领域 ———
	"internal/domain": {layer: 9, name: "domain", deny: []string{"internal/dao", "internal/runtimeconn", "internal/registry", "internal/service", "internal/sessionruntime", "internal/middleware", "internal/gateway", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},

	// ——— 10 层：执行面 / 装配平面 / 用例 / 会话编排 ———
	"internal/runtimeconn":    {layer: 10, name: "runtimeconn", sameLayer: true, deny: []string{"internal/service", "internal/middleware", "internal/gateway", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},
	"internal/registry":       {layer: 10, name: "registry", sameLayer: true, deny: []string{"internal/service", "internal/middleware", "internal/gateway", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},
	"internal/sessionruntime": {layer: 10, name: "sessionruntime", sameLayer: true, deny: []string{"internal/service", "internal/middleware", "internal/gateway", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},
	"internal/service":        {layer: 10, name: "service", deny: []string{"internal/store", "internal/dao", "internal/runtimeconn", "internal/registry", "internal/sessionruntime", "internal/middleware", "internal/gateway", "internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},

	// ——— 11 层：HTTP 中间件 / MCP 网关 ———
	"internal/middleware": {layer: 11, name: "middleware", deny: []string{"internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},
	"internal/gateway":    {layer: 11, name: "gateway", deny: []string{"internal/controller", "internal/router", "internal/assembly", "internal/app", "internal/cli"}},

	// ——— 12 层：入站适配器 ———
	"internal/controller": {layer: 12, name: "controller", deny: []string{"internal/store", "internal/dao", "internal/model", "internal/domain", "internal/mechanism", "internal/runtimeconn", "internal/registry", "internal/sessionruntime", "internal/router", "internal/assembly", "internal/app", "internal/cli", "internal/gateway"}},

	// ——— 13 层：gin engine + 路由表 ———
	"internal/router": {layer: 13, name: "router", deny: []string{"internal/service", "internal/store", "internal/dao", "internal/model", "internal/domain", "internal/dto", "internal/eventbus", "internal/runtimeconn", "internal/registry", "internal/sessionruntime", "internal/mechanism", "internal/assembly", "internal/app", "internal/cli"}},

	// ——— 14 层：唯一装配根 ———
	"internal/assembly": {layer: 14, name: "assembly", deny: []string{"internal/app", "internal/cli"}},

	// ——— 15 层：应用壳 ———
	"internal/app": {layer: 15, name: "app", deny: []string{"internal/service", "internal/store", "internal/dao", "internal/model", "internal/domain", "internal/controller", "internal/mechanism", "internal/runtimeconn", "internal/registry", "internal/sessionruntime", "internal/cli", "internal/gateway"}},

	// ——— 16 层：命令实现（cliops）———
	//
	// 设计的层表只有 `cli`，但命令实现（doctor 43KB 为主）必须用到 store/domain/mechanism；
	// 把实现留在 `cli` 就与"命令树不得 import 应用内核"冲突，搬进 `assembly` 又违背"装配根只装对象"。
	// 故命令实现单独成层（见 `.dsh/docs/plans/2026-10-19-backend-v3.1-execution-plan.md`）。
	"internal/cliops": {layer: 16, name: "cliops", sameLayer: true, deny: []string{"internal/cli"}},

	// ——— 17 层：命令树（纯分发）———
	"internal/cli": {layer: 17, name: "cli", sameLayer: true, deny: []string{"internal/service", "internal/store", "internal/dao", "internal/model", "internal/domain", "internal/controller", "internal/mechanism", "internal/runtimeconn", "internal/registry", "internal/sessionruntime", "internal/router", "internal/gateway"}},
}

// v31Exempt 不参与层表映射的包（§3.2.2 注）。
var v31Exempt = []string{
	"internal/testutil",
	"internal/archtest",
}

// v31LegacyPaths 过渡期路径映射：P2 之前 `pkg/*` 仍在，按目标层判定。
// P2 完成后本表必须为空（`TestLayerLegacyPathsAreEmptyV31` 会盯着）。
var v31LegacyPaths = map[string]string{
	"pkg/runtime":     "internal/runtime",
	"pkg/runtime/dsh": "internal/infra/dsh",
	"pkg/acl":         "internal/infra/acp",
	"pkg/mcp":         "internal/infra/mcp",
	"pkg/logging":     "internal/infra/logging",
	"pkg/credentials": "internal/infra/credentials",
	"pkg/dbclient":    "internal/infra/dbclient",
	"pkg/procreg":     "internal/infra/procreg",
	"pkg/identity":    "internal/identity",
	"pkg/resource":    "internal/resource",
	"pkg/redact":      "internal/redact",
	"pkg/object":      "internal/object",
	"pkg/capability":  "internal/capability",
	"pkg/automation":  "internal/automation",
	"pkg/config":      "internal/config",
	"pkg/code":        "internal/code",
}

// v31Exception 是一条已登记的过渡例外。
type v31Exception struct {
	// from 违规方的包前缀（相对 backend/，如 internal/controller/ctrsession）。
	from string
	// to 被 import 的包前缀（如 internal/store）。
	to string
	// budget 当前允许的命中次数上限（P0 用生产文件统计；只许减少）。
	budget int
	// phase 计划清零的阶段（P1/P4/P5/P6）。
	phase string
	// why 为什么现在还不能清零。
	why string
}

// v31Exceptions 过渡期例外清单。**只许减少**：新增边一律判红（blocking 时）。
//
// 这份清单是 v3.1 §3.2.3 的「当前例外」一列的可执行形态；每阶段的收口就是把它逐条删掉。
var v31Exceptions []v31Exception

// v31PkgInfo 扫描到的一个包。
type v31PkgInfo struct {
	dir     string // 绝对目录
	rel     string // 相对 backend/ 的斜杠路径（如 internal/store）
	imports []string
	target  string // 层表 key（legacy 已归一）
	layer   v31Layer
	ok      bool
}

// v31Resolve 把 import 路径或目录相对路径归一成层表 key。
//
// 匹配**最长**前缀（`internal/cliops/doctor` → `internal/cliops`，而不是 `internal/cli`）；
// 表是 map，遍历顺序不定，所以必须显式取最长而不是"先命中先返回"。
func v31Resolve(rel string) (string, v31Layer, bool) {
	bestKey, bestLayer, bestLen := "", v31Layer{}, -1
	consider := func(key string, layer v31Layer) {
		if rel == key || strings.HasPrefix(rel, key+"/") {
			if len(key) > bestLen {
				bestKey, bestLayer, bestLen = key, layer, len(key)
			}
		}
	}
	for key, layer := range v31Layers {
		consider(key, layer)
	}
	for legacy, target := range v31LegacyPaths {
		if layer, ok := v31Layers[target]; ok {
			consider(legacy, layer)
			// legacy 命中时返回目标 key（层表以目标为准）。
			if rel == legacy || strings.HasPrefix(rel, legacy+"/") {
				if len(legacy) >= bestLen {
					return target, layer, true
				}
			}
		}
	}
	if bestLen < 0 {
		return "", v31Layer{}, false
	}
	return bestKey, bestLayer, true
}

// v31CollectPackages 扫描 backend/internal 与 backend/pkg 下的全部生产包（跳过 _test.go）。
func v31CollectPackages(t *testing.T) []v31PkgInfo {
	t.Helper()
	root := backendRoot(t)
	var out []v31PkgInfo
	for _, top := range []string{"internal", "pkg"} {
		base := filepath.Join(root, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		for _, dir := range goPackagesUnder(t, base) {
			relPath := filepath.ToSlash(rel(root, dir))
			pkg, err := build.ImportDir(dir, 0)
			if err != nil {
				t.Fatalf("解析包 %s 失败: %v", relPath, err)
			}
			// 只看生产 import：build.ImportDir 的 pkg.Imports 不含 _test.go。
			out = append(out, v31PkgInfo{dir: dir, rel: relPath, imports: pkg.Imports})
		}
	}
	if len(out) == 0 {
		t.Fatalf("层表守卫失效：internal/ 与 pkg/ 下扫到 0 个包")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	for i := range out {
		out[i].target, out[i].layer, out[i].ok = v31Resolve(out[i].rel)
	}
	return out
}

// v31ImportPath 把 import 字符串去掉 module 前缀（非本仓 import 返回空）。
func v31ImportPath(imp string) string {
	if !strings.HasPrefix(imp, modulePrefix) {
		return ""
	}
	return strings.TrimPrefix(imp, modulePrefix)
}

// v31IsExempt 判断相对路径是否落在豁免面（含其子包）。
func v31IsExempt(relPath string) bool {
	for _, e := range v31Exempt {
		if relPath == e || strings.HasPrefix(relPath, e+"/") {
			return true
		}
	}
	return false
}

// v31Violation 一条层表违规。
type v31Violation struct {
	from     string
	fromName string
	to       string
	toName   string
	upward   bool
}

func (v v31Violation) String() string {
	dir := "向下"
	if v.upward {
		dir = "向上"
	}
	return fmt.Sprintf("%s（%s 层）→ %s（%s 层，%s）", v.from, v.fromName, v.to, v.toName, dir)
}

// collectV31Violations 计算当前的全部层表违规。
func collectV31Violations(t *testing.T) ([]v31Violation, []v31PkgInfo) {
	t.Helper()
	pkgs := v31CollectPackages(t)
	var violations []v31Violation
	for _, p := range pkgs {
		if v31IsExempt(p.rel) {
			continue
		}
		if !p.ok {
			violations = append(violations, v31Violation{
				from: p.rel, fromName: "未知", to: "（层表未覆盖）", toName: "?",
			})
			continue
		}
		for _, imp := range p.imports {
			relImp := v31ImportPath(imp)
			if relImp == "" || v31IsExempt(relImp) {
				continue
			}
			targetKey, targetLayer, ok := v31Resolve(relImp)
			if !ok {
				// 同模块内但层表没覆盖（例如 cmd 之外的 future 包）→ 视为违规，逼层表补全。
				violations = append(violations, v31Violation{
					from: p.rel, fromName: p.layer.name,
					to: relImp, toName: "（层表未覆盖）",
				})
				continue
			}
			// 显式 deny 优先。
			denied := false
			for _, d := range p.layer.deny {
				if relImp == d || strings.HasPrefix(relImp, d+"/") {
					denied = true
					break
				}
			}
			if denied {
				violations = append(violations, v31Violation{
					from: p.rel, fromName: p.layer.name, to: targetKey, toName: targetLayer.name,
				})
				continue
			}
			if targetLayer.layer > p.layer.layer {
				violations = append(violations, v31Violation{
					from: p.rel, fromName: p.layer.name, to: targetKey, toName: targetLayer.name, upward: true,
				})
				continue
			}
			if targetLayer.layer == p.layer.layer && targetKey != p.target && !p.layer.sameLayer {
				violations = append(violations, v31Violation{
					from: p.rel, fromName: p.layer.name, to: targetKey, toName: targetLayer.name,
				})
			}
		}
	}
	return violations, pkgs
}

// TestLayerTableCoversAllPackagesV31 fail-closed：internal/ 下每个包都必须落在层表里。
func TestLayerTableCoversAllPackagesV31(t *testing.T) {
	pkgs := v31CollectPackages(t)
	for _, p := range pkgs {
		if v31IsExempt(p.rel) {
			continue
		}
		if !p.ok {
			t.Errorf("层表未覆盖：%s（请把它映射到 internal/archtest 层表的一层）", p.rel)
		}
	}
}

// TestLayerTableIsNotEmptyV31 fail-closed：扫到 0 个包即失败。
func TestLayerTableIsNotEmptyV31(t *testing.T) {
	pkgs := v31CollectPackages(t)
	if len(pkgs) == 0 {
		t.Fatalf("层表守卫失效：扫到 0 个包")
	}
}

// TestLayerLegacyPathsAreEmptyV31 P2 结束后 `pkg/` 必须不存在。
func TestLayerLegacyPathsAreEmptyV31(t *testing.T) {
	root := backendRoot(t)
	if _, err := os.Stat(filepath.Join(root, "pkg")); err == nil {
		if v31LayerGuardBlocking {
			t.Fatalf("P2 已收口：backend/pkg 必须不存在（仍有目录）")
		}
		t.Logf("P0–P1 报告：backend/pkg 仍存在（P2 合并后删除）")
	}
}

// TestLayerImportsV31 按层表检查生产 import。
func TestLayerImportsV31(t *testing.T) {
	violations, _ := collectV31Violations(t)
	if len(violations) == 0 {
		return
	}

	// 例外预算：同一条 (from,to) 边只允许登记过的次数。
	budget := map[string]int{}
	for _, e := range v31Exceptions {
		budget[e.from+"→"+e.to] = e.budget
	}
	used := map[string]int{}
	var unexpected []v31Violation
	for _, v := range violations {
		key := v.from + "→" + v.to
		used[key]++
		if used[key] > budget[key] {
			unexpected = append(unexpected, v)
		}
	}
	// 双向判据：例外降到 0 的条目必须删掉，防止清单腐烂。
	for _, e := range v31Exceptions {
		key := e.from + "→" + e.to
		if used[key] == 0 {
			if v31LayerGuardBlocking {
				t.Errorf("过期例外：%s → %s 已无命中，请从 v31Exceptions 删除（双向判据）", e.from, e.to)
			} else {
				t.Logf("P0 报告：过期例外 %s → %s（计划 %s 清零）", e.from, e.to, e.phase)
			}
		}
	}

	if !v31LayerGuardBlocking {
		byEdge := map[string]int{}
		for _, v := range unexpected {
			byEdge[v.from+" → "+v.to]++
		}
		keys := make([]string, 0, len(byEdge))
		for k := range byEdge {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Logf("P0 报告（未登记）：%s × %d", k, byEdge[k])
		}
		t.Logf("P0 报告：层表违规 %d 条（层表守卫当前 report-only；P6 起 blocking）", len(violations))
		return
	}

	for _, v := range unexpected {
		t.Errorf("层表违规：%s", v)
	}
}

// TestRuntimeDoesNotImportRuntimeAdaptersV31 `internal/runtime` 不得 import 任何 runtime adapter。
func TestRuntimeDoesNotImportRuntimeAdaptersV31(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "runtime")
	if !hasGoFiles(dir) {
		if v31LayerGuardBlocking {
			t.Fatalf("internal/runtime 不存在（P2 尚未完成？）")
		}
		return
	}
	for _, imp := range importedByPackage(t, dir) {
		if strings.HasPrefix(imp, modulePrefix+"internal/infra/dsh") {
			t.Errorf("adapter 边界：internal/runtime 不得 import %s", imp)
		}
	}
}

// TestOnlyAssemblyImportsRuntimeAdaptersV31 只有 internal/assembly 可以 import runtime adapter。
func TestOnlyAssemblyImportsRuntimeAdaptersV31(t *testing.T) {
	root := backendRoot(t)
	pkgs := goPackagesUnder(t, filepath.Join(root, "internal"))
	var hits []string
	for _, dir := range pkgs {
		relPath := filepath.ToSlash(rel(root, dir))
		if relPath == "internal/assembly" || v31IsExempt(relPath) {
			continue
		}
		for _, imp := range importedByPackage(t, dir) {
			if strings.HasPrefix(imp, modulePrefix+"internal/infra/dsh") {
				hits = append(hits, relPath+" → "+imp)
			}
		}
	}
	if len(pkgs) == 0 {
		t.Fatalf("层表守卫失效：internal/ 下扫到 0 个包")
	}
	for _, h := range hits {
		if v31LayerGuardBlocking {
			t.Errorf("adapter 装配边：只有 internal/assembly 可以 import infra/dsh；违规 %s", h)
		} else {
			t.Logf("P0 报告（adapter 装配边）：%s", h)
		}
	}
}

// TestAdapterStaysOutsideAppCoreV31 adapter 不得 import 应用内核。
func TestAdapterStaysOutsideAppCoreV31(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "infra", "dsh")
	if !hasGoFiles(dir) {
		return // P2 之前不存在
	}
	forbidden := []string{
		"internal/app", "internal/cli", "internal/assembly", "internal/router", "internal/controller",
		"internal/service", "internal/sessionruntime", "internal/registry", "internal/store",
		"internal/dao", "internal/model", "internal/domain", "internal/mechanism", "internal/gateway",
		"internal/runtimeconn",
	}
	for _, imp := range importedByPackage(t, dir) {
		relImp := v31ImportPath(imp)
		for _, bad := range forbidden {
			if relImp == bad || strings.HasPrefix(relImp, bad+"/") {
				t.Errorf("adapter 边界：internal/infra/dsh 不得 import %s", imp)
			}
		}
	}
}
