package archtest

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// ——— 后端六边形架构 v3.2 的层表守卫 ———
//
// 口径见 `.dsh/docs/plans/2026-10-19-hexagonal-alignment-plan.md`（终局版）。
//
//	L0  model                                                   表行形状（零内部依赖）
//	L1  object redact identity resource version credential
//	    eventbus code automation runtime capability
//	    infra/acp infra/mcp infra/logging                        值对象 / 协议库
//	L2  domain/*                                                领域
//	L3  dto/*                                                   应用契约
//	L4  service/svc*                                            用例
//	L5  store dao infra/dsh infra/dbclient infra/credentials
//	    infra/procreg                                           基础驱动
//	L6  gateway mechanism registry runtimeconn config
//	    maintenance                                             组合驱动
//	L7  sessionruntime                                          执行面
//	L8  controller middleware                                   入站适配
//	L9  router assembly app cliops cli                          交付 / 装配
//
// 相对 v3.1 表（`layers_v31_test.go`）只改了两件事：
//
//  1. **方向反过来**。v3.1 把 `domain` 放在第 9 层、`store` 第 7 层、`dto`/`dao` 第 6 层、
//     `model` 第 1 层，规则是"高层可以 import 低层"——于是 `domain → store/dto/mechanism`
//     全都被这张表定义成**合法**。v3.2 把 `domain` 压到第 2 层，把持久化与机制压到 5/6 层，
//     `domain` 的出边只剩 L0/L1。这不是代码写歪了，是 v3.1 的层号本来就把方向定反了。
//  2. **`dto` 从第 6 层挪到第 3 层**（domain 外侧、service 内侧）。它是应用契约：驱动侧
//     （执行面、mechanism、controller）用它是六边形允许的外→内依赖；领域侧用它才是违规。
//
// 三条规则与 v3.1 一致：只能向内（被 import 的层号不得增大）；同带默认不互链，除非该层显式
// `sameLayer`；每层可带 `deny` 精确排除"方向允许但不该做"的边（如 `cliops ↛ store`）。
//
// 失效方式与 `TestFrontendUiScanRootsCoverAllUi` 同形：层表搬走/改名后判据会**静默空转**
// （照样绿，只是不再检查任何东西），所以本文件同时 fail-closed：
//   - 扫到 0 个包 → 失败；
//   - 某个包映射不到任何层 → 失败；
//   - 例外预算被新边吃掉 → 失败；
//   - 例外降到 0 时条目未删除 → 失败（双向判据，防清单腐烂）。
//
// 与 v3.1 守卫的关系：两张表**同时生效**。v3.2 的收口过程只会删边、不会新增 v3.1 方向的边，
// 所以 v3.1 守卫在迁移期间应始终为绿；一旦它转红，说明这一刀把某条边挪到了 v3.1 的更高层。
// 迁移完成后（最后一刀）删除 `layers_v31_test.go`。
const v32LayerGuardBlocking = true

// v32Layer 是 v3.2 层表的一行。
type v32Layer struct {
	// layer 层号（0 = 最内层）。
	layer int
	// sameLayer 是否允许同层互链（默认只允许 import 严格更内层）。
	sameLayer bool
	// deny 显式禁止的 import 前缀（覆盖"更内层即可 import"的默认规则）。
	deny []string
	// name 人类可读的层名（错误文案用）。
	name string
}

