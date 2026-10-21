package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件是 v15「会话词汇统一 + 表行结构体命名」的两条**收口**守卫。
//
// 为什么需要它们：这次改名是约 200 个文件 / 约 3100 处词汇 + 约 340 处类型引用的机械改动，
// 而"漏一处"的表现几乎全是**静默**的——某条 REST 路径 404、某个 json 键悄悄消失让前端拿到
// `undefined`、某张表的实体少配一个表名常量。逐处 review 挡不住这类漏，机器判据能。

// vocabularyScanFace 返回"词汇收口"类守卫共用的扫描面，并给出仓库根。
//
// 扫描面：backend 全部 Go 源码（`internal/**` + `pkg/**`，**跳过 `internal/archtest` 自身**——
// 禁词表就写着那些词）、前端**界面源码根**（`uiSrcRoots()`，搬迁后界面在 `packages/console/src`）
// + `packages/{types,api}/src`、以及 `README.md`。
//
// 为什么抽成一个函数：词汇类判据"少扫一半"是**静默**的（守卫照样绿，只是不再管那一半），
// 两条词汇守卫（conversation / capability_revision）共用一处就不会各自漂。
func vocabularyScanFace(t *testing.T) (string, []string) {
	t.Helper()
	root := filepath.Dir(backendRoot(t)) // 仓库根
	for _, must := range []string{
		filepath.Join(root, "backend", "internal"),
		filepath.Join(root, "README.md"),
	} {
		if _, err := os.Stat(must); err != nil {
			t.Fatalf("扫描面成员不存在：%s（%v）——守卫失去对象", must, err)
		}
	}

	var files []string
	for _, dir := range []string{
		filepath.Join(root, "backend", "internal"),
	} {
		for _, p := range goFilesUnder(t, dir) {
			rel, _ := filepath.Rel(root, p)
			if strings.HasPrefix(filepath.ToSlash(rel), "backend/internal/archtest/") {
				// 本类守卫自己的禁词表就写着这些词（与 knowledge_boundary_test.go、
				// logging_boundary_test.go 同一手法：只豁免这一个目录）。
				continue
			}
			files = append(files, p)
		}
	}
	for _, dir := range uiSrcRoots(t) {
		files = append(files, listFrontendSources(t, dir)...)
	}
	for _, rel := range []string{
		filepath.Join("packages", "types", "src"),
		filepath.Join("packages", "api", "src"),
	} {
		files = append(files, listFrontendSources(t, filepath.Join(frontendRoot(t), rel))...)
	}
	files = append(files, filepath.Join(root, "README.md"))
	return root, files
}

// conversationVocabularyWhitelist 是**允许**出现 `conversation` 的扫描点（相对仓库根路径前缀）。
//
// 白名单必须逐条写明理由（空理由同样判红）：白名单是"这次确实改不了/不该改"的登记簿，
// 不是"懒得改"的垃圾桶。任何一条新增都必须能回答"为什么它必须留着"。
var conversationVocabularyWhitelist = []struct {
	path   string
	reason string
}{
	{
		path: "backend/internal/adapters/outbound/acp/scope.go",
		reason: "ACP `_meta` 的**第三方键** `conversationId`/`conversation_id`：那是底座发的，" +
			"改了就丢 scope（README 已登记这条解析口径）。internal/adapters/outbound/acp 是「可复用的 ACP 客户端」，" +
			"它不该知道 ark-mate 的词汇表",
	},
	{
		path: "backend/internal/adapters/outbound/sqlite/store/migrate.go",
		reason: "v1–v14 的历史迁移语句与注释里必然有 `conversations`/`conversation_id`：" +
			"迁移内核纪律是「追加迁移 = 在尾部新增，绝不回改历史」（v15 的 DDL 才有 `sessions`）",
	},
	{
		path: "backend/internal/adapters/outbound/sqlite/store/migrate_v14.go",
		reason: "同上（历史步骤只用字面量）；它还带 v14 的 JSON 键 `existing_conversations`——" +
			"那是已经落进 meta.migration_notes_v14 的**持久事实**，改名等于让旧笔记读不出来",
	},
	{
		path: "backend/internal/adapters/outbound/sqlite/store/migrate_test.go",
		reason: "「停在 v15 之前」的历史迁移用例要用裸 SQL 造/读 `conversations` 表" +
			"（DAO 已改认 `sessions`，读不到历史表）。这是守卫⑤ 那条纪律的测试侧镜像",
	},
	{
		path: "backend/internal/adapters/outbound/sqlite/store/migrate_v15_test.go",
		reason: "同一个理由的测试侧：v15 的用例要在 **v14 库**上写夹具，而那时列名还是" +
			"`last_conversation_id` / 表名还是 `conversations`（v15 才 RENAME/重建）",
	},
	{
		path: "backend/internal/adapters/outbound/sqlite/store/migrate_v15.go",
		reason: "`v15TablePlan` 里逐张登记「被重建的表」，`conversations` 正是**历史表名**——" +
			"守卫⑥ 的 R6 判据（DROP 清单 ↔ 清理清单一一对应）就靠这个字面量",
	},
	{
		path: "backend/internal/adapters/inbound/http/router/router_test.go",
		reason: "反向断言「README 不得出现历史名 /conversations」的**判据字面量**本身——" +
			"这种守卫必须写出它禁的那个词，扫自己必然自证其罪",
	},
	{
		path: "frontend/apps/desktop/resources/compliance/",
		reason: "第三方 SBOM / THIRD-PARTY-NOTICES 里的上游包名 `dsh-client-ui-conversation`：" +
			"那是别人的包名，不是我们的词汇",
	},
}

