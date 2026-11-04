package archtest

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
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
	// v35 追加：工作区相对路径（dirty 惰性判定定位原文件）。v34 已合入主干不可改，故新列走 v35 ALTER。
	{"path", "TEXT"},
}

// TestArtifactsColumnsFrozen（A-2）v34 artifacts 表的列集合与类型冻结 + 实体标签逐列一致。
func TestArtifactsColumnsFrozen(t *testing.T) {
	ddlPath := filepath.Join(backendRoot(t), "internal", "persistence", "store", "migrate_v34.go")
	raw, err := os.ReadFile(ddlPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", ddlPath, err)
	}
	src := stripLineComments(string(raw))

	baseCols, ok := createTableColumns(src, "artifacts")
	if !ok {
		t.Fatalf("在 %s 里解析不到 `CREATE TABLE artifacts (...)`（本守卫 fail-closed：解析不到即失败）", ddlPath)
	}
	baseTypes := createTableColumnTypes(src, "artifacts")
	// 合并 v35 的 `ALTER TABLE artifacts ADD COLUMN <name> <TYPE>`（v34 已发布不可改，新列走 v35）。
	alterNames, alterTypes, err := parseArtifactAddColumns(filepath.Join(
		backendRoot(t), "internal", "persistence", "store", "migrate_v35.go"))
	if err != nil {
		t.Fatalf("解析 v35 ALTER 失败: %v（改迁移文件请同步本守卫）", err)
	}
	got := append(append([]string(nil), baseCols...), alterNames...)
	types := map[string]string{}
	for k, v := range baseTypes {
		types[k] = v
	}
	for k, v := range alterTypes {
		types[k] = v
	}

	var wantNames []string
	for _, col := range artifactColumnSnapshot {
		wantNames = append(wantNames, col.name)
	}
	if strings.Join(got, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("artifacts 的列集合变了（A-2，v34 CREATE + v35 ALTER 合并）：\n got  %v\n want %v\n"+
			"改列必须同批更新本快照 + model/artifact.go 的 gorm 标签 + dao 列词汇。", got, wantNames)
	}
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

// parseArtifactAddColumns 从迁移文件里解析 `ALTER TABLE artifacts ADD COLUMN <name> <TYPE>`，
// 返回追加列名（按出现顺序）与列名→类型。仅用于 v35 那一步（artifacts 加 path）。
func parseArtifactAddColumns(path string) (names []string, types map[string]string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	src := stripLineComments(string(raw))
	types = map[string]string{}
	// 匹配反引号字符串里的 ALTER 语句（迁移文件里语句是 Go 原始字符串字面量）。
	re := regexp.MustCompile("(?i)ALTER\\s+TABLE\\s+artifacts\\s+ADD\\s+COLUMN\\s+([a-z_]+)\\s+([A-Za-z]+)")
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		name := m[1]
		typ := strings.ToUpper(m[2])
		names = append(names, name)
		types[name] = typ
	}
	if len(names) == 0 {
		return nil, nil, errNoAlter
	}
	return names, types, nil
}

var errNoAlter = errors.New("未找到 ALTER TABLE artifacts ADD COLUMN（v35 是否改动了？）")
