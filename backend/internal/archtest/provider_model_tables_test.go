package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// providerModelColumnWhitelist v13 起 `providers` / `models` 是**资源表**：平台拥有的字段全部成列。
//
// 这不是"可以顺手再加列"的许可，而是**白名单**：新增一列必须同时回答两个问题——
//  1. 它是平台拥有的封闭字段（用户自由正文才用 body，ADR-2）？
//  2. 它的不变量有机器判据吗（列集合由本守卫、落库形态由 `format_test`、迁移由 `migrate_test`）？
var providerModelColumnWhitelist = map[string][]string{
	"providers_v13": {"id", "name", "protocol", "base_url", "note", "revision", "created_at", "updated_at"},
	"models_v13":    {"id", "provider_id", "model_id", "name", "note", "input_modalities", "revision", "created_at", "updated_at"},
}

// forbiddenProviderModelColumns 两张表**刻意没有**的列（每一条都是判据，不是遗漏）。
//
// 它们的缺席比白名单更重要：白名单挡住"多一列"，这里挡住"把已经删掉的东西加回来"——
// 后者更常见（"顺手加个 key_ref 吧"），而且后果是**静默**的（第二份副本慢慢漂移、
// 明文重新有了落点）。
var forbiddenProviderModelColumns = []string{
	"body",                                      // 键集合封闭 ⇒ 列（A1 的判据）
	"key_ref",                                   // providerRef(id) 的纯函数副本 + AAD 已绑 vault:providers/<id>（ADR-7）
	"key",                                       // v13 当时刻意不给密钥留列（v20 才用 ALTER 加进 providers，历史建表语句里仍然没有）
	"is_default",                                // 默认是用户的偏好、单一值，住 meta.MetaKeyDefaultModel（ADR-21）
	"runtime",                                   // v12 ADR-1 已删，不回填
	"pinned", "hidden", "sort_order", "enabled", // 无需求证据（ADR-7）
}

// TestProviderModelTablesAreColumnsNotCarriers A1：v13 的两张建表语句列集合 == 白名单，且无 `body`。
//
// **fail-closed**：解析不到 v13 的建表语句即失败（沿用 `settings_copy_test.go` 的纪律）——
// 迁移改名、换写法、或把 DDL 挪走，都必须让守卫红，而不是静默变成空转。
//
// 判据取**静态源码**而不是"实建临时库读 PRAGMA"：后者要求迁移已能跑通，于是"迁移坏了"
// 会把这条守卫一起弄红，两件事就分不开了（PRAGMA 那半由 `internal/persistence/store` 的
// `TestCapabilityDefinitionColumnsFrozen` 在真库上覆盖）。
func TestProviderModelTablesAreColumnsNotCarriers(t *testing.T) {
	path := filepath.Join(backendRoot(t), "internal", "persistence", "store", "migrate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", path, err)
	}
	src := stripLineComments(string(raw))

	for table, want := range providerModelColumnWhitelist {
		got, ok := createTableColumns(src, table)
		if !ok {
			t.Fatalf("在 %s 里解析不到 `CREATE TABLE %s (...)`：v13 的建表语句被改名/挪走了？"+
				"（本守卫是 fail-closed：解析不到即失败，避免它变成空转）", path, table)
		}
		if len(got) != len(want) {
			t.Fatalf("%s 的列集合变了：\n got  %v\n want %v", table, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s 的列集合变了：\n got  %v\n want %v", table, got, want)
			}
		}
		for _, banned := range forbiddenProviderModelColumns {
			for _, col := range got {
				if col == banned {
					t.Fatalf("%s 里出现了 `%s` 列——它是**刻意没有**的（见 forbiddenProviderModelColumns 的逐条理由）",
						table, banned)
				}
			}
		}
	}

	// 两张表都必须真的有 `input_modalities`：默认 `text` 是"平台总是声明输入模态"这条决定的载体
	// （ADR-12）。少了它，"默认 text"就只存在于 Go 侧，交付字节里没有它的落点。
	if cols, _ := createTableColumns(src, "models_v13"); !containsStr(cols, "input_modalities") {
		t.Fatalf("models_v13 缺 input_modalities 列（它是 ADR-12 的载体）：%v", cols)
	}

	// 同一判据的实体侧：ProviderEntity / ModelEntity 的 gorm 标签里**不得**再出现
	// `column:body`（四张「定义载体」表保留 body，判据不同，所以只看这两个结构体块）。
	assertEntityHasNoBodyColumn(t, "ProviderEntity")
	assertEntityHasNoBodyColumn(t, "ModelEntity")
}

// createTableColumns 从剥掉注释的 Go 源码里取 `CREATE TABLE <name> (...)` 的列名（保持书写顺序）。
//
// 只认"**一列一行**"的书写形态（本仓 DDL 一律如此）：表级约束行（PRIMARY KEY / UNIQUE /
// CHECK / FOREIGN KEY）会被跳过——它们不是列。
func createTableColumns(src, table string) ([]string, bool) {
	re := regexp.MustCompile(`(?s)CREATE TABLE ` + regexp.QuoteMeta(table) + `\s*\((.*?)\n\s*\)`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return nil, false
	}
	var cols []string
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "PRIMARY KEY") || strings.HasPrefix(upper, "UNIQUE") ||
			strings.HasPrefix(upper, "CHECK") || strings.HasPrefix(upper, "FOREIGN KEY") ||
			strings.HasPrefix(upper, "CONSTRAINT") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		cols = append(cols, strings.ToLower(fields[0]))
	}
	if len(cols) == 0 {
		return nil, false
	}
	return cols, true
}

// assertEntityHasNoBodyColumn 断言某个实体结构体的字段里没有 `column:body`（fail-closed：找不到结构体即失败）。
//
// v15 起 `internal/persistence/model` 是**一表一文件**（`entity.go` 已拆掉），所以这里必须 glob 整个目录
// 而不是钉死一个文件名——钉死会在拆文件后**解析不到即失败**（fail-closed 是好的那一侧），
// 而"读不到就跳过"那种写法会让守卫静默空转，本函数刻意不给自己留那条路。
func assertEntityHasNoBodyColumn(t *testing.T, structName string) {
	t.Helper()
	dir := filepath.Join(backendRoot(t), "internal", "persistence", "model")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("扫不到 %s/*.go: %v（本守卫是 fail-closed，找不到实体源码即失败）", dir, err)
	}
	re := regexp.MustCompile(`(?s)type ` + regexp.QuoteMeta(structName) + ` struct \{(.*?)\n\}`)
	found := false
	for _, path := range paths {
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("读不到 %s: %v", path, rerr)
		}
		m := re.FindStringSubmatch(stripLineComments(string(raw)))
		if m == nil {
			continue
		}
		found = true
		if strings.Contains(m[1], "column:body") {
			t.Fatalf("%s（%s）里又出现了 `column:body`：这两张表是**资源表**（平台字段全成列），"+
				"body 的键集合封闭——它只留给「正文是用户自由文档」的四张定义载体表（ADR-1/ADR-2）",
				structName, path)
		}
	}
	if !found {
		t.Fatalf("在 %s/*.go 里找不到 `type %s struct`：结构体被改名/挪走了？"+
			"（本守卫是 fail-closed）", dir, structName)
	}
}

// stripLineComments 去掉 `//` 行注释（守卫看的是**代码**，注释里出现被禁的字面量是正常的：
// 它们的理由就写在那些注释里）。
func stripLineComments(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if i := strings.Index(ln, "//"); i >= 0 {
			ln = ln[:i]
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