// readmeThirdPartyMetaKeys 是 README 里**唯一**被允许出现的 `conversation` 词组：ACP `_meta`
// 的第三方键序列。
//
// 为什么只豁免这一个原样词组（而不是给 README 开一条文件级白名单）：README 是路由与 JSON 键
// 的契约文档，给整份文件开豁免等于放弃对它的检查；而这个键序列是**底座发的键**，写文档时
// 必须原样给出，否则读者不知道要解析什么（README 已把它与 `internal/adapters/outbound/acp/scope.go` 的处置写在同一句里）。
const readmeThirdPartyMetaKeys = "`sessionId`/`session_id`/`conversationId`/`conversation_id`"

// TestNoConversationVocabulary 词汇收口（T5 / B2 / R-1 的机器判据）。
//
// 扫描面：backend 全部 Go 源码（`internal/**` + `pkg/**`）、前端**界面源码根**（`uiSrcRoots()`，
// 搬迁后界面在 `packages/console/src`）+ `packages/{types,api,ui}/src`、以及 `README.md`。
//
// 判据：剥掉注释后不得出现 `conversation`（大小写不敏感）。剥注释是有意的——注释里解释
// "v15 之前这里叫 conversation_id"是**解释**，不是残留词汇。
//
// fail-closed：扫描面解析不到（目录/文件缺失、命中的文件数为 0）即失败——否则改名结束后
// 这条守卫会静默空转，而"空转的守卫"比没有守卫更坏（它给出虚假的安心）。
func TestNoConversationVocabulary(t *testing.T) {
	root, files := vocabularyScanFace(t)

	if len(files) < 100 {
		t.Fatalf("扫描面只拿到 %d 个文件——路径写错了？（fail-closed：解析不到即失败）", len(files))
	}

	scanned := 0
	var hits []string
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("相对路径: %v", err)
		}
		relSlash := filepath.ToSlash(rel)
		if allowedByVocabularyWhitelist(t, relSlash) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", rel, err)
		}
		src := string(raw)
		switch {
		case strings.HasSuffix(path, ".go"):
			src = stripLineComments(src)
		case strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".mjs"):
			src = stripTSComments(src)
		default:
			src = stripTSComments(src)
		}
		if strings.HasSuffix(path, "README.md") {
			src = strings.ReplaceAll(src, readmeThirdPartyMetaKeys, "")
		}
		scanned++
		if idx := strings.Index(strings.ToLower(src), "conversation"); idx >= 0 {
			// 报告行号（截断到命中行）而不是整个文件，便于阅读。
			line := 1 + strings.Count(src[:idx], "\n")
			hits = append(hits, relSlash+":"+itoa(line))
		}
	}
	if scanned == 0 {
		t.Fatal("一个文件都没扫到（fail-closed）")
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		t.Fatalf("v15 起「一个东西一个词」是 `session`：以下位置剥注释后仍出现 `conversation`\n"+
			"（确需保留的，登记进 conversationVocabularyWhitelist **并写明理由**；空理由同样红）\n  %s",
			strings.Join(hits, "\n  "))
	}
}

