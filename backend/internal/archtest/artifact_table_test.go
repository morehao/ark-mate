package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ——— 产物表 artifacts（v34）的列集合冻结（A-2）———
//
// 为什么钉：`artifacts` 的列是**产物溯源落库契约**（读面按列装配、用例层按列读写）。加一列不更新快照
// → 红；改类型不更新 → 红。同时交叉断言 `model/artifact.go` 的 gorm `column:` 标签与 DDL 逐列一致
// （两处手工同步，只查一侧另一半会静默漂移：`column:` 写错编译期无感、运行期才报 no such column）。
//
// 真库侧（迁移可回滚 + 旧库升级）由 `store/migrate_v34_test.go` 覆盖。

// artifactColumnSnapshot artifacts 冻结列（顺序 + 类型，与 migrate_v34.go 一致）。
var artifactColumnSnapshot = []colType{
	{"id", "TEXT"},
	{"session_id", "TEXT"},
	{"run_id", "TEXT"},
	{"tool_call_id", "TEXT"},
	{"origin", "TEXT"},
	{"kind", "TEXT"},
	{"title", "TEXT"},
	{"mime_type", "TEXT"},
	{"size_bytes", "INTEGER"},
	{"upload_id", "TEXT"},
	{"content_text", "TEXT"},
	{"content_sha256", "TEXT"},
	{"dirty", "INTEGER"},
	{"created_at", "TEXT"},
	{"updated_at", "TEXT"},
}

// TestArtifactsColumnsFrozen（A-2）v34 artifacts 表的列集合与类型冻结 + 实体标签逐列一致。
func TestArtifactsColumnsFrozen(t *testing.T) {
	ddlPath := filepath.Join(backendRoot(t), "internal", "persistence", "store", "migrate_v34.go")
	raw, err := os.ReadFile(ddlPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", ddlPath, err)
	}
	src := stripLineComments(string(raw))

	got, ok := createTableColumns(src, "artifacts")
	if !ok {
		t.Fatalf("在 %s 里解析不到 `CREATE TABLE artifacts (...)`（本守卫 fail-closed：解析不到即失败）", ddlPath)
	}
	var wantNames []string
	for _, col := range artifactColumnSnapshot {
		wantNames = append(wantNames, col.name)
	}
	if strings.Join(got, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("artifacts 的列集合变了（A-2）：\n got  %v\n want %v\n"+
			"改列必须同批更新本快照 + model/artifact.go 的 gorm 标签 + dao 列词汇。", got, wantNames)
	}
	types := createTableColumnTypes(src, "artifacts")
	for _, col := range artifactColumnSnapshot {
		if tt := types[col.name]; tt != col.typ {
			t.Fatalf("artifacts.%s 的类型变了：got %q want %q（列类型是对外落库契约）", col.name, tt, col.typ)
		}
	}

	// 实体侧：gorm `column:` 标签与 DDL 逐列一致。
	entityPath := filepath.Join(backendRoot(t), "internal", "persistence", "model", "artifact.go")
	entityRaw, err := os.ReadFile(entityPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v", entityPath, err)
	}
	entityCols := gormColumnTags(stripLineComments(string(entityRaw)))
	sort.Strings(entityCols)
	wantSorted := append([]string(nil), wantNames...)
	sort.Strings(wantSorted)
	if strings.Join(entityCols, ",") != strings.Join(wantSorted, ",") {
		t.Fatalf("model/artifact.go 的 gorm 列标签与 artifacts DDL 不一致：\n entity %v\n ddl    %v\n"+
			"两处必须手工逐列一致。", entityCols, wantSorted)
	}
}
