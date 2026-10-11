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

// conversationVocabularyWhitelist 是**允许**出现 `conversation` 的扫描点（相对仓库根路径前缀）。
//
// 白名单必须逐条写明理由（空理由同样判红）：白名单是"这次确实改不了/不该改"的登记簿，
// 不是"懒得改"的垃圾桶。任何一条新增都必须能回答"为什么它必须留着"。
var conversationVocabularyWhitelist = []struct {
	path   string
	reason string
}{
	{
		path: "backend/pkg/acl/scope.go",
		reason: "ACP `_meta` 的**第三方键** `conversationId`/`conversation_id`：那是底座发的，" +
			"改了就丢 scope（README 已登记这条解析口径）。pkg/acl 是「可复用的 ACP 客户端」，" +
			"它不该知道 ark-mate 的词汇表",
	},
	{
		path: "backend/internal/store/migrate.go",
		reason: "v1–v14 的历史迁移语句与注释里必然有 `conversations`/`conversation_id`：" +
			"迁移内核纪律是「追加迁移 = 在尾部新增，绝不回改历史」（v15 的 DDL 才有 `sessions`）",
	},
	{
		path: "backend/internal/store/migrate_v14.go",
		reason: "同上（历史步骤只用字面量）；它还带 v14 的 JSON 键 `existing_conversations`——" +
			"那是已经落进 meta.migration_notes_v14 的**持久事实**，改名等于让旧笔记读不出来",
	},
	{
		path: "backend/internal/store/migrate_test.go",
		reason: "「停在 v15 之前」的历史迁移用例要用裸 SQL 造/读 `conversations` 表" +
			"（DAO 已改认 `sessions`，读不到历史表）。这是守卫⑤ 那条纪律的测试侧镜像",
	},
	{
		path: "backend/internal/store/migrate_v15.go",
		reason: "`v15TablePlan` 里逐张登记「被重建的表」，`conversations` 正是**历史表名**——" +
			"守卫⑥ 的 R6 判据（DROP 清单 ↔ 清理清单一一对应）就靠这个字面量",
	},
	{
		path: "backend/internal/router/router_test.go",
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
// 必须原样给出，否则读者不知道要解析什么（README 已把它与 `pkg/acl/scope.go` 的处置写在同一句里）。
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
	root, err := filepath.Abs(filepath.Join("..", "..", "..")) // 仓库根
	if err != nil {
		t.Fatalf("解析仓库根失败: %v", err)
	}
	for _, must := range []string{
		filepath.Join(root, "backend", "internal"),
		filepath.Join(root, "backend", "pkg"),
		filepath.Join(root, "README.md"),
	} {
		if _, err := os.Stat(must); err != nil {
			t.Fatalf("扫描面成员不存在：%s（%v）——守卫失去对象", must, err)
		}
	}

	var files []string
	for _, dir := range []string{
		filepath.Join(root, "backend", "internal"),
		filepath.Join(root, "backend", "pkg"),
	} {
		for _, p := range goFilesUnder(t, dir) {
			rel, _ := filepath.Rel(root, p)
			if strings.HasPrefix(filepath.ToSlash(rel), "backend/internal/archtest/") {
				// 本守卫自己的禁词表就写着这个词（与 knowledge_boundary_test.go、
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
// 读 `internal/model/*.go` 用 **glob**，不写死文件名：v15 把 583 行的 `entity.go` 拆成了一表
// 一文件，钉死路径会让这条守卫静默空转（或反过来，改名后立刻失效）。
func TestModelEntitiesFollowNamingConvention(t *testing.T) {
	dir := filepath.Join(backendRoot(t), "internal", "model")
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