// allowedByVocabularyWhitelist 命中白名单即跳过（前缀匹配，便于豁免整棵目录）。
func allowedByVocabularyWhitelist(t *testing.T, rel string) bool {
	t.Helper()
	for _, w := range conversationVocabularyWhitelist {
		if strings.TrimSpace(w.reason) == "" {
			t.Fatalf("白名单 %q 的理由为空——白名单是登记簿，不是垃圾桶", w.path)
		}
		if strings.HasPrefix(rel, w.path) {
			return true
		}
	}
	return false
}

// capabilityRevisionVocabularyWhitelist 是**允许**出现旧能力审计词汇的扫描点（相对仓库根前缀）。
//
// 与 conversation 的白名单同一纪律：逐条写明理由，空理由同样红。
var capabilityRevisionVocabularyWhitelist = []struct {
	path   string
	reason string
}{
	{
		path: "backend/internal/adapters/outbound/sqlite/store/migrate.go",
		reason: "v8 的建表语句与 v16 的 `RENAME TO`/`RENAME COLUMN` 必须写**当时那一版**的名字" +
			"（`capability_revisions` / `capability_id` / `idx_capability_revisions_capability`）：" +
			"迁移内核纪律是「追加迁移 = 在尾部新增，绝不回改历史」，而 v16 正是把老名字改成新名字的那一步" +
			"——它当然得写出老名字（v15 的 `conversations` 同理，见 conversation 白名单）",
	},
	{
		path: "backend/internal/adapters/outbound/sqlite/store/migrate_test.go",
		reason: "「停在 v16 之前」的历史迁移用例要用裸 SQL 造/读 `capability_revisions` 表" +
			"（DAO 已改认 `definition_revisions`，读不到历史表）。这是守卫⑤ 那条纪律的测试侧镜像" +
			"（同 `readLegacyConversation`）",
	},
	{
		path: "backend/internal/adapters/outbound/sqlite/store/migrate_v16_test.go",
		reason: "v16 自己的迁移用例：夹具必须按**当时那一版**（v15）的表名与列名写裸 SQL" +
			"（`capability_revisions.capability_id`），再断言 v16 之后它们变成了新名字且**行没被改坏**" +
			"——改用新名字写夹具的话，这条用例就只是在验证「新表能存东西」，对迁移本身零覆盖",
	},
}

// legacyCapabilityVocabulary 是 v16 起禁止的旧词（**小写化后**做子串匹配）。
//
// 三项分别对应三种漏法：`capability_revision`（表名与 Go 类型前缀）、`capability_id`（列名，
// 只在这里带下划线——`CapabilityID` 是**另一个东西**：运行期能力绑定 id，见
// `capability_selection.go`，刻意不误伤）、`capabilityrevision`（驼峰写法）。
var legacyCapabilityVocabulary = []string{"capability_revision", "capability_id", "capabilityrevision"}