// v32Layers 是终局层表。key 是目标路径前缀（`internal/...`），按**最长前缀**匹配。
var v32Layers = map[string]v32Layer{
	// ——— L0：表行形状 ———
	//
	// 不需要 deny：它在最内层，`model → 任何 internal/*` 都必然是"向外"，天然违规。
	"internal/model": {layer: 0, name: "model"},

	// ——— L1：值对象 / 协议库 ———
	//
	// 本带允许同层互链：`code` 要把 ACP 协议错误折成本仓错误码，`runtime` 要用
	// `identity`/`resource` 的值类型，而 ACP 是各底座**共用的线协议**（不是可替换的外部
	// 服务，见 plan 的 ADR-1）——把它们拆开只会得到一堆复制粘贴。
	"internal/object":     {layer: 1, name: "object", sameLayer: true},
	"internal/redact":     {layer: 1, name: "redact", sameLayer: true},
	"internal/identity":   {layer: 1, name: "identity", sameLayer: true},
	"internal/resource":   {layer: 1, name: "resource", sameLayer: true},
	"internal/version":    {layer: 1, name: "version", sameLayer: true},
	"internal/credential": {layer: 1, name: "credential", sameLayer: true},
	"internal/eventbus":   {layer: 1, name: "eventbus", sameLayer: true},
	"internal/code":       {layer: 1, name: "code", sameLayer: true},
	"internal/automation": {layer: 1, name: "automation", sameLayer: true},
	// 能力定义的**值层规则**（id 合法性、patch 键语义、参数类错误出口）：从 `mechanism/definition`
	// 抽出来，让 `domain/*` 不必反向 import 机制层。
	"internal/definition":    {layer: 1, name: "definition", sameLayer: true},
	"internal/runtime":       {layer: 1, name: "runtime", sameLayer: true, deny: []string{"internal/infra/dsh"}},
	"internal/capability":    {layer: 1, name: "capability", sameLayer: true},
	"internal/infra/acp":     {layer: 1, name: "infra/acp", sameLayer: true},
	"internal/infra/mcp":     {layer: 1, name: "infra/mcp", sameLayer: true},
	"internal/infra/logging": {layer: 1, name: "infra/logging", sameLayer: true},

	// ——— L2：领域 ———
	//
	// 出边只允许落在 L0/L1。子域之间允许互链：它们不是独立的 bounded context，
	// 而是同一内核的分区（`session` 用 `identity` 的身份值）。
	"internal/domain": {layer: 2, name: "domain", sameLayer: true, deny: []string{
		"internal/store", "internal/dao", "internal/dto", "internal/mechanism",
		"internal/infra/credentials", "internal/infra/dsh", "internal/registry",
		"internal/sessionruntime", "internal/runtimeconn", "internal/gateway",
		"internal/controller", "internal/middleware", "internal/router", "internal/assembly",
		"internal/app", "internal/cliops", "internal/cli",
	}},

	// ——— L3：应用契约（dto）———
	//
	// 只许向内（L0/L1/L2 都不该被 dto 依赖，实际上它只用标准库 + 少量值类型）。
	"internal/dto": {layer: 3, name: "dto", sameLayer: true},

	// ——— L4：用例 ———
	//
	// **永不 sameLayer**：这是"svc 包之间零互引"（三条硬规矩第 2 条）的载体。
	// deny `internal/service` 同时挡住兄弟 svc 与自身（自 import 不会出现在图里）。
	"internal/service": {layer: 4, name: "service", deny: []string{
		"internal/service", "internal/store", "internal/dao", "internal/infra/dsh",
		"internal/mechanism", "internal/registry", "internal/runtimeconn",
		"internal/sessionruntime", "internal/gateway", "internal/controller",
		"internal/middleware", "internal/router", "internal/assembly", "internal/app",
		"internal/cliops", "internal/cli",
	}},

	// ——— L5：基础驱动 ———
	"internal/dao": {layer: 5, name: "dao", sameLayer: true, deny: []string{
		"internal/store", "internal/mechanism", "internal/registry", "internal/runtimeconn",
		"internal/sessionruntime", "internal/gateway", "internal/controller",
		"internal/router", "internal/assembly", "internal/app", "internal/cliops", "internal/cli",
	}},
	"internal/store": {layer: 5, name: "store", sameLayer: true, deny: []string{
		"internal/mechanism", "internal/registry", "internal/runtimeconn",
		"internal/sessionruntime", "internal/gateway", "internal/controller",
		"internal/router", "internal/assembly", "internal/app", "internal/cliops", "internal/cli",
	}},
	"internal/infra/dsh":         {layer: 5, name: "infra/dsh", sameLayer: true, deny: v32AdapterDeny},
	"internal/infra/dbclient":    {layer: 5, name: "infra/dbclient", sameLayer: true},
	"internal/infra/credentials": {layer: 5, name: "infra/credentials", sameLayer: true},
	"internal/infra/procreg":     {layer: 5, name: "infra/procreg", sameLayer: true},

	// ——— L6：组合驱动 ———
	"internal/gateway": {layer: 6, name: "gateway", sameLayer: true, deny: []string{
		"internal/controller", "internal/middleware", "internal/router",
		"internal/assembly", "internal/app", "internal/cliops", "internal/cli",
	}},
	"internal/mechanism": {layer: 6, name: "mechanism", sameLayer: true, deny: []string{
		"internal/registry", "internal/runtimeconn", "internal/sessionruntime",
		"internal/gateway", "internal/controller", "internal/middleware", "internal/router",
		"internal/assembly", "internal/app", "internal/cliops", "internal/cli",
	}},
	"internal/registry": {layer: 6, name: "registry", sameLayer: true, deny: []string{
		"internal/sessionruntime", "internal/gateway", "internal/controller",
		"internal/router", "internal/assembly", "internal/app", "internal/cliops", "internal/cli",
	}},
	"internal/runtimeconn": {layer: 6, name: "runtimeconn", sameLayer: true, deny: []string{
		"internal/sessionruntime", "internal/gateway", "internal/controller",
		"internal/router", "internal/assembly", "internal/app", "internal/cliops", "internal/cli",
	}},
	"internal/config": {layer: 6, name: "config", sameLayer: true, deny: []string{
		"internal/controller", "internal/router", "internal/assembly", "internal/app",
		"internal/cliops", "internal/cli",
	}},
	// 刀 10 引入：migrate / doctor / transcripts / mcp reap 的落点。
	"internal/maintenance": {layer: 6, name: "maintenance", sameLayer: true},

	// ——— L7：执行面 ———
	"internal/sessionruntime": {layer: 7, name: "sessionruntime", deny: []string{
		"internal/gateway", "internal/controller", "internal/middleware", "internal/router",
		"internal/assembly", "internal/app", "internal/cliops", "internal/cli",
	}},

	// ——— L8：入站适配 ———
	"internal/controller": {layer: 8, name: "controller", deny: []string{
		"internal/model", "internal/domain", "internal/store", "internal/dao",
		"internal/mechanism", "internal/registry", "internal/runtimeconn",
		"internal/sessionruntime", "internal/gateway", "internal/router",
		"internal/assembly", "internal/app", "internal/cliops", "internal/cli",
	}},
	"internal/middleware": {layer: 8, name: "middleware", deny: []string{
		"internal/controller", "internal/router", "internal/assembly", "internal/app",
		"internal/cliops", "internal/cli",
	}},

	// ——— L9：交付 / 装配 ———
	"internal/router": {layer: 9, name: "router", sameLayer: true, deny: []string{
		"internal/service", "internal/store", "internal/dao", "internal/model",
		"internal/domain", "internal/dto", "internal/eventbus", "internal/mechanism",
		"internal/registry", "internal/runtimeconn", "internal/sessionruntime",
		"internal/gateway", "internal/assembly", "internal/app", "internal/cliops", "internal/cli",
	}},
	"internal/assembly": {layer: 9, name: "assembly", sameLayer: true, deny: []string{
		"internal/app", "internal/cli",
	}},
	"internal/app": {layer: 9, name: "app", sameLayer: true, deny: []string{
		"internal/service", "internal/store", "internal/dao", "internal/model",
		"internal/domain", "internal/mechanism", "internal/registry", "internal/runtimeconn",
		"internal/sessionruntime", "internal/controller", "internal/router",
		"internal/cliops", "internal/cli", "internal/gateway",
	}},
	// cliops 是**第二入站适配器**，不是第二装配根：它只做参数绑定 + 调用例 + 打印。
	// 持久化与机制一律经 `assembly.BuildResult` 暴露的 `Maintenance` 端口，拿不到具体实现。
	"internal/cliops": {layer: 9, name: "cliops", sameLayer: true, deny: []string{
		"internal/store", "internal/dao", "internal/model", "internal/mechanism",
		"internal/domain", "internal/registry", "internal/runtimeconn",
		"internal/sessionruntime", "internal/gateway", "internal/infra",
	}},
	"internal/cli": {layer: 9, name: "cli", sameLayer: true, deny: []string{
		"internal/service", "internal/store", "internal/dao", "internal/model",
		"internal/domain", "internal/controller", "internal/mechanism",
		"internal/runtimeconn", "internal/registry", "internal/sessionruntime",
		"internal/router", "internal/gateway",
	}},
}

