package archtest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件是 v15 起的两条「两个 id 词汇不得互串 + 主键形状」守卫（守卫②③），v17 起改口径：
//
//	· 平台铸造 id = `<前缀>_<26 位 ULID>`（`sessions.id` / `events.id` …）——**平台单方面定义**；
//	· 底座 id     = `rt_<底座原文>`（`runtime_session_id`，v17 前叫 `provider_session_id`）
//	                ——**只加壳不解析主体**。
//
// 两个形状永不可能相等（不变式 I1），"形状即类型"这条免费检查因此回来。
// v17 增补：`events` 也有平台铸造身份（`evt_`），**位次（`seq`）与身份（`id`）分列两列**——
// 换掉 `provider` 这个词是因为它在「runtime 侧的 ACP 会话」上一直是个内部术语（见 `docs/agents/vocabulary.md` 消歧表）。

// runtimeShellCallSite 是一处**允许调用** `WrapRuntime`/`UnwrapRuntime` 的位置许可
// （设计 §4.5.3 的 4 包装 / 3 解包表 —— 这是"包装点唯一"这条不变式的机器判据）。
//
// 锚点是**包含调用点的函数名**，不是文件路径。为什么换锚：这几个点会随"哪个包拥有它"而搬家
// （v27 起 `internal/sessionruntime` 正被拆成 `internal/domain/*` / `internal/drivers/mechanism/*`），而搬家时
// 变的是**包与接收者类型**，函数名不变。钉路径的守卫在搬家后只有两种结局，都很坏：静默空转
// （判据指向一个不存在的文件，测试照样绿却什么都没检查）或假红。
//
// 函数名锚并不比路径锚弱：调用**按次数精确比对**——少了报"白名单失去对象"，多了报"多了一处调用"。
type runtimeShellCallSite struct {
	// Func 是包含调用点的函数 / 方法名（不含接收者）。匿名函数里的调用归给它外层的具名函数。
	Func   string
	Wrap   int // 该函数里 `WrapRuntime(` 的调用次数（精确值）
	Unwrap int // 同上，`UnwrapRuntime(`
	Why    string
}

// runtimeShellCallSites 是唯一允许出现 `rt_` 壳包装/解包的函数。
//
// 多一处即红——它挡的是"顺手在别处也包一层/解一层"：那会让同一个值在不同层有两种形态，
// 而两种形态**都合法**（一个平台 id 也能被 wrap 成 `rt_ses_…`），于是没有任何运行时信号。
//
// 每条 Why 末尾的「目标包」是搬家后的落点（见设计文档 §3.4 的文件去向表）：写在这里是为了
// 让下一个人搬完文件后知道该去哪儿核对，而不是靠 grep 找。
var runtimeShellCallSites = []runtimeShellCallSite{
	{
		Func: "StartSession", Wrap: 1, Unwrap: 0,
		Why: "W1 首次建会话落 rec（P4a 第五十六刀已落 `internal/domain/session/lifecycle.go`）",
	},
	{
		Func: "ConnectSession", Wrap: 1, Unwrap: 1,
		Why: "W2 原生恢复失败或迁移后换会话（最容易被漏的一处）/ U1 原生恢复前解包" +
			"（P4a 第五十六刀已落 `internal/domain/session/lifecycle.go`）",
	},
	{
		// v26.1：这一支**只剩解包**了。v15 的 W3（"`session/list` 直出底座原文，包装后才是平台侧的
		// 值"）是为「同一个变量兼作给底座用与比较/落库用两种语义」而设的；现在目录取样只保留
		// **原文那一半**——列出的会话直接当 resume 目标使，从不落 rec、也不与包装值比较
		// （平台记录那半由 U2 解包成原文后比较，两侧同为原文）。于是这里不再有包装调用点：
		// 白名单若继续要求包装，这条守卫就会为"已经不存在的东西"发许可——它自己报的那句
		// 「白名单已经失去对象」正是这个意思。别为了让这条绿而把包装加回来。
		Func: "resumeTargetsFrom", Wrap: 0, Unwrap: 1,
		Why: "U2 探测用 resume 前解包（源 `manager/model_catalog.go`，目标包 `internal/domain/provider`；" +
			"另见 v15 §4.5.3 的 W3：该包装点已随 v26.1 取样重构消失）",
	},
	{
		Func: "platformSessionIDForProvider", Wrap: 1, Unwrap: 0,
		Why: "D-4 修法：wire `_meta` 里的底座 id 要先按 `runtime_session_id` 反查平台会话（包装后比对）" +
			"（源 `manager/permission_flow.go`，目标包 `internal/domain/interaction`）",
	},
}

// runtimeShellImplPaths 是「壳包装/解包的**实现本身**与它的单测」——两处都住在 `pkg/`，
// **不参与**本次 `internal/` 的重划，所以这里继续钉精确路径：它们本来就只该各有一份。
var runtimeShellImplPaths = []string{
	"backend/internal/core/object/objid/objid.go",
	"backend/internal/core/object/objid/objid_test.go",
}