// TestNoCapabilityRevisionVocabulary 词汇收口（v16 / 设计 D3 的机器判据）。
//
// v16 把「定义变更审计」的表名与列名从 `capability_revisions` / `capability_id` 改成了
// `definition_revisions` / `definition_id`，理由是**`capability` 在本仓是重载词**：它既指
// 运行期声明的能力集（`capability_json`、`CapabilityScope`、`internal/platform/capability` = skill + mcp），
// 又指这张审计表覆盖的**六种定义**（skill / mcp / agent / automation / provider / model，
// 后四者与"运行期能力"无关）。判据与 v15 的 conversation 同形：**零命中**是可以机器判据的。
//
// 剥注释是有意的——注释里写"v16 之前它叫 `capability_revisions`"是**解释**，不是残留词汇。
//
// fail-closed：扫描面解析不到（文件数为 0）即失败，否则改名结束后这条守卫会静默空转。
func TestNoCapabilityRevisionVocabulary(t *testing.T) {
	root, files := vocabularyScanFace(t)
	if len(files) < 100 {
		t.Fatalf("扫描面只拿到 %d 个文件——路径写错了？（fail-closed：解析不到即失败）", len(files))
	}

	scanned := 0
	var hits []string
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("相对路径: %v", err)
		}
		relSlash := filepath.ToSlash(rel)
		if allowedByCapabilityRevisionWhitelist(t, relSlash) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", rel, err)
		}
		src := string(raw)
		if strings.HasSuffix(path, ".go") {
			src = stripLineComments(src)
		} else {
			src = stripTSComments(src)
		}
		scanned++
		lower := strings.ToLower(src)
		for _, banned := range legacyCapabilityVocabulary {
			if idx := strings.Index(lower, banned); idx >= 0 {
				line := 1 + strings.Count(src[:idx], "\n")
				hits = append(hits, relSlash+":"+itoa(line)+" 命中 "+banned)
				break
			}
		}
	}
	if scanned == 0 {
		t.Fatal("一个文件都没扫到（fail-closed）")
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		t.Fatalf("v16 起定义变更审计只有一个词：`definition_revisions` / `definition_id`。\n"+
			"以下位置剥注释后仍出现旧词（确需保留的，登记进 capabilityRevisionVocabularyWhitelist"+
			" **并写明理由**；空理由同样红）\n  %s", strings.Join(hits, "\n  "))
	}
}

// allowedByCapabilityRevisionWhitelist 命中白名单即跳过（前缀匹配，便于豁免整棵目录）。
func allowedByCapabilityRevisionWhitelist(t *testing.T, rel string) bool {
	t.Helper()
	for _, w := range capabilityRevisionVocabularyWhitelist {
		if strings.TrimSpace(w.reason) == "" {
			t.Fatalf("白名单 %q 的理由为空——白名单是登记簿，不是垃圾桶", w.path)
		}
		if strings.HasPrefix(rel, w.path) {
			return true
		}
	}
	return false
}

// providerSessionVocabularyWhitelist 是**允许**出现 `provider_session` 旧词的扫描点。
//
// 只有两个：历史迁移语句与它的历史夹具（同 conversation / capability_revision 的理由）。
var providerSessionVocabularyWhitelist = []struct {
	path   string
	reason string
}{
	{
		path: "backend/internal/adapters/outbound/sqlite/store/migrate.go",
		reason: "v9 的建列语句与 v17 的 `RENAME COLUMN provider_session_id TO runtime_session_id`" +
			"必须写**当时那一版**的列名（迁移内核纪律：追加迁移 = 在尾部新增，绝不回改历史；" +
			"而 v17 正是把老名字改成新名字的那一步——它当然得写出老名字）",
	},
	{
		path: "backend/internal/adapters/outbound/sqlite/store/migrate_test.go",
		reason: "「停在 v15 之前」的历史迁移用例要用裸 SQL 造/读 `conversations.provider_session_id`" +
			"（DAO 已改认 `runtime_session_id`，读不到历史列）。这是守卫⑤ 那条纪律的测试侧镜像",
	},
}

// legacyProviderSessionVocabulary 是 v17 起禁止的旧词（**小写化后**做子串匹配）。
//
// 两项覆盖两种写法：`provider_session`（列名 / json 键 / 日志键 / 路径段）与 `providersession`
// （驼峰 `ProviderSessionID`，小写化后命中）。**刻意不含** `provider` 单独一词——它是本仓
// 的「模型供应商」（`providers` 表、`ProviderID`），与底座会话无关，误伤面极大。
var legacyProviderSessionVocabulary = []string{"provider_session", "providersession"}

