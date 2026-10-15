package archtest

import (
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
// 换掉 `provider` 这个词是因为它在「runtime 侧的 ACP 会话」上一直是个内部术语（见 AGENTS.md 消歧表）。

// runtimeShellCallSite 是一个允许调用 `WrapRuntime`/`UnwrapRuntime` 的文件
// （设计 §4.5.3 的 4 包装 / 3 解包表 —— 这是"包装点唯一"这条不变式的机器判据）。
type runtimeShellCallSite struct {
	Path string
	// Wrap / Unwrap 该文件**必须**出现哪一个（fail-closed：白名单文件里两个都不出现
	// ⇒ 映射改道了，白名单已经失去对象）。
	Wrap   bool
	Unwrap bool
	Why    string
}

// runtimeShellCallSites 是唯一允许出现 `rt_` 壳包装/解包的文件。
//
// 多一处即红——它挡的是"顺手在别处也包一层/解一层"：那会让同一个值在不同层有两种形态，
// 而两种形态**都合法**（一个平台 id 也能被 wrap 成 `rt_ses_…`），于是没有任何运行时信号。
var runtimeShellCallSites = []runtimeShellCallSite{
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
		Why: "D-4 修法：wire `_meta` 里的底座 id 要先按 `runtime_session_id` 反查平台会话（包装后比对）",
	},
	{
		Path: "backend/pkg/object/objid/objid_test.go", Wrap: true, Unwrap: true,
		Why: "唯一实现的单测（含三条错误路径）",
	},
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
				// 跳过本包：这条守卫自己的白名单与判据字面量就写着 `WrapRuntime(` /
				// `UnwrapRuntime(`（与 knowledge_boundary_test.go、logging_boundary_test.go
				// 同一手法：只豁免 internal/archtest 这一个目录）。
				continue
			}
			src := stripLineComments(readFileOrFatal(t, path))
			w := strings.Count(src, "WrapRuntime(")
			u := strings.Count(src, "UnwrapRuntime(")
			// 定义行（`func WrapRuntime(`）不算调用点。
			if strings.Contains(src, "func WrapRuntime(") {
				w--
			}
			if strings.Contains(src, "func UnwrapRuntime(") {
				u--
			}
			if w == 0 && u == 0 {
				continue
			}
			scanned++
			allowed, ok := runtimeShellCallSiteFor(relSlash)
			if !ok {
				t.Fatalf("%s 调用了 `rt_` 壳的包装/解包（wrap=%d unwrap=%d），但它不在白名单里。\n"+
					"包装/解包**各只有一个实现、且只在 §4.5.3 那几个边界点上调用**——多一处会让同一个值"+
					"在不同层有两种形态，而两种形态都合法（没有运行时信号）。要加一处，请同时更新 "+
					"runtimeShellCallSites 并写明理由。", relSlash, w, u)
			}
			if allowed.Wrap && w == 0 {
				t.Fatalf("白名单文件 %s 里没有 `WrapRuntime(` 调用：白名单已经失去对象（映射改道了？）"+
					"——fail-closed，别让它变成空转的许可", relSlash)
			}
			if allowed.Unwrap && u == 0 {
				t.Fatalf("白名单文件 %s 里没有 `UnwrapRuntime(` 调用：白名单已经失去对象（映射改道了？）",
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

// runtimeShellCallSiteFor 查白名单。
func runtimeShellCallSiteFor(rel string) (runtimeShellCallSite, bool) {
	for _, c := range runtimeShellCallSites {
		if strings.TrimSpace(c.Why) == "" {
			continue // 空理由不算登记
		}
		if c.Path == rel {
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
	File string // 相对 internal/model
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
				"而 `definition_revisions.seq` 直接就是能力代际 rev。\n实际成员体：\n%s", e.Name, body)
		}
	}

	// ③ `objid` 的前缀登记表 == 8 个平台铸造前缀，**不含**任何定义类。
	objidSrc := stripLineComments(readFileOrFatal(t,
		filepath.Join(backendRoot(t), "pkg", "object", "objid", "objid.go")))
	prefixRe := regexp.MustCompile(`(?m)^\s*([A-Z]\w+)\s*=\s*"([a-z]{2,3}_)\s*"`)
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
	wantNames := []string{"Event", "Input", "Message", "Model", "PermissionMemory", "Run", "Session", "Workspace"}
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
	evSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "model", "event.go")))
	body := structBody(t, evSrc, "EventEntity", "internal/model/event.go")
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

	// ③ DAO：事件查询只按 `seq` 排序/翻页，不得引入 `id` 序。
	daoSrc := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "dao", "event.go")))
	// `id > ?` 用"前一个字符不是标识符字符"锚定：复合键集比较里的 `session_id > ?` 含有子串
	// `id > ?`，用裸 Contains 会把它误判成"按事件身份比较"（v21 的 AllBySession 就是这么被误报的）。
	idCompare := regexp.MustCompile(`(?:^|[^A-Za-z0-9_"'])id\s*>\s*\?`)
	for _, bad := range []string{"ORDER BY id", "Order(\"id\"", "Order(\"id "} {
		if strings.Contains(daoSrc, bad) {
			t.Fatalf("`internal/dao/event.go` 里出现了按 `id` 排序的写法 %q：事件的身份**不参与排序**"+
				"（位次的权威只有 `seq`；铸号顺序恰好同序会让这类错误在很长时间里看不出来）", bad)
		}
	}
	if idCompare.MatchString(daoSrc) {
		t.Fatal("`internal/dao/event.go` 里出现了按 `id` 比较的写法（`id > ?`）：事件的身份**不参与比较**" +
			"——跨会话翻页用的是复合键 `(session_id, seq)`，而 `session_id > ?` 是它的一部分，" +
			"别把两者混起来")
	}
	if !strings.Contains(daoSrc, "Order(\"seq") && !strings.Contains(daoSrc, "Order(\"session_id ASC, seq ASC\"") {
		t.Fatal("`internal/dao/event.go` 里找不到按 `seq` 的排序：事件游标只认 seq（fail-closed，" +
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
// fail-closed：任何一处解析不到即红。
func TestEventIdentityTravelsWithData(t *testing.T) {
	root := backendRoot(t)
	read := func(rel ...string) string {
		t.Helper()
		return stripLineComments(readFileOrFatal(t, filepath.Join(append([]string{root}, rel...)...)))
	}
	cases := []struct {
		rel  string
		want string // 正则
		why  string
	}{
		{"internal/manager/publish.go", `objid\.New\(objid\.Event\)`,
			"运行时**唯一的**事件铸号点（其余路径只搬运）：少了它，事件就没有身份"},
		{"internal/manager/publish.go", `AppendEvent\(context\.Background\(\), evtID,`,
			"① 落库必须带上刚铸的那支 id（不重铸）"},
		{"internal/manager/publish.go", `PublishAt\(sessionID, seq, evtID,`,
			"① 帧上也必须是同一支 id（`PublishAt` 的第三参数；v21 起第一位是聚合 id）"},
		{"internal/manager/publish.go", `eventbus\.Event\{ID: evtID,`,
			"① nil-bus（内存模式无 bus）分支同样带 id"},
		{"internal/manager/publish.go", `publishSideEffects\(sessionID, evt\.ID,`,
			"② 副作用（transcript）拿的是**已发布事件**的 id，不是再铸一支"},
		{"internal/manager/publish.go", `appendLine\(sessionID, eventID,`,
			"② transcript 落盘的 id 来自上游（本函数不再铸号）"},
		{"internal/manager/transcript.go", `wireTranscriptLine\{ID: eventID,`,
			"② v2 事件行的第一个字段就是身份"},
		{"internal/manager/transcript_load.go", `PublishAt\(p\.sessionID, p\.line\.seq, p\.line\.id,`,
			"③ memory 模式装载：transcript 的 id/seq 原样注入 bus（按会话分组）"},
		{"internal/manager/transcript_load.go", `id: ev\.ID, seq: ev\.Seq,`,
			"③ 事件行必须先过形状校验再落进内存行"},
		{"internal/manager/persist.go", `PublishAt\(ev\.SessionID, ev\.Seq, ev\.ID,`,
			"④ sqlite 模式恢复：库里的 id 原样注入 bus"},
		{"internal/manager/export.go", `ID: e\.ID, Type: e\.Type,`,
			"⑤ REST 导出的事件带上 id"},
		{"internal/manager/sessions.go", `ID: e\.ID, Type: e\.Type,`,
			"⑤ 会话历史的读模型带上 id"},
		{"internal/app/command/migrate.go", `model\.EventEntity\{ID: l\.ID,`,
			"⑥ 导入把 transcript 的 id 交给 store（缺 id 时前面已 fail-loud）"},
		{"internal/eventbus/bus.go", `Event\{ID: id,`,
			"帧结构体必须真的把 id 放进去（omitempty 只对「没有身份」的事件生效）"},
		{"internal/manager/automation_sched.go", `Publish\(\s*""\s*,`,
			"⑦ 只走实时通道的事件刻意不带身份（空串），「帧上有 id ⟺ durable 日志里查得到」才成立"},
	}
	for _, c := range cases {
		src := read(strings.Split(c.rel, "/")...)
		if !regexp.MustCompile(c.want).MatchString(src) {
			t.Fatalf("%s 里找不到 %q —— %s\n"+
				"（J4：身份必须随数据走到**每一个**载体；少一处，那一处拿到的东西仍然「形状合法」，"+
				"只是指向别的事件或什么都指不到）", c.rel, c.want, c.why)
		}
	}

	// ⑧ 导入判重/分叉：同一支 id 第二次导入必须**逐字节比对内容**再决定跳过还是报错。
	impSrc := read("internal", "store", "import.go")
	if !strings.Contains(impSrc, "FindByID(") {
		t.Fatal("`internal/store/import.go` 里找不到 `FindByID(`：导入不做事件级判重，" +
			"重复导入要么写第二份同 id 的行（主键冲突）要么整批失败——两种都不是幂等")
	}
	if !strings.Contains(impSrc, "分叉") {
		t.Fatal("`internal/store/import.go` 里找不到「分叉」这条错误：" +
			"同 id 不同内容必须**报错**而不是静默跳过（静默跳过 = 库里留着两份同 id 不同内容的行）")
	}
}