// TestRuntimeSessionNeverOnWireAsPlatformID 守卫②：两个 id 词汇不得互串。
//
// 四段判据：
//  1. `internal/router/routes_*.go` 的路径字面量不得含 `runtime_session`——底座 id 永不允许
//     出现在 URL 路径段（它一旦成了路径参数，就会有人拿它当"会话 id"用）；
//  2. DTO 结构体里 `json:"id"` 不得绑到 `Runtime*` 字段（对外 `id` 只能是平台 id）；
//  3. `SessionResp` 必须**同时**声明 `id` 与 `runtime_session_id` 两个 string 字段
//     （形状断言本身在 `frontend/scripts/smoke-e2e.mjs`：`^ses_…$` 与 `^rt_.+$`——那是运行时
//     事实，静态源码判不出来；本守卫钉的是"这两个字段还在"，避免契约面被悄悄合并掉）；
//  4. `WrapRuntime`/`UnwrapRuntime` 的调用点白名单（多一处即红）。
//
// v17 改口径：`provider_session_id` 这个名字整体退役（见 vocabulary_test.go 的
// `TestNoProviderSessionVocabulary`），所以 ①②③ 的判据字面量同步换成 `runtime_session`。
// 判据**不**同时保留新旧两套：留着旧的那一套就是给已经退役的词留一条合法通道。
//
// fail-closed：DTO 解析不到结构体、或白名单文件两个函数都不出现 → 红。
func TestRuntimeSessionNeverOnWireAsPlatformID(t *testing.T) {
	root := backendRoot(t)

	// ① 路由路径字面量。
	routes, err := filepath.Glob(filepath.Join(root, "internal", "router", "routes_*.go"))
	if err != nil || len(routes) == 0 {
		t.Fatalf("扫不到 internal/router/routes_*.go: %v（fail-closed）", err)
	}
	pathLiteral := regexp.MustCompile(`"([^"]*)"`)
	for _, path := range routes {
		src := stripLineComments(readFileOrFatal(t, path))
		for _, m := range pathLiteral.FindAllStringSubmatch(src, -1) {
			if strings.Contains(m[1], "runtime_session") || strings.Contains(m[1], "provider_session") {
				t.Fatalf("%s 的路径字面量 %q 含底座会话 id 的列名：底座 id 永不允许出现在 URL 路径段"+
					"（入口纪律，设计 §4.5.5）", filepath.Base(path), m[1])
			}
		}
	}

	// ② DTO：`json:"id"` 不得绑 `Runtime*` 字段。
	dtoFiles, err := filepath.Glob(filepath.Join(root, "internal", "dto", "*", "*.go"))
	if err != nil || len(dtoFiles) == 0 {
		t.Fatalf("扫不到 internal/dto/*/*.go: %v（fail-closed）", err)
	}
	fieldRe := regexp.MustCompile("(?m)^\\s*(\\w+)\\s+([\\w\\[\\]\\*]+)\\s+`[^`]*json:\"([^\",]+)")
	for _, path := range dtoFiles {
		src := stripLineComments(readFileOrFatal(t, path))
		for _, m := range fieldRe.FindAllStringSubmatch(src, -1) {
			name, tag := m[1], m[3]
			if tag == "id" && (strings.Contains(name, "Runtime") || strings.Contains(name, "Provider")) {
				t.Fatalf("%s 的字段 %s 用 `json:\"id\"` 暴露：对外的 `id` 只能是**平台会话 id**"+
					"（底座 id 有它自己的 `runtime_session_id` 字段）", filepath.Base(path), name)
			}
		}
	}

	// ③ `SessionResp` 的两个字段都还在、都是 string。
	respSrc := stripLineComments(readFileOrFatal(t,
		filepath.Join(root, "internal", "dto", "dtosession", "response.go")))
	respBody := structBody(t, respSrc, "SessionResp", "internal/dto/dtosession/response.go")
	for _, want := range []struct{ field, tag string }{
		{"ID", "id"},
		{"RuntimeSessionID", "runtime_session_id"},
	} {
		if !matchesJSONField(respBody, want.field, want.tag) {
			t.Fatalf("`SessionResp` 少了 `%s string` 且 `json:\"%s\"` 的字段："+
				"两个 id 各有各的键是「形状可分」的前提（契约冒烟按这两个键断言形状）", want.field, want.tag)
		}
	}

	// ④ 包装/解包调用点白名单（按**函数名**累计，见 runtimeShellCallSite）。
	scanned := 0
	implSeen := 0
	got := map[string][2]int{} // 函数名 → (wrap, unwrap)
	for _, dir := range []string{
		filepath.Join(root, "internal"),
	} {
		for _, path := range goFilesUnder(t, dir) {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				t.Fatalf("相对路径: %v", rerr)
			}
			relSlash := "backend/" + filepath.ToSlash(rel)
			if strings.HasPrefix(filepath.ToSlash(rel), "internal/archtest/") {
				// 跳过本包：这条守卫自己的白名单与判据字面量就写着 `WrapRuntime(` /
				// `UnwrapRuntime(`（与 knowledge_boundary_test.go、logging_boundary_test.go
				// 同一手法：只豁免 internal/archtest 这一个目录）。
				continue
			}
			src := stripLineComments(readFileOrFatal(t, path))
			if containsStr(runtimeShellImplPaths, relSlash) {
				// 实现与它的单测：共同点是"这两个名字必须出现在这个文件里"，但**形态不同**——
				// 实现文件里是**定义**（`func WrapRuntime(` / `func UnwrapRuntime(`，所以它的
				// 调用点数是 0，按调用点判会把它整个漏掉），单测文件里是调用。故按名字出现判。
				if !strings.Contains(src, "WrapRuntime") || !strings.Contains(src, "UnwrapRuntime") {
					t.Fatalf("%s 是 `rt_` 壳包装/解包的实现或它的单测，但里面找不到 `WrapRuntime` / "+
						"`UnwrapRuntime` 这两个名字：实现被搬走或改名了？fail-closed，别让这条守卫空转", relSlash)
				}
				implSeen++
				continue
			}
			sites := runtimeShellSites(src)
			if len(sites) == 0 {
				continue
			}
			scanned++
			for _, s := range sites {
				if _, ok := runtimeShellCallSiteFor(s.fn); !ok {
					t.Fatalf("%s 的 `%s` 里调用了 `rt_` 壳的包装/解包，但它不在白名单里。\n"+
						"包装/解包**各只有一个实现、且只在 §4.5.3 那几个边界点上调用**——多一处会让同一个值"+
						"在不同层有两种形态，而两种形态都合法（没有运行时信号）。要加一处，请同时更新 "+
						"runtimeShellCallSites（按**函数名**登记）并写明理由。", relSlash, s.fn)
				}
				c := got[s.fn]
				if s.wrap {
					c[0]++
				} else {
					c[1]++
				}
				got[s.fn] = c
			}
		}
	}
	if implSeen != len(runtimeShellImplPaths) {
		t.Fatalf("壳包装/解包的实现文件扫到 %d 个、白名单写着 %d 个（fail-closed：实现被搬走或改名后，"+
			"「包装点唯一」这条不变式就没有判据了）", implSeen, len(runtimeShellImplPaths))
	}
	for _, c := range runtimeShellCallSites {
		if strings.TrimSpace(c.Why) == "" {
			t.Fatalf("白名单条目 `%s` 的理由是空的：空理由不算登记", c.Func)
		}
		g := got[c.Func]
		if g[0] == 0 && g[1] == 0 {
			t.Fatalf("白名单函数 `%s` 里扫不到任何包装/解包调用：白名单已经失去对象（映射改道了？）"+
				"——fail-closed，别让它变成空转的许可", c.Func)
		}
		if g[0] != c.Wrap || g[1] != c.Unwrap {
			t.Fatalf("`%s` 的包装/解包调用次数变了：got wrap=%d unwrap=%d，want wrap=%d unwrap=%d。\n"+
				"次数是**精确值**：多了说明有人又包了一层/又解了一层，少了说明某个边界点被搬走或删掉了"+
				"——两种都要先把 §4.5.3 那张表更新成「现在的真相」，再改这里。", c.Func, g[0], g[1], c.Wrap, c.Unwrap)
		}
	}
	if scanned == 0 || implSeen == 0 {
		t.Fatalf("包装/解包调用点扫不到（scanned=%d implSeen=%d）——fail-closed，"+
			"否则「包装点唯一」这条不变式没有任何判据", scanned, implSeen)
	}
}