// TestNoProviderSessionVocabulary 词汇收口（v17 / 设计 J1 的机器判据）。
//
// v17 把「runtime 侧的 ACP 会话」这个概念的落点从 `provider_session_id` 改成
// `runtime_session_id`（连带 `SessionRec.ProviderSessionID` → `RuntimeSessionID`、DTO 的
// `provider_session_id` → `runtime_session_id`、日志键 `provider_session` → `runtime_session`）。
// 理由：`provider` 在本仓的主流含义是**模型供应商**（providers 表 / ProviderID / provider 档案），
// 而这一列存的是"底座那边的会话 id"——同一个词指两个东西，正是「一个东西一个词」要消灭的。
//
// 判据是**零命中**（剥注释后，小写化子串匹配），因为一次机械改名漏一处的表现全是静默的：
// 前端读 `c.provider_session_id` 拿到 `undefined`、日志键对不上、路由段成了路径参数。
//
// 剥注释是有意的——注释里写"v17 之前它叫 provider_session_id"是**解释**，不是残留词汇。
//
// fail-closed：扫描面解析不到（文件数为 0）即失败。
func TestNoProviderSessionVocabulary(t *testing.T) {
	root, files := vocabularyScanFace(t)
	if len(files) < 100 {
		t.Fatalf("扫描面只拿到 %d 个文件——路径写错了？（fail-closed：解析不到即失败）", len(files))
	}

	scanned := 0
	var hits []string
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("相对路径: %v", err)
		}
		relSlash := filepath.ToSlash(rel)
		if allowedByProviderSessionWhitelist(t, relSlash) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", rel, err)
		}
		src := string(raw)
		if strings.HasSuffix(path, ".go") {
			src = stripLineComments(src)
		} else {
			src = stripTSComments(src)
		}
		scanned++
		lower := strings.ToLower(src)
		for _, banned := range legacyProviderSessionVocabulary {
			if idx := strings.Index(lower, banned); idx >= 0 {
				line := 1 + strings.Count(src[:idx], "\n")
				hits = append(hits, relSlash+":"+itoa(line)+" 命中 "+banned)
				break
			}
		}
	}
	if scanned == 0 {
		t.Fatal("一个文件都没扫到（fail-closed）")
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		t.Fatalf("v17 起「runtime 侧的 ACP 会话」只有一个词：`runtime_session_id`。\n"+
			"以下位置剥注释后仍出现旧词（确需保留的，登记进 providerSessionVocabularyWhitelist"+
			" **并写明理由**；空理由同样红）\n  %s", strings.Join(hits, "\n  "))
	}
}

// allowedByProviderSessionWhitelist 命中白名单即跳过（前缀匹配，便于豁免整棵目录）。
func allowedByProviderSessionWhitelist(t *testing.T, rel string) bool {
	t.Helper()
	for _, w := range providerSessionVocabularyWhitelist {
		if strings.TrimSpace(w.reason) == "" {
			t.Fatalf("白名单 %q 的理由为空——白名单是登记簿，不是垃圾桶", w.path)
		}
		if strings.HasPrefix(rel, w.path) {
			return true
		}
	}
	return false
}