// v32AdapterDeny adapter（`infra/dsh`）不得认识应用内核的任何一层。
var v32AdapterDeny = []string{
	"internal/model", "internal/eventbus", "internal/capability", "internal/credential",
	"internal/code", "internal/automation", "internal/config", "internal/dto",
	"internal/dao", "internal/store", "internal/mechanism", "internal/domain",
	"internal/runtimeconn", "internal/registry", "internal/service",
	"internal/sessionruntime", "internal/middleware", "internal/gateway",
	"internal/controller", "internal/router", "internal/assembly", "internal/app",
	"internal/cliops", "internal/cli",
}

// v32Resolve 把相对路径归一成层表 key（最长前缀优先）。
//
// 表是 map，遍历顺序不定，所以必须显式取最长前缀，而不是"先命中先返回"
// （`internal/cliops` 必须命中 cliops 层，不能被 `internal/cli` 抢走）。
func v32Resolve(rel string) (string, v32Layer, bool) {
	bestKey, bestLayer, bestLen := "", v32Layer{}, -1
	for key, layer := range v32Layers {
		if rel == key || strings.HasPrefix(rel, key+"/") {
			if len(key) > bestLen {
				bestKey, bestLayer, bestLen = key, layer, len(key)
			}
		}
	}
	if bestLen < 0 {
		return "", v32Layer{}, false
	}
	return bestKey, bestLayer, true
}

