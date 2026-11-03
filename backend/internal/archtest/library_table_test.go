package archtest

import (
	"os"
	"path/filepath"
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

// libraryNodeColumnSnapshot library_nodes 冻结列（顺序 + 类型）。
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
			if tt := types[col.name]; tt != col.typ {
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