// itoa 极简整数转字符串（避免为一行日志引入 strconv 之外的东西——这里用 strconv 也行，
// 但保持本文件的依赖面最小）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestModelEntitiesFollowNamingConvention 表行结构体的后缀与配对（T6 / §3.3.2 的机器判据）。
//
// 三条规则，全部双向：
//   - R1 每个 `func (X) TableName() string` 的 `X` 必须以 `Entity` 结尾，且包内存在
//     `type <X>List []<X>`；
//   - R2 方法体必须是 `return TableName<域词>`（不许写字面量），且每个 `TableName<域词>` 常量
//     至少被一个 `TableName()` 引用（抓"加了常量忘了实体"与"加了实体忘了常量"两个方向）；
//   - R3 实体名与常量名的**域词必须相等**（`SessionEntity` ↔ `TableNameSession`）。
//
// 读 `internal/adapters/outbound/sqlite/model/*.go` 用 **glob**，不写死文件名：v15 把 583 行的 `entity.go` 拆成了一表
// 一文件，钉死路径会让这条守卫静默空转（或反过来，改名后立刻失效）。
func TestModelEntitiesFollowNamingConvention(t *testing.T) {
	dir := filepath.Join(backendRoot(t), "internal", "adapters", "outbound", "sqlite", "model")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("扫不到 %s/*.go: %v（fail-closed）", dir, err)
	}

	type entity struct {
		name   string
		domain string
		file   string
	}
	entities := map[string]entity{}
	consts := map[string]string{} // 常量名 → 域词

	tableNameCall := regexp.MustCompile(`func \((\w+)\) TableName\(\) string \{\s*return (\w+)\s*\}`)
	constDecl := regexp.MustCompile(`(?m)^\s*(TableName[A-Z]\w*)\s*=\s*"[^"]*"`)
	listDecl := regexp.MustCompile(`(?m)^type (\w+EntityList) \[\](\w+)`)

	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", path, err)
		}
		src := stripLineComments(string(raw))
		for _, m := range constDecl.FindAllStringSubmatch(src, -1) {
			consts[m[1]] = strings.TrimPrefix(m[1], "TableName")
		}
		for _, m := range tableNameCall.FindAllStringSubmatch(src, -1) {
			recv, ret := m[1], m[2]
			if !strings.HasSuffix(recv, "Entity") {
				t.Fatalf("%s 的 TableName 接收者 %q 不以 `Entity` 结尾——"+
					"表行结构体一律 `<域词>Entity`（v15 / §3.3.2 R1）", filepath.Base(path), recv)
			}
			if !strings.HasPrefix(ret, "TableName") {
				t.Fatalf("%s 的 %s.TableName() 返回 %q（字面量？）——"+
					"表名必须经 `TableName<域词>` 常量，不许在方法体里写字面量（R2）", filepath.Base(path), recv, ret)
			}
			entities[recv] = entity{name: recv, domain: strings.TrimSuffix(recv, "Entity"), file: filepath.Base(path)}
			if _, ok := consts[ret]; !ok {
				t.Fatalf("%s 的 %s.TableName() 返回了未声明的常量 %q", filepath.Base(path), recv, ret)
			}
			if want := "TableName" + strings.TrimSuffix(recv, "Entity"); ret != want {
				t.Fatalf("实体 %s 配到了 %s，期望 %s（实体名与常量名的域词必须相等，R3）", recv, ret, want)
			}
		}
	}

	if len(entities) == 0 {
		t.Fatal("一个 `func (X) TableName() string` 都没解析到（fail-closed：改名/搬迁后不许静默空转）")
	}
	if len(consts) < len(entities) {
		t.Fatalf("表名常量（%d）少于实体（%d）——有实体没配常量", len(consts), len(entities))
	}

	// R1 的另一半：每个实体都要有 `<X>List []<X>`。
	listTypes := map[string]string{} // list 名 → 元素类型
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", path, err)
		}
		for _, m := range listDecl.FindAllStringSubmatch(stripLineComments(string(raw)), -1) {
			listTypes[m[1]] = m[2]
		}
	}
	for name := range entities {
		want := name + "List"
		elem, ok := listTypes[want]
		if !ok {
			t.Fatalf("实体 %s 缺 `type %s []%s`（gormdao 泛型参数要求具名切片，R1）", name, want, name)
		}
		if elem != name {
			t.Fatalf("%s 的元素类型是 %s，期望 %s", want, elem, name)
		}
	}

	// R2 反向：每个 TableName<域词> 常量都必须至少被一个 TableName() 引用。
	used := map[string]bool{}
	for _, e := range entities {
		used["TableName"+e.domain] = true
	}
	for c := range consts {
		if !used[c] {
			t.Fatalf("表名常量 %s 没有任何 TableName() 引用它——"+
				"加了表却没人用 / 加了常量忘了实体（R2 的反向）", c)
		}
	}

	// R3 反向：域词相同还不够，实体还得恰好对应一张表——即实体数 == 常量数。
	if len(entities) != len(consts) {
		t.Fatalf("实体数 %d ≠ 表名常量数 %d（一一对应）", len(entities), len(consts))
	}
}