// v32Violation 一条 v3.2 层表违规。
type v32Violation struct {
	from   string // 违规方包（相对 backend/）
	fromLn int
	to     string // 被 import 的包（相对 backend/）
	toLn   int
	kind   string // "向上" / "同层" / "deny"
}

func (v v32Violation) String() string {
	return fmt.Sprintf("%s（L%d）→ %s（L%d，%s）", v.from, v.fromLn, v.to, v.toLn, v.kind)
}

// collectV32Violations 计算当前全部 v3.2 违规（只看生产 import）。
func collectV32Violations(t *testing.T) []v32Violation {
	t.Helper()
	pkgs := v31CollectPackages(t) // 复用 v31 的扫描器（它只读生产 import）
	var out []v32Violation
	for _, p := range pkgs {
		if v31IsExempt(p.rel) {
			continue
		}
		fromKey, fromLayer, ok := v32Resolve(p.rel)
		if !ok {
			out = append(out, v32Violation{from: p.rel, fromLn: -1, to: "（层表未覆盖）", toLn: -1, kind: "未映射"})
			continue
		}
		for _, imp := range p.imports {
			relImp := v31ImportPath(imp)
			if relImp == "" || v31IsExempt(relImp) {
				continue
			}
			toKey, toLayer, ok := v32Resolve(relImp)
			if !ok {
				out = append(out, v32Violation{
					from: p.rel, fromLn: fromLayer.layer,
					to: relImp, toLn: -1, kind: "目标未映射",
				})
				continue
			}
			// deny 优先：方向允许但它不该做。
			denied := false
			for _, d := range fromLayer.deny {
				if relImp == d || strings.HasPrefix(relImp, d+"/") {
					denied = true
					break
				}
			}
			if denied {
				out = append(out, v32Violation{
					from: p.rel, fromLn: fromLayer.layer,
					to: toKey, toLn: toLayer.layer, kind: "deny",
				})
				continue
			}
			if toLayer.layer > fromLayer.layer {
				out = append(out, v32Violation{
					from: p.rel, fromLn: fromLayer.layer,
					to: toKey, toLn: toLayer.layer, kind: "向上",
				})
				continue
			}
			if toLayer.layer == fromLayer.layer && toKey != fromKey && !fromLayer.sameLayer {
				out = append(out, v32Violation{
					from: p.rel, fromLn: fromLayer.layer,
					to: toKey, toLn: toLayer.layer, kind: "同层",
				})
			}
		}
	}
	return out
}

// TestV32ReportOnly 打印当前全部违规（**永远通过**）。
//
// 它存在的理由：迁移期间需要一份随时可生成的"还剩哪些边"清单，
// `-run TestV32ReportOnly -v` 的输出就是收口进度。
func TestV32ReportOnly(t *testing.T) {
	violations := collectV32Violations(t)
	byEdge := map[string]int{}
	for _, v := range violations {
		byEdge[fmt.Sprintf("%s → %s（%s）", v.from, v.to, v.kind)]++
	}
	keys := make([]string, 0, len(byEdge))
	for k := range byEdge {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("v3.2 违规：%s × %d", k, byEdge[k])
	}
	t.Logf("v3.2 违规合计 %d 条", len(violations))
}