// runtimeShellSite 是一次 `WrapRuntime(` / `UnwrapRuntime(` 调用，以及它所在的**具名函数**。
type runtimeShellSite struct {
	fn   string
	wrap bool
}

var (
	// 具名函数声明（`func Name(` 或 `func (recv) Name(`）；匿名函数字面量 `func(` 刻意不匹配
	// ——闭包里的调用因此归给它外层的具名函数。
	namedFuncDecl = regexp.MustCompile(`^func (?:\*?\([^)]*\) )?([A-Za-z_]\w*)\(`)
	// 实现自身的定义行（`func WrapRuntime(`）不算调用点。
	shellImplDecl = regexp.MustCompile(`^func (?:WrapRuntime|UnwrapRuntime)\(`)
)

// runtimeShellSites 按**行**扫出一处文件里所有包装/解包调用点，并归到它们所在的具名函数。
//
// 为什么按行而不按字节偏移：`stripLineComments` 逐行剥注释、行数不变，所以"最近一个具名函数
// 声明行"就是所在函数，不需要任何解析器。
func runtimeShellSites(src string) []runtimeShellSite {
	var out []runtimeShellSite
	fn := ""
	for _, ln := range strings.Split(src, "\n") {
		if m := namedFuncDecl.FindStringSubmatch(ln); m != nil && !shellImplDecl.MatchString(ln) {
			fn = m[1]
		}
		if shellImplDecl.MatchString(ln) {
			continue
		}
		// `UnwrapRuntime(` 里的小写 `wrapRuntime(` 不匹配 `WrapRuntime(`，两个计数互不污染。
		for i := 0; i < strings.Count(ln, "WrapRuntime("); i++ {
			out = append(out, runtimeShellSite{fn: fn, wrap: true})
		}
		for i := 0; i < strings.Count(ln, "UnwrapRuntime("); i++ {
			out = append(out, runtimeShellSite{fn: fn, wrap: false})
		}
	}
	return out
}

