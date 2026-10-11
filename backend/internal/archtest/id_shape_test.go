package archtest

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件是 v15 的两条「两个 id 词汇不得互串」守卫（守卫②③）。
//
// 背景（设计 §4.5 选项 D）：v15 给**底座透传 id** 加了固定壳 `rt_`，于是
//   · 平台铸造 id = `ses_<26 位 ULID>`（`sessions.id`）——**平台单方面定义**；
//   · 底座 id      = `rt_<底座原文>`（`provider_session_id`）——**只加壳不解析主体**。
// 两个形状永不可能相等（不变式 I1），"形状即类型"这条免费检查因此回来。

// providerShellCallSite 是一个允许调用 `WrapProvider`/`UnwrapProvider` 的文件
// （设计 §4.5.3 的 4 包装 / 3 解包表 —— 这是"包装点唯一"这条不变式的机器判据）。
type providerShellCallSite struct {
	Path string
	// Wrap / Unwrap 该文件**必须**出现哪一个（fail-closed：白名单文件里两个都不出现
	// ⇒ 映射改道了，白名单已经失去对象）。
	Wrap   bool
	Unwrap bool
	Why    string
}

// providerShellCallSites 是唯一允许出现 `rt_` 壳包装/解包的文件。
//
// 多一处即红——它挡的是"顺手在别处也包一层/解一层"：那会让同一个值在不同层有两种形态，
// 而两种形态**都合法**（一个平台 id 也能被 wrap 成 `rt_ses_…`），于是没有任何运行时信号。
var providerShellCallSites = []providerShellCallSite{
	{
		Path: "backend/pkg/object/objid/objid.go", Wrap: true, Unwrap: true,
		Why: "唯一实现本身（`pkg/object/objid`）——包装/解包都只有这一份代码",
	},
	{
		Path: "backend/internal/manager/session_start.go", Wrap: true, Unwrap: true,
		Why: "W1 首次建会话落 rec / W2 原生恢复失败或迁移后换会话（最容易被漏的一处）/ U1 原生恢复前解包",
	},
	{
		Path: "backend/internal/manager/model_catalog.go", Wrap: true, Unwrap: true,
		Why: "W3 `session/list` 直出底座原文（第二个最容易漏的点）/ U2 探测用 resume 前解包",
	},
	{
		Path: "backend/internal/manager/permission_flow.go", Wrap: true, Unwrap: false,
		Why: "D-4 修法：wire `_meta` 里的底座 id 要先按 `provider_session_id` 反查平台会话（包装后比对）",
	},
	{
		Path: "backend/pkg/object/objid/objid_test.go", Wrap: true, Unwrap: true,
		Why: "唯一实现的单测（含三条错误路径）",
	},
}