// v32Exception 一条已登记的迁移例外。
type v32Exception struct {
	// from 违规方的包（相对 backend/，精确到包，不是前缀——这样一条边只对应一处）。
	from string
	// to 被 import 的包（层表 key）。
	to string
	// budget 当前允许的命中次数上限（= 生产 import 语句数）。只许减少。
	budget int
	// slice 计划清零的那一刀（见 plan §4）。
	slice string
	// why 为什么现在还不能清零。
	why string
}

// v32Exceptions 迁移例外清单。**只许减少**。
//
// 这份清单就是 plan §4 的任务板：每一刀的目标是把对应条目删成 0。
// 新增边一律判红；降到 0 未删除也判红（双向判据，防清单腐烂）。
var v32Exceptions = []v32Exception{
	// ——— 刀 4：domain/skill ———

	// ——— 刀 5：domain/mcp ———
	{from: "internal/domain/mcp", to: "internal/dto", budget: 1, slice: "刀5", why: "返回值是 dto/dtocapability"},

	// ——— 刀 6：domain/workspace ———
	{from: "internal/domain/workspace", to: "internal/dto", budget: 1, slice: "刀6", why: "返回值是 dto/dtoworkspace"},
	{from: "internal/domain/workspace", to: "internal/store", budget: 1, slice: "刀6", why: "直接持 *store.Store，未经窄端口"},

	// ——— 刀 7：domain/automation ———
	{from: "internal/domain/automation", to: "internal/dto", budget: 1, slice: "刀7", why: "返回值是 dto/dtoautomation"},
	{from: "internal/domain/automation", to: "internal/mechanism", budget: 1, slice: "刀7", why: "用 mechanism/definition"},
	{from: "internal/domain/automation", to: "internal/store", budget: 1, slice: "刀7", why: "直接持 *store.Store，未经窄端口"},

	// ——— 刀 8：domain/provider ———
	{from: "internal/domain/provider", to: "internal/dto", budget: 2, slice: "刀8", why: "返回值是 dtomodel 与 dtoprovider"},
	{from: "internal/domain/provider", to: "internal/infra/credentials", budget: 1, slice: "刀8", why: "直接读凭据文件，应经 mechanism/credential"},
	{from: "internal/domain/provider", to: "internal/mechanism", budget: 2, slice: "刀8", why: "用 definition 与 credential 两个机制"},
	{from: "internal/domain/provider", to: "internal/store", budget: 1, slice: "刀8", why: "直接持 *store.Store，未经窄端口"},

	// ——— 刀 9：domain/session（最难，最后做）———
	{from: "internal/domain/session", to: "internal/dto", budget: 4, slice: "刀9", why: "返回值是 dtolibrary/dtosearch/dtosession/dtousage"},
	{from: "internal/domain/session", to: "internal/mechanism", budget: 1, slice: "刀9", why: "用 mechanism/doctor"},

	// ——— 刀 10：service 收口 + cliops 拆分 ———
	{from: "internal/service/svcmodel", to: "internal/mechanism", budget: 1, slice: "刀10", why: "用 mechanism/definition"},
	{from: "internal/service/svcprovider", to: "internal/infra/credentials", budget: 1, slice: "刀10", why: "直接读凭据文件"},

	{from: "internal/cliops", to: "internal/domain", budget: 3, slice: "刀10", why: "命令实现里有业务判断，应下沉到 svc"},
	{from: "internal/cliops", to: "internal/gateway", budget: 1, slice: "刀10", why: "命令直接开网关"},
	{from: "internal/cliops", to: "internal/infra/mcp", budget: 1, slice: "刀10", why: "mcp reap 运维动作，应搬 maintenance"},
	{from: "internal/cliops", to: "internal/mechanism", budget: 3, slice: "刀10", why: "doctor 直接调机制，应搬 maintenance"},
	{from: "internal/cliops", to: "internal/model", budget: 1, slice: "刀10", why: "读模型里直接拼表行"},
	{from: "internal/cliops", to: "internal/registry", budget: 1, slice: "刀10", why: "命令直接操作执行平面"},
	{from: "internal/cliops", to: "internal/sessionruntime", budget: 1, slice: "刀10", why: "命令直接开会话编排"},
	{from: "internal/cliops", to: "internal/store", budget: 1, slice: "刀10", why: "命令直接持有 *store.Store（第二装配根）"},
}

