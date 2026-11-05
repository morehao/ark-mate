package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ——— 内部模型调用流水表 llm_calls（v37）列集合冻结 ———
//
// 为什么钉：`llm_calls` 的列是**调用流水落库契约**（网关记账写入、用量聚合按列读）。加一列不改
// 快照 → 红；改类型不改 → 红。同时交叉断言 `model/llm_call.go` 的 gorm `column:` 标签与 DDL 逐列一致
// （两处手工同步，只查一侧另一半会静默漂移：`column:` 写错编译期无感、运行期才报 no such column）。
//
// 真库侧（全新库迁移建表 + 插入/聚合往返）由 `store/migrate_v37_test.go` 覆盖。

// llmCallColumnSnapshot llm_calls 冻结列（顺序 + 类型，与 migrate_v37.go 一致）。
var llmCallColumnSnapshot = []colType{
	{"id", "TEXT"},
	{"at", "TEXT"},
	{"purpose", "TEXT"},
	{"session_id", "TEXT"},
	{"run_id", "TEXT"},
	{"provider", "TEXT"},
	{"model", "TEXT"},
	{"input_tokens", "INTEGER"},
	{"output_tokens", "INTEGER"},
	{"cached_read_tokens", "INTEGER"},
	{"cached_write_tokens", "INTEGER"},
	{"thought_tokens", "INTEGER"},
	{"total_tokens", "INTEGER"},
	{"cost_amount", "REAL"},
	{"cost_currency", "TEXT"},
	{"latency_ms", "INTEGER"},
	{"status", "TEXT"},
}

// TestLLMCallsColumnsFrozen v37 llm_calls 表的列集合与类型冻结 + 实体标签逐列一致。
func TestLLMCallsColumnsFrozen(t *testing.T) {
	ddlPath := filepath.Join(backendRoot(t), "internal", "persistence", "store", "migrate_v37.go")
	raw, err := os.ReadFile(ddlPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", ddlPath, err)
	}
	src := stripLineComments(string(raw))

	cols, ok := createTableColumns(src, "llm_calls")
	if !ok {
		t.Fatalf("在 %s 里解析不到 `CREATE TABLE llm_calls (...)`（fail-closed：解析不到即失败）", ddlPath)
	}
	types := createTableColumnTypes(src, "llm_calls")

	var wantNames []string
	for _, col := range llmCallColumnSnapshot {
		wantNames = append(wantNames, col.name)
	}
	if strings.Join(cols, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("llm_calls 的列集合变了：\n got  %v\n want %v\n"+
			"改列必须同批更新本快照 + model/llm_call.go 的 gorm 标签 + store/llm_calls.go 读写。", cols, wantNames)
	}
	for _, col := range llmCallColumnSnapshot {
		if tt := types[col.name]; tt != col.typ {
			t.Fatalf("llm_calls.%s 的类型变了：got %q want %q（列类型是对外落库契约）", col.name, tt, col.typ)
		}
	}

	// 实体侧：gorm `column:` 标签与 DDL 逐列一致。
	entityPath := filepath.Join(backendRoot(t), "internal", "persistence", "model", "llm_call.go")
	entityRaw, err := os.ReadFile(entityPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v", entityPath, err)
	}
	entityCols := gormColumnTags(stripLineComments(string(entityRaw)))
	sort.Strings(entityCols)
	wantSorted := append([]string(nil), wantNames...)
	sort.Strings(wantSorted)
	if strings.Join(entityCols, ",") != strings.Join(wantSorted, ",") {
		t.Fatalf("model/llm_call.go 的 gorm 列标签与 llm_calls DDL 不一致：\n entity %v\n ddl    %v\n"+
			"两处必须手工逐列一致。", entityCols, wantSorted)
	}
}
