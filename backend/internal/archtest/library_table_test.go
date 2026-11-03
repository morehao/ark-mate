package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ——— 资料库目录树三表（v33）的列集合冻结（G1）———
//
// 为什么钉：`library_nodes` / `library_attachments` / `library_tables` 的列是**对外落库契约**
// （读面按列装配、用例层按列读写）。加一列不更新快照 → 红；改类型不更新 → 红。
// 同时校验实体 `model/library_*.go` 的 gorm `column:` 标签与 DDL 逐列一致（两处手工同步，
// 只查一侧另一半会静默漂移：`column:` 写错编译期无感、运行期才报 no such column）。
//
// 真库侧（迁移可回滚 + 旧库升级）由 `internal/persistence/store/migrate_v33_test.go` 覆盖。

type colType struct {
	name string
	typ  string
}

// v34AlterTypes 是 v34 `ALTER TABLE ... ADD COLUMN` 追加列的类型（library_nodes 的 category/provenance_id）。
// 这些列不在 v33 的 CREATE TABLE 里，类型校验时从这张表取。
var v34AlterTypes = map[string]string{
	"category":      "TEXT",
	"provenance_id": "TEXT",
}

// alterAddedColumns 从 v34 迁移源码里取 `ALTER TABLE <table> ADD COLUMN <name> ...` 的列名（书写顺序）。
//
// 与 createTableColumns（只认 CREATE TABLE）配套：表在 v33 建、列在 v34 追加时，两边拼起来才是完整列集合。
func alterAddedColumns(t *testing.T, table string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(backendRoot(t), "internal", "persistence", "store", "migrate_v34.go"))
	if err != nil {
		t.Fatalf("读不到 migrate_v34.go: %v（路径改了请同步本守卫）", err)
	}
	src := stripLineComments(string(raw))
	re := regexp.MustCompile(`ALTER TABLE\s+` + regexp.QuoteMeta(table) + `\s+ADD COLUMN\s+([a-z_][a-z0-9_]*)`)
	var cols []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		cols = append(cols, strings.ToLower(m[1]))
	}
	return cols
}

// libraryNodeColumnSnapshot library_nodes 冻结列（顺序 + 类型）。
//
// v34 起追加 `category` / `provenance_id`（产物溯源，见 migrate_v34.go 的 ALTER TABLE）。
var libraryNodeColumnSnapshot = []colType{
	{"id", "TEXT"},
	{"space_id", "TEXT"},
	{"parent_id", "TEXT"},
	{"node_kind", "TEXT"},
	{"title", "TEXT"},
	{"body", "TEXT"},
	{"table_id", "TEXT"},
	{"drive_upload_id", "TEXT"},
	{"source_url", "TEXT"},
	{"size_bytes", "INTEGER"},
	{"created_at", "TEXT"},
	{"updated_at", "TEXT"},
	{"category", "TEXT"},
	{"provenance_id", "TEXT"},
}

// libraryAttachmentColumnSnapshot library_attachments 冻结列。
var libraryAttachmentColumnSnapshot = []colType{
	{"node_id", "TEXT"},
	{"attachment_id", "TEXT"},
	{"role", "TEXT"},
	{"created_at", "TEXT"},
}

// libraryTableColumnSnapshot library_tables 冻结列。
var libraryTableColumnSnapshot = []colType{
	{"node_id", "TEXT"},
	{"schema_json", "TEXT"},
	{"rows_json", "TEXT"},
	{"updated_at", "TEXT"},
}

// TestLibraryTablesColumnsFrozen（G1）v33 三张资料库表的列集合与类型冻结。
func TestLibraryTablesColumnsFrozen(t *testing.T) {
	ddlPath := filepath.Join(backendRoot(t), "internal", "persistence", "store", "migrate_v33.go")
	raw, err := os.ReadFile(ddlPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", ddlPath, err)
	}
	src := stripLineComments(string(raw))

	cases := []struct {
		table    string
		snapshot []colType
		entity   string // model 文件名
	}{
		{"library_nodes", libraryNodeColumnSnapshot, "library_node.go"},
		{"library_attachments", libraryAttachmentColumnSnapshot, "library_attachment.go"},
		{"library_tables", libraryTableColumnSnapshot, "library_table.go"},
	}

	for _, c := range cases {
		got, ok := createTableColumns(src, c.table)
		if !ok {
			t.Fatalf("在 %s 里解析不到 `CREATE TABLE %s (...)`：v33 建表语句被改名/挪走了？"+
				"（本守卫 fail-closed：解析不到即失败，避免空转）", ddlPath, c.table)
		}
		// v34 对 library_nodes 有 ALTER TABLE ADD COLUMN：把追加列拼进 DDL 侧（建表 + 后续 ALTER），
		// 才能与「已含 v34 列的 snapshot / 实体」对齐。
		got = append(got, alterAddedColumns(t, c.table)...)
		var wantNames []string
		for _, col := range c.snapshot {
			wantNames = append(wantNames, col.name)
		}
		if strings.Join(got, ",") != strings.Join(wantNames, ",") {
			t.Fatalf("%s 的列集合变了（G1）：\n got  %v\n want %v\n"+
				"改列必须同批更新本快照 + model/%s 的 gorm 标签 + dao 列词汇。", c.table, got, wantNames, c.entity)
		}

		types := createTableColumnTypes(src, c.table)
		for _, col := range c.snapshot {
			tt := types[col.name]
			if v34t, ok := v34AlterTypes[col.name]; ok {
				tt = v34t // v34 ALTER ADD COLUMN 的列类型（v33 CREATE 里没有）
			}
			if tt != col.typ {
				t.Fatalf("%s.%s 的类型变了：got %q want %q（列类型是对外落库契约）", c.table, col.name, tt, col.typ)
			}
		}

		// 实体侧：gorm `column:` 标签与 DDL 逐列一致。
		entityPath := filepath.Join(backendRoot(t), "internal", "persistence", "model", c.entity)
		entityRaw, err := os.ReadFile(entityPath)
		if err != nil {
			t.Fatalf("读不到 %s: %v", entityPath, err)
		}
		entityCols := gormColumnTags(stripLineComments(string(entityRaw)))
		sort.Strings(entityCols)
		wantSorted := append([]string(nil), wantNames...)
		sort.Strings(wantSorted)
		if strings.Join(entityCols, ",") != strings.Join(wantSorted, ",") {
			t.Fatalf("model/%s 的 gorm 列标签与 %s DDL 不一致：\n entity %v\n ddl    %v\n"+
				"两处必须手工逐列一致（column: 标签写错运行期才报 no such column）。", c.entity, c.table, entityCols, wantSorted)
		}
	}
}