// TestModelsVocabularyIsModelNotModelID 守卫⑤（v24 / 计划 §4.M1.T4）。
//
// `models.model_id` 在本仓是**一词三义**的一半（另一半是 `models.id`）：前者是"发给上游的模型名"，
// 后者是"平台铸造的行身份（`mdl_<ULID>`）"——两个都带 `id` 又都不是同一个东西，正是用户
// 2026-10-15 那句「`models` 中 `id` 和 `model_id` 两个字段也很有歧义」。v24 把前者改名 `model`
// （定案 §3 辛），此后 `_id` 一律指"指向某表 `id`"。
//
// 四面 + 一反向，全部 fail-closed（解析不到即失败，避免改名/搬迁后守卫静默空转）：
//
//	① **真 schema**：跑完整迁移的库里 `models` 有 `model`、没有 `model_id`；
//	② **DTO**：`internal/contract/dtomodel/model_*.go` 剥注释后零 `model_id`（线上键名唯一事实源）；
//	③ **前端类型**：`packages/types/src/model.ts` 剥注释后零 `model_id`；
//	④ **实体 gorm 标签**：`internal/adapters/outbound/sqlite/model/model.go` 必须是 `column:model`——标签写错**编译期无感**，
//	   只有运行时那条 SQL 才报 `no such column`（v16 的 A1 守卫注释里写着同一件事）；
//	⑤ **反向**（防"改过头"）：`sessions` **必须仍有** `model_id` 列——E8 的陷阱变成机器判据。
//
// 为什么不做全仓 grep：`model_id` 在**会话侧**是保留词（`sessions.model_id` → `models.id`，
// 它名列其名），在**历史迁移**里是必须写出的当时那一版名字。判据按**面**限定（真 schema + DTO +
// 前端类型 + 一个实体文件），而不是全仓子串——后者要么假红，要么得维护一张长长的白名单。
func TestModelsVocabularyIsModelNotModelID(t *testing.T) {
	// ① 真 schema。
	db := migratedSchemaDB(t)
	cols := map[string]bool{}
	rows, err := db.Raw(`PRAGMA table_info(models)`).Rows()
	if err != nil {
		t.Fatalf("PRAGMA table_info(models): %v", err)
	}
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             *string
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("扫 models 的列: %v", err)
		}
		cols[name] = true
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("关 models 的列游标: %v", err)
	}
	if len(cols) == 0 {
		t.Fatal("models 一列都没读到（fail-closed：真 schema 是这条守卫的根）")
	}
	if !cols["model"] {
		t.Fatal("v24 起 `models` 必须有 `model` 列（发给上游的模型名）")
	}
	if cols["model_id"] {
		t.Fatal("v24 起 `models.model_id` 必须已改名 `model`——它仍然在，说明改名没落地" +
			"（或有人把它加了回来）")
	}

	// ② DTO：**平台清单那一面**的线上键名。
	//
	// 只扫 `list.go`（平台清单的对外面）而不是整个包：同一个包里还有 `response.go` 的**运行时目录面**
	// （`ConfigOptionValueResp.ModelID` 是"这个候选对应平台哪一行"的**注解**，wire 键就叫
	// `model_id`，v26 加的）——那是第三个含义、也是**正确**的那个名字（它真的指向 `models.id`）。
	// 判据按**面**限定：这里问的是"平台清单行里那个'发给上游的模型名'键叫什么"。
	listPath := filepath.Join(backendRoot(t), "internal", "contract", "dtomodel", "model_list.go")
	raw, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（fail-closed：平台清单的线上键名是这条守卫的一半）", listPath, err)
	}
	if idx := strings.Index(strings.ToLower(stripLineComments(string(raw))), "model_id"); idx >= 0 {
		t.Fatalf("%s 剥注释后仍出现 `model_id`——平台清单行的上游模型名键一律 `model`（v24）", listPath)
	}
	// 反向：目录面的注解必须还在（它消失时"候选 → 平台行"这条链就断了，而前端会静默地
	// 让所有候选都变成"平台未声明"）。
	catPath := filepath.Join(backendRoot(t), "internal", "contract", "dtomodel", "model_catalog.go")
	raw, err = os.ReadFile(catPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（fail-closed）", catPath, err)
	}
	if !strings.Contains(string(raw), "`json:\"model_id,omitempty\"`") {
		t.Fatalf("%s 里找不到 `json:\"model_id,omitempty\"`：目录候选的**平台行注解**是 v26 的契约，"+
			"没了它前端只能把所有候选标成「平台未声明」", catPath)
	}

	// ③ 前端类型（fail-closed：文件不在即失败，避免"改名后路径变了"静默空转）。
	tsPath := filepath.Join(frontendRoot(t), "packages", "types", "src", "model.ts")
	raw, err = os.ReadFile(tsPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（fail-closed：前端类型面是这条守卫的一半）", tsPath, err)
	}
	tsSrc := stripTSComments(string(raw))
	// 只判**平台清单**那两个接口（`ModelRow` / `ModelCreateReq`）的成员，不判整个文件：
	// 同一个文件里还有目录候选的 `ConfigOptionValue.model_id`——那是 v26 的**注解**
	// （候选 → 平台行），它真的指向 `models.id`，名字是对的。判据按**面**限定。
	for _, name := range []string{"ModelRow", "ModelCreateReq"} {
		body, ok := tsInterfaceBody(tsSrc, name)
		if !ok {
			t.Fatalf("在 %s 里解析不出 `export interface %s {…}` 的成员体"+
				"（fail-closed：退化成「整文件扫」或「什么都不扫」都会让这条守卫失效）", tsPath, name)
		}
		if idx := strings.Index(strings.ToLower(body), "model_id"); idx >= 0 {
			t.Fatalf("%s 的 %s 剥注释后仍出现 `model_id`——平台清单那一面的线上键一律 `model`（v24）",
				tsPath, name)
		}
		if !strings.Contains(body, "model") {
			t.Fatalf("%s 的 %s 里找不到 `model` 成员：抽取器读空了（fail-closed）", tsPath, name)
		}
	}
	// 反向：目录候选的注解必须还在（它消失时前端会把所有候选静默标成"平台未声明"）。
	if body, ok := tsInterfaceBody(tsSrc, "ConfigOptionValue"); !ok || !strings.Contains(body, "model_id") {
		t.Fatalf("%s 的 ConfigOptionValue 里找不到 `model_id` 注解（v26 契约）："+
			"没了它前端只能把所有候选标成「平台未声明」", tsPath)
	}

	// ④ 实体 gorm 标签。
	entityPath := filepath.Join(backendRoot(t), "internal", "adapters", "outbound", "sqlite", "model", "model.go")
	raw, err = os.ReadFile(entityPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（fail-closed）", entityPath, err)
	}
	entitySrc := stripLineComments(string(raw))
	if !strings.Contains(entitySrc, "column:model;") {
		t.Fatalf("%s 里找不到 `column:model` 标签——gorm 标签与 DDL 不同步是**编译期无感**的，"+
			"只有运行时那条 SQL 才报 `no such column`", entityPath)
	}
	if strings.Contains(entitySrc, "column:model_id") {
		t.Fatalf("%s 里仍有 `column:model_id`——实体标签必须跟到 `model`（v24）", entityPath)
	}

	// ⑤ 反向：会话侧那一列**必须还在**（E8：辰之后它的名字才是对的）。
	var sessionModelID int64
	if err := db.Raw(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='model_id'`).
		Row().Scan(&sessionModelID); err != nil {
		t.Fatalf("读 sessions 列集合: %v", err)
	}
	if sessionModelID != 1 {
		t.Fatal("`sessions.model_id` 必须仍在：它指的是 `models.id`（v24 之后这个名字才是对的）。" +
			"把它一起改名 = 把「改会话语义」与「改列名」混在一起（计划 E8）")
	}
}

// tsInterfaceBody 抽出 `export interface <name> { … }` 的成员体（**不含**花括号本身）。
//
// 为什么需要一个抽取器：词汇类判据"扫整个文件"会把同一个文件里**另一个面**的合法字段一起判红
// （`packages/types/src/model.ts` 同时装着平台清单面与运行时目录面，两面的 `model_id` 含义不同）。
// 按接口名取成员体是"按面限定"的最小实现，且**解析不到即失败**（调用方 fail-closed）——
// 改写法（比如换成 `type X = {…}`）时必须有人回来把这行判据对齐，而不是让它静默空转。
//
// 收尾用行首的 `}`（本仓的接口一律这样排版）；成员体为空也返回 ok（那由调用方另行判空）。
func tsInterfaceBody(src, name string) (string, bool) {
	re := regexp.MustCompile(`(?m)^export interface ` + regexp.QuoteMeta(name) + `\s*\{([\s\S]*?)^\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return "", false
	}
	return m[1], true
}