// TestProviderSessionNeverOnWireAsPlatformID 守卫②：两个 id 词汇不得互串。
//
// 四段判据：
//  1. `internal/router/routes_*.go` 的路径字面量不得含 `provider_session`——底座 id 永不允许
//     出现在 URL 路径段（它一旦成了路径参数，就会有人拿它当"会话 id"用）；
//  2. DTO 结构体里 `json:"id"` 不得绑到 `Provider*` 字段（对外 `id` 只能是平台 id）；
//  3. `SessionResp` 必须**同时**声明 `id` 与 `provider_session_id` 两个 string 字段
//     （形状断言本身在 `frontend/scripts/smoke-e2e.mjs`：`^ses_…$` 与 `^rt_.+$`——那是运行时
//     事实，静态源码判不出来；本守卫钉的是"这两个字段还在"，避免契约面被悄悄合并掉）；
//  4. `WrapProvider`/`UnwrapProvider` 的调用点白名单（多一处即红）。
//
// fail-closed：DTO 解析不到结构体、或白名单文件两个函数都不出现 → 红。
func TestProviderSessionNeverOnWireAsPlatformID(t *testing.T) {
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
			if strings.Contains(m[1], "provider_session") {
				t.Fatalf("%s 的路径字面量 %q 含 `provider_session`：底座 id 永不允许出现在 URL 路径段"+
					"（入口纪律，设计 §4.5.5）", filepath.Base(path), m[1])
			}
		}
	}

	// ② DTO：`json:"id"` 不得绑 `Provider*` 字段。
	dtoFiles, err := filepath.Glob(filepath.Join(root, "internal", "dto", "*", "*.go"))
	if err != nil || len(dtoFiles) == 0 {
		t.Fatalf("扫不到 internal/dto/*/*.go: %v（fail-closed）", err)
	}
	fieldRe := regexp.MustCompile("(?m)^\\s*(\\w+)\\s+([\\w\\[\\]\\*]+)\\s+`[^`]*json:\"([^\",]+)")
	for _, path := range dtoFiles {
		src := stripLineComments(readFileOrFatal(t, path))
		for _, m := range fieldRe.FindAllStringSubmatch(src, -1) {
			name, tag := m[1], m[3]
			if tag == "id" && strings.Contains(name, "Provider") {
				t.Fatalf("%s 的字段 %s 用 `json:\"id\"` 暴露：对外的 `id` 只能是**平台会话 id**"+
					"（底座 id 有它自己的 `provider_session_id` 字段）", filepath.Base(path), name)
			}
		}
	}

	// ③ `SessionResp` 的两个字段都还在、都是 string。
	respSrc := stripLineComments(readFileOrFatal(t,
		filepath.Join(root, "internal", "dto", "dtosession", "response.go")))
	respBody := structBody(t, respSrc, "SessionResp", "internal/dto/dtosession/response.go")
	for _, want := range []struct{ field, tag string }{
		{"ID", "id"},
		{"ProviderSessionID", "provider_session_id"},
	} {
		re := regexp.MustCompile(`(?m)^\s*` + want.field + `\s+string\s+` + "`" + `[^` + "`" + `]*json:"` + want.tag + `"`)
		_ = re
		if !matchesJSONField(respBody, want.field, want.tag) {
			t.Fatalf("`SessionResp` 少了 `%s string` 且 `json:\"%s\"` 的字段："+
				"两个 id 各有各的键是「形状可分」的前提（契约冒烟按这两个键断言形状）", want.field, want.tag)
		}
	}

	// ④ 包装/解包调用点白名单。
	scanned := 0
	wraps := 0
	unwraps := 0
	for _, dir := range []string{
		filepath.Join(root, "internal"),
		filepath.Join(root, "pkg"),
	} {
		for _, path := range goFilesUnder(t, dir) {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				t.Fatalf("相对路径: %v", rerr)
			}
			relSlash := "backend/" + filepath.ToSlash(rel)
			if strings.HasPrefix(filepath.ToSlash(rel), "internal/archtest/") {
				// 跳过本包：这条守卫自己的白名单与判据字面量就写着 `WrapProvider(` /
				// `UnwrapProvider(`（与 knowledge_boundary_test.go、logging_boundary_test.go
				// 同一手法：只豁免 internal/archtest 这一个目录）。
				continue
			}
			src := stripLineComments(readFileOrFatal(t, path))
			w := strings.Count(src, "WrapProvider(")
			u := strings.Count(src, "UnwrapProvider(")
			// 定义行（`func WrapProvider(`）不算调用点。
			if strings.Contains(src, "func WrapProvider(") {
				w--
			}
			if strings.Contains(src, "func UnwrapProvider(") {
				u--
			}
			if w == 0 && u == 0 {
				continue
			}
			scanned++
			allowed, ok := providerShellCallSiteFor(relSlash)
			if !ok {
				t.Fatalf("%s 调用了 `rt_` 壳的包装/解包（wrap=%d unwrap=%d），但它不在白名单里。\n"+
					"包装/解包**各只有一个实现、且只在 §4.5.3 那几个边界点上调用**——多一处会让同一个值"+
					"在不同层有两种形态，而两种形态都合法（没有运行时信号）。要加一处，请同时更新 "+
					"providerShellCallSites 并写明理由。", relSlash, w, u)
			}
			if allowed.Wrap && w == 0 {
				t.Fatalf("白名单文件 %s 里没有 `WrapProvider(` 调用：白名单已经失去对象（映射改道了？）"+
					"——fail-closed，别让它变成空转的许可", relSlash)
			}
			if allowed.Unwrap && u == 0 {
				t.Fatalf("白名单文件 %s 里没有 `UnwrapProvider(` 调用：白名单已经失去对象（映射改道了？）",
					relSlash)
			}
			wraps += w
			unwraps += u
		}
	}
	if scanned == 0 || wraps == 0 || unwraps == 0 {
		t.Fatalf("包装/解包调用点扫不到（scanned=%d wrap=%d unwrap=%d）——fail-closed，"+
			"否则「包装点唯一」这条不变式没有任何判据", scanned, wraps, unwraps)
	}
}

// matchesJSONField 判断成员体里是否有 `Field string \`... json:"tag"...\“ 形态的字段
// （tag 允许带 `,omitempty` 之类的选项：判据是"这个键在"，不是"它没有选项"）。
func matchesJSONField(body, field, tag string) bool {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(field) + `\s+string\s+` + "`" + `[^` + "`" + `]*json:"` + regexp.QuoteMeta(tag))
	return re.MatchString(body)
}