// fileContaining 返回 `dir` 下**唯一**一个含 `sym` 的非测试 Go 文件（返回绝对路径）。
//
// fail-closed 两侧都红：0 个 ⇒ 判据已经失去对象（那个符号被删了或改名了）；≥2 个 ⇒ 该符号
// 不再有唯一持有者（装载面被复制了）——后者正是"按符号而不是按文件路径"换来的新判据。
func fileContaining(t *testing.T, dir, sym string) string {
	t.Helper()
	var hits []string
	for _, p := range goFilesUnder(t, dir) {
		if strings.HasSuffix(p, "_test.go") || strings.Contains(filepath.ToSlash(p), "/archtest/") {
			continue
		}
		if strings.Contains(stripLineComments(readFileOrFatal(t, p)), sym) {
			hits = append(hits, p)
		}
	}
	sort.Strings(hits)
	if len(hits) != 1 {
		t.Fatalf("`%s` 下含 %q 的非测试 Go 文件应**恰好一个**，实际 %d 个：%v\n"+
			"（0 个 ⇒ 该符号被删/改名，判据失去对象；≥2 个 ⇒ 唯一持有者不再唯一——两种都要红，"+
			"否则这条守卫会静默地不再检查任何东西）", dir, sym, len(hits), hits)
	}
	return hits[0]
}

// matchesJSONField 判断成员体里是否有 `Field string \`... json:"tag"...\“ 形态的字段
// （tag 允许带 `,omitempty` 之类的选项：判据是"这个键在"，不是"它没有选项"）。
func matchesJSONField(body, field, tag string) bool {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(field) + `\s+string\s+` + "`" + `[^` + "`" + `]*json:"` + regexp.QuoteMeta(tag))
	return re.MatchString(body)
}

// runtimeShellCallSiteFor 按**函数名**查白名单。
func runtimeShellCallSiteFor(fn string) (runtimeShellCallSite, bool) {
	for _, c := range runtimeShellCallSites {
		if strings.TrimSpace(c.Why) == "" {
			continue // 空理由不算登记
		}
		if c.Func == fn {
			return c, true
		}
	}
	return runtimeShellCallSite{}, false
}

// structBody 取出 `type <name> struct { ... }` 的成员体（fail-closed：解析不到即失败）。
func structBody(t *testing.T, src, name, where string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)type ` + regexp.QuoteMeta(name) + ` struct \{(.*?)\n\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("在 %s 里解析不到 `type %s struct`（fail-closed）", where, name)
	}
	return m[1]
}

// primaryKeyEntity 是一个"平台铸造主键"的实体：`ID` 必须是 string + `column:id;primaryKey`。
type primaryKeyEntity struct {
	Name string
	File string // 相对 internal/persistence/model
}

// platformIDEntities v15 起由 `objid` 铸号的平台主键实体（§4.2 的前缀登记表一一对应；
// v17 加 `EventEntity` —— 事件从"以 seq 为身份"改成"id 是身份、seq 是位次"）。
var platformIDEntities = []primaryKeyEntity{
	{"SessionEntity", "session.go"},
	{"RunEntity", "run.go"},
	{"MessageEntity", "message.go"},
	{"InputEntity", "input.go"},
	{"WorkspaceEntity", "workspace.go"},
	{"ModelEntity", "model.go"},
	{"PermissionMemoryEntity", "permission_memory.go"},
	{"EventEntity", "event.go"},
}

// monotonicCursorEntities 主键是**单调游标**、刻意**不**换前缀 id 的实体（§1.4 非目标）：
// `definition_revisions.seq` 直接当能力代际 rev，换 TEXT 会毁掉 `MAX(seq)` 与索引序，收益为零。
//
// v17 起 `events` 从这里**移出**：它就是被"换回游标当身份"这件顺手事伤到的那个（同一个事件
// 每次重启换一个 seq，客户端引用不了）。它的形状由 G1（`TestEventSeqStaysTheCursor`）单钉：
// 主键必须是 `evt_` 铸号、`seq` 必须还在但**不再**是主键。
var monotonicCursorEntities = []primaryKeyEntity{
	{"DefinitionRevisionEntity", "definition_revision.go"},
}

// customIDEntities 主键是**用户自定义文件名**的定义类实体（§1.4：不加前缀）。
//
// `providers.id` 还参与 vault 的 AAD（`vault:providers/<id>`）——改它等于让已加密的密钥
// 全部解不开，需要重加密工具，不在本次范围。
var customIDEntities = []primaryKeyEntity{
	{"SkillEntity", "skill.go"},
	{"McpServerEntity", "mcp_server.go"},
	{"AgentEntity", "agent.go"},
	{"AutomationEntity", "automation.go"},
	{"ProviderEntity", "provider.go"},
}