// v32GinAllowlist gin 围栏的白名单。刀 2 已清空——`assembly` 交 `http.Handler`，
// `app` 不再知道 Web 框架；只剩 router/controller/middleware 三个入站适配层可以 import gin。
// 若将来又要临时开口，在这里登记并要求双向判据（清零即删）。
var v32GinAllowlist []v32Exception

// v32GinPath gin 的 import 路径。
const v32GinPath = "github.com/gin-gonic/gin"

// TestLayerTableCoversAllPackagesV32 fail-closed：每个包都必须落在 v3.2 层表里。
//
// 与"扫到 0 个包"合并成一条：两者失效方式相同（判据静默空转），分开写只会多一处漏配。
func TestLayerTableCoversAllPackagesV32(t *testing.T) {
	pkgs := v31CollectPackages(t)
	if len(pkgs) == 0 {
		t.Fatalf("v3.2 层表守卫失效：扫到 0 个包")
	}
	for _, p := range pkgs {
		if v31IsExempt(p.rel) {
			continue
		}
		if _, _, ok := v32Resolve(p.rel); !ok {
			t.Errorf("v3.2 层表未覆盖：%s（请把它映射到 layers_v32_test.go 的某一层）", p.rel)
		}
	}
}

// TestLayerImportsV32 按 v3.2 层表检查生产 import。
func TestLayerImportsV32(t *testing.T) {
	violations := collectV32Violations(t)

	budget := map[string]int{}
	for _, e := range v32Exceptions {
		budget[e.from+"→"+e.to] = e.budget
	}
	used := map[string]int{}
	var unexpected []v32Violation
	for _, v := range violations {
		key := v.from + "→" + v.to
		used[key]++
		if used[key] > budget[key] {
			unexpected = append(unexpected, v)
		}
	}

	// 双向判据：例外降到 0 的条目必须删掉，防止清单腐烂。
	for _, e := range v32Exceptions {
		if used[e.from+"→"+e.to] == 0 {
			t.Errorf("过期例外：%s → %s 已无命中，请从 v32Exceptions 删除（双向判据；计划 %s 清零）", e.from, e.to, e.slice)
		}
	}

	if !v32LayerGuardBlocking {
		t.Logf("v3.2 层表守卫 report-only：违规 %d 条，未登记 %d 条", len(violations), len(unexpected))
		return
	}
	for _, v := range unexpected {
		t.Errorf("v3.2 层表违规：%s", v)
	}
}

// TestGinConfinedToInboundAdaptersV32 gin 只允许出现在入站适配器。
//
// 判据来自 plan §3.5：`assembly` 知道 gin 是"装配根可以用具体适配器"（可接受但不必要），
// `app` 知道 gin 是真正的味道——进程壳不该知道 Web 框架。两者都在刀 2 收口，先用白名单
// 把现状钉住，防止在迁移期间扩散到别处。
func TestGinConfinedToInboundAdaptersV32(t *testing.T) {
	// 入站适配层：gin 的合法落点是这三个带。
	allowed := map[string]bool{
		"internal/router":     true,
		"internal/controller": true,
		"internal/middleware": true,
	}
	budget := map[string]int{}
	for _, e := range v32GinAllowlist {
		budget[e.from] = e.budget
	}
	used := map[string]int{}
	for _, p := range v31CollectPackages(t) {
		if v31IsExempt(p.rel) {
			continue
		}
		key, _, ok := v32Resolve(p.rel)
		if !ok {
			continue // 由 TestLayerTableCoversAllPackagesV32 报
		}
		if allowed[key] {
			continue
		}
		for _, imp := range p.imports {
			if imp != v32GinPath {
				continue
			}
			used[p.rel]++
			if used[p.rel] > budget[p.rel] {
				t.Errorf("gin 围栏：%s 不得 import gin（只允许 router/controller/middleware）", p.rel)
			}
		}
	}
	for _, e := range v32GinAllowlist {
		if used[e.from] == 0 {
			t.Errorf("过期白名单：%s 已不再 import gin，请从 v32GinAllowlist 删除（计划 %s）", e.from, e.slice)
		}
	}
}