// providerShellCallSiteFor 查白名单。
func providerShellCallSiteFor(rel string) (providerShellCallSite, bool) {
	for _, c := range providerShellCallSites {
		if strings.TrimSpace(c.Why) == "" {
			continue // 空理由不算登记
		}
		if c.Path == rel {
			return c, true
		}
	}
	return providerShellCallSite{}, false
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
	File string // 相对 internal/model
}

// platformIDEntities v15 起由 `objid` 铸号的 7 个实体（§4.2 的前缀登记表一一对应）。
var platformIDEntities = []primaryKeyEntity{
	{"SessionEntity", "session.go"},
	{"RunEntity", "run.go"},
	{"MessageEntity", "message.go"},
	{"InputEntity", "input.go"},
	{"WorkspaceEntity", "workspace.go"},
	{"ModelEntity", "model.go"},
	{"PermissionMemoryEntity", "permission_memory.go"},
}

// monotonicCursorEntities 主键是**单调游标**、刻意**不**换前缀 id 的两个实体（§1.4 非目标）：
// `events.seq` 是全局水位、`capability_revisions.seq` 直接当能力代际 rev，换 TEXT 会毁掉
// `MAX(seq)` 与索引序，收益为零。
var monotonicCursorEntities = []primaryKeyEntity{
	{"EventEntity", "event.go"},
	{"CapabilityRevisionEntity", "capability_revision.go"},
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
//  2. 把 `events.seq` / `capability_revisions.seq`"统一成前缀 id"（毁掉 `MAX(seq)` 与代际 rev）；
//  3. 把定义类的 id 也登记进 `objid` 的前缀表（用户就不能给自己的 agent 起名了，
//     而 provider 的 vault 密文会全部作废）；
//  4. `transcript_load.go` 里"未经形状校验就把 header 的 id 当平台 id"（旧数据形状复活）。
//
// fail-closed：一个目标实体都解析不到 → 红。
func TestPrimaryKeyShapesStayDeclared(t *testing.T) {
	modelDir := filepath.Join(backendRoot(t), "internal", "model")

	readEntity := func(e primaryKeyEntity) string {
		t.Helper()
		src := stripLineComments(readFileOrFatal(t, filepath.Join(modelDir, e.File)))
		return structBody(t, src, e.Name, "internal/model/"+e.File)
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
				"而 `capability_revisions.seq` 直接就是能力代际 rev。\n实际成员体：\n%s", e.Name, body)
		}
	}

	// ③ `objid` 的前缀登记表 == 7 个平台铸造前缀，**不含**任何定义类。
	objidSrc := stripLineComments(readFileOrFatal(t,
		filepath.Join(backendRoot(t), "pkg", "object", "objid", "objid.go")))
	prefixRe := regexp.MustCompile(`(?m)^\s*([A-Z]\w+)\s*=\s*"([a-z]{2,3}_)\s*"`)
	type kv struct{ name, value string }
	var got []kv
	for _, m := range prefixRe.FindAllStringSubmatch(objidSrc, -1) {
		if m[1] == "ProviderShell" {
			continue
		}
		got = append(got, kv{m[1], m[2]})
	}
	var gotNames []string
	for _, g := range got {
		gotNames = append(gotNames, g.name)
	}
	sort.Strings(gotNames)
	wantNames := []string{"Input", "Message", "Model", "PermissionMemory", "Run", "Session", "Workspace"}
	sort.Strings(wantNames)
	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("`objid` 的铸造前缀登记表变了：\n got  %v\n want %v\n"+
			"登记表 == 「平台铸造」这件事本身：把定义类（skill/mcp/agent/automation/provider）登记进来"+
			"就等于给用户自定义 id 加前缀（且 provider 的 vault 密文会全部解不开）；"+
			"少一个则那个实体的铸号会去用别的前缀。", gotNames, wantNames)
	}

	// ④ transcript 装载面：不得把 header 的 id 直接当平台 id。
	tlSrc := stripLineComments(readFileOrFatal(t,
		filepath.Join(backendRoot(t), "internal", "manager", "transcript_load.go")))
	if !strings.Contains(tlSrc, "objid.IsPlatformID(") {
		t.Fatal("`transcript_load.go` 里没有 `objid.IsPlatformID(`：旧形状的会话 id 会被直接当成平台 id" +
			"（Q10：旧数据不要 ⇒ 跳过 + WARN，不做派生；放行会让数据面出现两种会话 id 形状）")
	}
	if strings.Contains(tlSrc, "ID:      hdr.SessionID") || strings.Contains(tlSrc, "ID: hdr.SessionID") {
		t.Fatal("`transcript_load.go` 里出现了 `ID: hdr.SessionID` 直通：header 的 id 必须先过形状校验" +
			"（`hdr.SessionID` 只允许出现在校验那一处，落 rec 用校验过的变量）")
	}
}