// TestPrimaryKeyShapesStayDeclared 守卫③：主键形状与 transcript 装载面的直通禁令。
//
// 它挡的是三类"顺手改"（每一类的后果都是静默的）：
//  1. 把某个平台铸造主键改回 `int64` / 去掉 `primaryKey`（库不再保证行身份）；
//  2. 把 `definition_revisions.seq`"统一成前缀 id"（毁掉 `MAX(seq)` 与代际 rev）；
//  3. 把定义类的 id 也登记进 `objid` 的前缀表（用户就不能给自己的 agent 起名了，
//     而 provider 的 vault 密文会全部作废）；
//  4. `transcript_load.go` 里"未经形状校验就把 header 的 id 当平台 id"（旧数据形状复活）。
//
// v17 起 `events` 归 ①（身份是 `evt_` 铸号），它的"游标不再是主键"那半由 G2 单钉。
//
// fail-closed：一个目标实体都解析不到 → 红。
func TestPrimaryKeyShapesStayDeclared(t *testing.T) {
	modelDir := filepath.Join(backendRoot(t), "internal", "persistence", "model")

	readEntity := func(e primaryKeyEntity) string {
		t.Helper()
		src := stripLineComments(readFileOrFatal(t, filepath.Join(modelDir, e.File)))
		return structBody(t, src, e.Name, "internal/persistence/model/"+e.File)
	}

	// ① 平台铸造主键：string + `column:id;primaryKey`。
	for _, e := range platformIDEntities {
		body := readEntity(e)
		re := regexp.MustCompile(`(?m)^\s*ID\s+string\s+` + "`" + `[^` + "`" + `]*column:id;primaryKey`)
		if !re.MatchString(body) {
			t.Fatalf("`%s.ID` 必须是 `string` 且带 `column:id;primaryKey`（v15 起主键由 Go 侧铸号：\n"+
				"`<前缀>_<26 位 ULID>`；改成 int64/去掉 primaryKey 会让库不再保证行身份）。\n实际成员体：\n%s",
				e.Name, body)
		}
	}

	// ② 单调游标：必须是 int64。
	for _, e := range monotonicCursorEntities {
		body := readEntity(e)
		re := regexp.MustCompile(`(?m)^\s*Seq\s+int64\s+` + "`" + `[^` + "`" + `]*column:seq;primaryKey`)
		if !re.MatchString(body) {
			t.Fatalf("`%s.Seq` 必须是 `int64`（单调游标）：换 TEXT 会毁掉 `MAX(seq)` 与索引序，"+
				"而 `definition_revisions.seq` 直接就是能力代际 rev。\n实际成员体：\n%s", e.Name, body)
		}
	}

	// ③ `objid` 的前缀登记表 == 8 个平台铸造前缀，**不含**任何定义类。
	objidSrc := stripLineComments(readFileOrFatal(t,
		filepath.Join(backendRoot(t), "internal", "core", "object", "objid", "objid.go")))
	// 前缀长度放宽到 2–5：v33 起新增 `libnd_`（资料库节点，5 字母）——它同样是平台铸造前缀，
	// 必须被这条守卫看见（否则"少登记一个前缀"那一半判据会对它空转）。
	prefixRe := regexp.MustCompile(`(?m)^\s*([A-Z]\w+)\s*=\s*"([a-z]{2,5}_)\s*"`)
	type kv struct{ name, value string }
	var got []kv
	for _, m := range prefixRe.FindAllStringSubmatch(objidSrc, -1) {
		if m[1] == "RuntimeShell" {
			continue
		}
		got = append(got, kv{m[1], m[2]})
	}
	var gotNames []string
	for _, g := range got {
		gotNames = append(gotNames, g.name)
	}
	sort.Strings(gotNames)
	wantNames := []string{"Attachment", "Event", "Input", "LibraryNode", "Message", "Model", "PermissionMemory", "Run", "Session", "Workspace"}
	sort.Strings(wantNames)
	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("`objid` 的铸造前缀登记表变了：\n got  %v\n want %v\n"+
			"登记表 == 「平台铸造」这件事本身：把定义类（skill/mcp/agent/automation/provider）登记进来"+
			"就等于给用户自定义 id 加前缀（且 provider 的 vault 密文会全部解不开）；"+
			"少一个则那个实体的铸号会去用别的前缀。", gotNames, wantNames)
	}

	// ④ transcript 装载面：不得把 header 的 id 直接当平台 id。
	//
	// 锚点是**闸门调用本身**（`objid.IsPlatformID(h.SessionID)`）：闸门是装载面唯一的形状入口，
	// 也是搬家时不会变的那一半（文件会移包，这行调用不会）。源 `internal/sessionruntime/transcript_load.go`
	// → 目标 `internal/domain/session/`。
	//
	// 这一步同时修掉一条**本来就没在检查**的断言：旧判据找的是 `ID: hdr.SessionID` 直通，而
	// `hdr.SessionID` 在整个仓库里只出现在一句注释里（`hdr` 还是另一个函数的变量），剥注释后
	// 永远匹配不到——它一直是空转的。换成按**读取次数**判：`h.SessionID` 在该文件里恰好被读四处
	// （取 header / 过闸门 / WARN 日志 / 落 rec），多一处就是有人绕开闸门从别处拿了这个 id。
	tlPath := fileContaining(t, filepath.Join(backendRoot(t), "internal"), "objid.IsPlatformID(h.SessionID)")
	tlSrc := stripLineComments(readFileOrFatal(t, tlPath))
	const hSessionIDReads = 4
	if got := strings.Count(tlSrc, "h.SessionID"); got != hSessionIDReads {
		t.Fatalf("%s 里 `h.SessionID` 被读了 %d 次（期望 %d）：读点只有「取 header / 过形状闸门 / "+
			"WARN 日志 / 落 rec」这四处。多一处意味着有人绕开闸门直接用了它，少一处意味着闸门或落 rec "+
			"被搬走了——两种的后果都是静默的：数据面出现 `ses_…` 与裸 uuid 两种会话 id 形状",
			tlPath, got, hSessionIDReads)
	}
	if !strings.Contains(tlSrc, "sessionID = h.SessionID") {
		t.Fatalf("%s 里找不到 `sessionID = h.SessionID`：落 rec 用的必须是**过了闸门**的那个变量"+
			"（`h.SessionID` 只允许出现在校验那一处，落 rec 用校验过的变量）", tlPath)
	}
}

