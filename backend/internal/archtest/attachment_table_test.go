package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// attachmentColumnSnapshot 是 `attachments` 表（v32）冻结的列集合（列名 + 类型）。
//
// G1 判据：列集合固定——**加一列不更新快照 → 红**（反证实跑见提交说明）。
// 为什么同时钉类型：`size_bytes` 若是 TEXT、`created_at` 不是 TEXT，读写两侧的字符串/数值语义
// 会各自成立、只在对账那一刻分叉（`format_test` 只测现成的列，测不到"这一列被换过类型"）。
//
// 一列一义（vocabulary.md）：`kind` 只表达投递形态（image|document），MIME 只在 `mime_type`；
// `state` 只表达生命周期（pending|linked|orphan）。
var attachmentColumnSnapshot = []struct {
	name string
	typ  string // 归一化后的大写类型前缀（TEXT / INTEGER）
}{
	{"id", "TEXT"},
	{"upload_id", "TEXT"},
	{"session_id", "TEXT"},
	{"name", "TEXT"},
	{"mime_type", "TEXT"},
	{"size_bytes", "INTEGER"},
	{"sha256", "TEXT"},
	{"kind", "TEXT"},
	{"state", "TEXT"},
	{"created_at", "TEXT"},
}

// TestAttachmentTableColumnsFrozen（G1）`attachments` 表列集合与类型冻结。
//
// fail-closed：解析不到 v32 的 `CREATE TABLE attachments (...)` 即失败（改名/挪走必须让守卫红，
// 而不是静默空转）。取**静态源码**而不是实建库读 PRAGMA——后者的红会与"迁移坏了"混在一起分不开；
// 真库侧（迁移可回滚 + 旧库升级）由 `internal/persistence/store/migrate_v32_test.go` 覆盖。
func TestAttachmentTableColumnsFrozen(t *testing.T) {
	path := filepath.Join(backendRoot(t), "internal", "persistence", "store", "migrate_v32.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", path, err)
	}
	src := stripLineComments(string(raw))

	got, ok := createTableColumns(src, "attachments")
	if !ok {
		t.Fatalf("在 %s 里解析不到 `CREATE TABLE attachments (...)`：v32 的建表语句被改名/挪走了？"+
			"（本守卫 fail-closed：解析不到即失败，避免它变成空转）", path)
	}

	var wantNames []string
	for _, c := range attachmentColumnSnapshot {
		wantNames = append(wantNames, c.name)
	}
	if strings.Join(got, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("attachments 的列集合变了（G1）：\n got  %v\n want %v\n"+
			"改列必须同批更新本快照 + model/attachment.go 的 gorm 标签 + dao 列词汇。", got, wantNames)
	}

	// 类型也钉住：从 DDL 行里取类型前缀，与快照逐列比对。
	types := createTableColumnTypes(src, "attachments")
	for _, c := range attachmentColumnSnapshot {
		if got := types[c.name]; got != c.typ {
			t.Fatalf("attachments.%s 的类型变了：got %q want %q（列类型是对外落库契约，见 format_test）",
				c.name, got, c.typ)
		}
	}

	// 实体侧：model.AttachmentEntity 的 gorm `column:` 标签必须与 DDL **逐列一致**。
	// 两处手工同步（vocabulary.md 的「四处落地点」），只查一侧另一半会静默漂移
	// （`column:` 写错编译期无感、运行期才报 no such column）。
	entityPath := filepath.Join(backendRoot(t), "internal", "persistence", "model", "attachment.go")
	entityRaw, err := os.ReadFile(entityPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v", entityPath, err)
	}
	entitySrc := stripLineComments(string(entityRaw))
	entityCols := gormColumnTags(entitySrc)
	sort.Strings(entityCols)
	wantSorted := append([]string(nil), wantNames...)
	sort.Strings(wantSorted)
	if strings.Join(entityCols, ",") != strings.Join(wantSorted, ",") {
		t.Fatalf("model.AttachmentEntity 的 gorm 列标签与 v32 DDL 不一致：\n entity %v\n ddl    %v\n"+
			"两处必须手工逐列一致（column: 标签写错运行期才报 no such column）。", entityCols, wantSorted)
	}
}

// createTableColumnTypes 从 DDL 里取 `<table>` 每列的类型前缀（大写，去掉括号与约束）。
// 例：`size_bytes INTEGER NOT NULL` → "INTEGER"；`id TEXT PRIMARY KEY` → "TEXT"。
func createTableColumnTypes(src, table string) map[string]string {
	out := map[string]string{}
	re := regexp.MustCompile(`(?s)CREATE TABLE ` + regexp.QuoteMeta(table) + `\s*\((.*?)\n\s*\)`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return out
	}
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		typ := strings.ToUpper(fields[1])
		if i := strings.IndexByte(typ, '('); i >= 0 {
			typ = typ[:i]
		}
		out[strings.ToLower(fields[0])] = typ
	}
	return out
}

// gormColumnTags 从 Go 源码里取所有 `column:<name>` 标签的列名（去重、升序）。
func gormColumnTags(src string) []string {
	re := regexp.MustCompile("column:([a-z][a-z0-9_]*)")
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}