// TestEventSeqStaysTheCursor（G1，v17 立、v21 重写）：`events` 的**身份**是 `id`、**位次**是
// `seq`，两者不得换位；而 v21 起位次是**会话内**的量（`UNIQUE(session_id, seq)`，从 0 起），
// 不再是全局号序。
//
// 为什么单独一条（而不是并进 TestPrimaryKeyShapesStayDeclared）：`events` 是唯一一张**同时**
// 需要"平台铸造身份"与"位次游标"的表，而那两件事各自都有一种静默的失效方式：
//
//	① 把 `seq` 又写回主键（或把它删掉）——位次的权威分配链断掉，回放/书签全错；
//	② 让 `id` 参与排序或比较（`ORDER BY id` / `WHERE id > ?`）——铸号顺序**恰好**是 seq 升序
//	   （迁移与运行时都按 seq 递增铸号），所以它会"看起来对"，直到跨进程/时钟回拨那天才错；
//	③ 位次的作用域悄悄退回**全库**（唯一约束写成 `UNIQUE(seq)` 或让 `idx_events_seq` 复活）
//	   ——两个会话就会互相顶号，而"第 0 条"这个概念也随之消失。
//
// v21 把 ② 的判据从"扫迁移文本里那条 CREATE UNIQUE INDEX"改成了**读真实 schema**：v17 建过
// `idx_events_seq`、v21 换表时把它整个丢掉，所以文本扫描会同时看到"建过"和"没了"两个真相
// （要么假红，要么得写一个 DDL 模拟器）。跑一遍真实迁移读索引定义才是最终形态本身。
//
// fail-closed：实体、真实 schema、DAO 任一解析不到 → 红。
func TestEventSeqStaysTheCursor(t *testing.T) {
	root := backendRoot(t)

	// ① 实体：`id` 是主键；`seq` 在、是 int64、且**不带** primaryKey。
	evSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "persistence", "model", "event.go")))
	body := structBody(t, evSrc, "EventEntity", "internal/persistence/model/event.go")
	if !regexp.MustCompile(`(?m)^\s*ID\s+string\s+` + "`" + `[^` + "`" + `]*column:id;primaryKey`).MatchString(body) {
		t.Fatalf("`EventEntity.ID` 必须是 `string` 且带 `column:id;primaryKey`（v17 起事件身份由 objid 铸："+
			"`evt_<26 位 ULID>`）。\n实际成员体：\n%s", body)
	}
	seqRe := regexp.MustCompile(`(?m)^\s*Seq\s+int64\s+` + "`" + `([^` + "`" + `]*)` + "`")
	sm := seqRe.FindStringSubmatch(body)
	if sm == nil {
		t.Fatalf("`EventEntity.Seq` 必须是 `int64`（会话内位次，`MAX(seq)` 与回放都靠它）。\n实际成员体：\n%s", body)
	}
	if !strings.Contains(sm[1], "column:seq") {
		t.Fatalf("`EventEntity.Seq` 的列名必须是 `seq`，实际标签 %q", sm[1])
	}
	if strings.Contains(sm[1], "primaryKey") {
		t.Fatalf("`EventEntity.Seq` 又变成主键了（标签 %q）：位次只在**会话内**有序，"+
			"把它当主键会让「同一个事件在不同重启后换一个 id」这类问题重新长回来", sm[1])
	}

	// ② 真实 schema：`events` 上的唯一约束必须是 **(session_id, seq)**。
	//    读索引定义而不是扫 DDL：v17 建过 `idx_events_seq`（全局唯一）、v21 换表时连带丢掉，
	//    文本里两种真相并存，只有跑完迁移的 schema 才是最终形态。
	db := migratedSchemaDB(t)
	var idxSQL string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_events_session'`).
		Row().Scan(&idxSQL); err != nil {
		t.Fatalf("读 `idx_events_session` 定义（v21 起它必须是 UNIQUE）：%v", err)
	}
	if !strings.HasPrefix(idxSQL, "CREATE UNIQUE INDEX") || !strings.Contains(idxSQL, "events(session_id, seq)") {
		t.Fatalf("`idx_events_session` 必须是 `CREATE UNIQUE INDEX … ON events(session_id, seq)`，实际 %q："+
			"位次的作用域就是这条约束——写成 `UNIQUE(seq)` 会让两个会话互相顶号，"+
			"而「每个会话各自从 0 起」也随之失效", idxSQL)
	}
	var n int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_events_seq'`).
		Row().Scan(&n); err != nil {
		t.Fatalf("查 `idx_events_seq` 是否还在: %v", err)
	}
	if n != 0 {
		t.Fatal("`idx_events_seq`（**全局**位次唯一）又回到了 schema 里：v21 起位次是会话内的量，" +
			"这条索引的语义已经不存在——它的复活意味着「跨会话全局号序」这个已被推翻的设计又长了回来")
	}
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_events_session_type'`).
		Row().Scan(&n); err != nil {
		t.Fatalf("查 `idx_events_session_type`: %v", err)
	}
	if n != 1 {
		t.Fatalf("`idx_events_session_type`（按类型回放）应存在：n=%d", n)
	}
	// `event_sequence` 是取号器：主键是**会话**，不是一张全局计数表。
	var pkCol string
	if err := db.Raw(`SELECT name FROM pragma_table_info('event_sequence') WHERE pk > 0`).
		Row().Scan(&pkCol); err != nil {
		t.Fatalf("读 `event_sequence` 主键（fail-closed）：%v", err)
	}
	if pkCol != "session_id" {
		t.Fatalf("`event_sequence` 的主键必须是 `session_id`（每会话一行计数器），实际 %q", pkCol)
	}

	// ③ 读面（store）：事件查询只按 `seq` 排序/翻页，不得引入 `id` 序。
	//
	// 判据作用在 `internal/persistence/store/events.go`：v3.3 起排序/游标口径归 store（DAO 只剩列词汇与
	// 泛型方法）——这条守卫必须跟着实现走，否则它会在 dao/event.go 里对着一个空文件空转。
	daoSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "persistence", "store", "events.go")))
	// `id > ?` 用"前一个字符不是标识符字符"锚定：复合键集比较里的 `session_id > ?` 含有子串
	// `id > ?`，用裸 Contains 会把它误判成"按事件身份比较"（v21 的 AllBySession 就是这么被误报的）。
	idCompare := regexp.MustCompile(`(?:^|[^A-Za-z0-9_"'])id\s*>\s*\?`)
	for _, bad := range []string{"ORDER BY id", "Order(\"id\"", "Order(\"id "} {
		if strings.Contains(daoSrc, bad) {
			t.Fatalf("`internal/persistence/store/events.go` 里出现了按 `id` 排序的写法 %q：事件的身份**不参与排序**"+
				"（位次的权威只有 `seq`；铸号顺序恰好同序会让这类错误在很长时间里看不出来）", bad)
		}
	}
	if idCompare.MatchString(daoSrc) {
		t.Fatal("`internal/persistence/store/events.go` 里出现了按 `id` 比较的写法（`id > ?`）：事件的身份**不参与比较**" +
			"——跨会话翻页用的是复合键 `(session_id, seq)`，而 `session_id > ?` 是它的一部分，" +
			"别把两者混起来")
	}
	if !strings.Contains(daoSrc, "Order(\"seq") && !strings.Contains(daoSrc, "Order(\"session_id ASC, seq ASC\"") {
		t.Fatal("`internal/persistence/store/events.go` 里找不到按 `seq` 的排序：事件游标只认 seq（fail-closed，" +
			"否则「游标仍然是 seq」这句话没有任何判据）")
	}
}

// TestEventIdentityTravelsWithData（G2，v17 / 判据 J4）：身份必须在**每一个载体**上原样传递。
//
// 为什么这条必须逐点钉（每一处漏掉的表现都是静默的，且**形状仍然合法**）：
//
//	① 落库：`events.id` 由运行时铸一次（`manager/publish.go`），其余路径只搬运、不重铸——
//	   重铸的后果是"帧上那个 id 在库里查不到"；
//	② transcript 落盘：写进去的必须是**同一个** id（不是落盘时再铸一个）；
//	③ memory 模式装载：从 transcript 读回来的 id/seq 原样注入 bus（否则重启后同一个事件换个身份）；
//	④ 恢复（sqlite 模式）：从库里读出来的 id 原样注入 bus；
//	⑤ REST/WS 出口：两条出口都用 `eventbus.Event`（json tag 带 id），所以这里钉的是
//	   "构造时把 `e.ID` 放进去了"；
//	⑥ 导入：`mate migrate` 把 transcript 的 id 交给 store（不重铸，缺 id 直接失败）；
//	⑦ 只走实时通道的事件（`automation.run`）**刻意不铸号**——"帧上有 id ⟺ 它在 durable 日志里
//	   查得到"这条不变式的另一半。
//
// 判据是**调用点字面量**（正则容空白）：这层的正确性没有运行时信号，只有"这一行还在不在"。
//
// v27 起锚点是**符号在全树里的出现次数**，不是文件路径：这些点会随包搬家
// （`internal/sessionruntime/*` → `internal/domain/*` / `internal/drivers/mechanism/*`），而钉路径的判据
// 一搬就静默空转。次数是**实测精确值**，两侧都拦：多了说明有人又铸了一支 id / 又开了一条出口
// （正是要拦的），少了说明某个载体丢了身份。
//
// fail-closed：扫描面为空即红；次数不符即红。
func TestEventIdentityTravelsWithData(t *testing.T) {
	root := backendRoot(t)
	cases := []struct {
		pat string // 正则
		n   int    // 全树（非测试、非 archtest）期望出现次数——精确值
		why string
	}{
		{`objid\.New\(objid\.Event\)`, 2,
			"① 运行时**唯一的**事件铸号点 + v17 迁移的历史回填各一处（分部由 ⑦′ 单独断言）：少了它，事件就没有身份"},
		{`AppendEvent\(context\.Background\(\), evtID,`, 1,
			"① 落库必须带上刚铸的那支 id（不重铸）"},
		{`PublishAt\(sessionID, seq, evtID,`, 1,
			"① 帧上也必须是同一支 id（`PublishAt` 的第三参数；v21 起第一位是聚合 id）"},
		{`eventbus\.Event\{ID: evtID,`, 1,
			"① nil-bus（内存模式无 bus）分支同样带 id"},
		{`publishSideEffects\(sessionID, evt\.ID,`, 1,
			"② 副作用（transcript）拿的是**已发布事件**的 id，不是再铸一支"},
		{`appendLine\(sessionID, eventID,`, 1,
			"② transcript 落盘的 id 来自上游（本函数不再铸号；P3b-2b recorder 随装载面落 " +
				"`domain/session`，端口 `TranscriptSink` 删除后方法名从 `AppendLine` 回到小写——判据不变）"},
		{`wireTranscriptLine\{ID: eventID,`, 1,
			"② v2 事件行的第一个字段就是身份"},
		{`PublishAt\(p\.sessionID, p\.line\.seq, p\.line\.id,`, 1,
			"③ memory 模式装载：transcript 的 id/seq 原样注入 bus（按会话分组）"},
		{`id: ev\.ID, seq: ev\.Seq,`, 1,
			"③ 事件行必须先过形状校验再落进内存行"},
		{`PublishAt\(ev\.SessionID, ev\.Seq, ev\.ID,`, 1,
			"④ sqlite 模式恢复：库里的 id 原样注入 bus"},
		{`ID: e\.ID, Type: e\.Type,`, 2,
			"⑤ 两条读模型出口都带 id（REST 导出 + 会话历史）"},
		{`model\.EventEntity\{ID: l\.ID,`, 1,
			"⑥ 导入把 transcript 的 id 交给 store（缺 id 时前面已 fail-loud）"},
		{`Event\{ID: id,`, 1,
			"⑦ 帧结构体必须真的把 id 放进去（omitempty 只对「没有身份」的事件生效）"},
		{`Publish\(\s*""\s*,`, 1,
			"⑦ 只走实时通道的事件刻意不带身份（空串），「帧上有 id ⟺ durable 日志里查得到」才成立"},
	}

	srcs := eventIdentityScan(t, root)
	for _, c := range cases {
		re := regexp.MustCompile(c.pat)
		total, where := 0, map[string]int{}
		for rel, src := range srcs {
			if n := len(re.FindAllString(src, -1)); n > 0 {
				total += n
				where[rel] = n
			}
		}
		if total != c.n {
			t.Fatalf("%q 在全树出现 %d 次（期望 %d）—— %s\n命中：%s\n"+
				"（J4：身份必须随数据走到**每一个**载体；次数不符要么是某个载体丢了身份，"+
				"要么是有人又铸了一支 id / 又开了一条出口——两种都让「帧上那个 id 在库里查得到」不再成立）",
				c.pat, total, c.n, c.why, formatHits(where))
		}
	}

	// ⑦′ 铸号点的**分部**：运行时恰好一处，其余必须都在 `migrate_v*.go` 的历史回填里。
	// 上表那条 `n: 2` 只说什么都没变；能区分「运行时 + 迁移」与「运行时 + 运行时」的只有这半边。
	mintRe := regexp.MustCompile(`objid\.New\(objid\.Event\)`)
	mintRuntime, mintMigration := 0, 0
	for rel, src := range srcs {
		n := len(mintRe.FindAllString(src, -1))
		if n == 0 {
			continue
		}
		if strings.Contains(filepath.Base(rel), "migrate_v") {
			mintMigration += n
		} else {
			mintRuntime += n
		}
	}
	if mintRuntime != 1 || mintMigration != 1 {
		t.Fatalf("事件铸号点的分部变了：运行时 %d 处（want 1）、迁移回填 %d 处（want 1）。\n"+
			"「运行时只铸一次」这条不变式只在**迁移之外**成立——迁移是历史数据的回填，与运行时铸号"+
			"是两件事；把两者混成一个数字，等于允许第二条运行时铸号路径存在。", mintRuntime, mintMigration)
	}

	// ⑧ 导入判重/分叉：同一支 id 第二次导入必须**逐字节比对内容**再决定跳过还是报错。
	//    （这一处钉的是 `internal/persistence/store` 的精确路径：该包**不在**本次重划范围内。）
	impSrc := stripLineComments(readFileOrFatal(t,
		filepath.Join(root, "internal", "persistence", "store", "import.go")))
	if !strings.Contains(impSrc, "FindEventByIDTx(") {
		t.Fatal("`internal/persistence/store/import.go` 里找不到 `FindEventByIDTx(`：导入不做事件级判重，" +
			"重复导入要么写第二份同 id 的行（主键冲突）要么整批失败——两种都不是幂等")
	}
	if !strings.Contains(impSrc, "分叉") {
		t.Fatal("`internal/persistence/store/import.go` 里找不到「分叉」这条错误：" +
			"同 id 不同内容必须**报错**而不是静默跳过（静默跳过 = 库里留着两份同 id 不同内容的行）")
	}
}

// eventIdentityScan 返回「相对 `backend/` 的斜杠路径 → 剥注释后的源码」。
//
// 扫描面 = `internal/` 下的**非测试、非 archtest** 文件：判据是按符号计数，所以必须把守卫自己
// 与测试排除掉——它们会写同样的字面量（与 knowledge_boundary_test.go、logging_boundary_test.go
// 同一手法）。fail-closed：一个文件都扫不到即红。
func eventIdentityScan(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range goFilesUnder(t, filepath.Join(root, "internal")) {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatalf("相对路径: %v", err)
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "internal/archtest/") {
			continue
		}
		out[rel] = stripLineComments(readFileOrFatal(t, p))
	}
	if len(out) == 0 {
		t.Fatal("事件身份守卫的扫描面是空的（fail-closed）：判据会静默空转，等于没有这条守卫")
	}
	return out
}

// formatHits 把"哪个文件命中几次"排成稳定的一行，失败信息里能直接看出是多了还是少了。
func formatHits(hits map[string]int) string {
	if len(hits) == 0 {
		return "（全树零命中）"
	}
	keys := make([]string, 0, len(hits))
	for k := range hits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, hits[k]))
	}
	return strings.Join(parts, " · ")
}
