package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workspaceColumnWhitelist v14 起 `workspaces` 是**登记项**：平台拥有的字段全部成列。
//
// 与 `providerModelColumnWhitelist` 同形、同一条纪律：这不是"可以顺手再加列"的许可，
// 而是白名单——新增一列必须同时回答两个问题：
//  1. 它是平台拥有的封闭字段吗？
//  2. 它的不变量有机器判据吗（列集合由本守卫、顺序对账由 store 的 TestWorkspaceMoveOrder、
//     唯一性由 TestWorkspaceUniqueIndexEnforced）？
var workspaceColumnWhitelist = []string{
	"id", "path", "title", "is_default", "list_order", "created_at", "updated_at",
}

// forbiddenWorkspaceColumns 这张表**刻意没有**的列（每一条都是判据，不是遗漏）。
//
// 它们的缺席比白名单更重要：白名单挡住"多一列"，这里挡住"把不要的东西加回来"——
// 后者更常见（"顺手存一份成员账吧""缓存一下目录状态吧"），而且后果是**静默**的。
var forbiddenWorkspaceColumns = []string{
	// 成员账：归属由 `conversations.workspace_id` 表达（设计 ADR-5）。dsh 的登记行里有
	// 顺序化的 sessionIds，那是因为它的会话账独立于会话行、会漂移；本仓归属就在会话行上，
	// 再存一份账只会多一条会不一致的路径。
	"session_ids", "sessions", "session_count",
	// 目录状态：**不缓存**。外部进程随时可能删/移目录，缓存只会让界面显示一个过期的好状态。
	"exists", "dir_status", "available", "last_seen_at",
	// 用户自由正文：登记项没有"正文"这种东西（键集合封闭）。
	"body", "meta", "extra", "config",
	// 没有需求证据的字段。
	"pinned", "hidden", "archived", "color", "icon", "sort_order",
}

// TestWorkspaceTableIsRegistrationNotCarrier A1：v14 的建表语句列集合 == 白名单，且无成员账。
//
// **fail-closed**：解析不到 v14 的建表语句即失败（沿用 `provider_model_tables_test.go` 的纪律）
// ——迁移改名、换写法、或把 DDL 挪走，都必须让守卫红，而不是静默变成空转。
//
// 判据取**静态源码**而不是"实建临时库读 PRAGMA"：后者要求迁移已能跑通，于是"迁移坏了"
// 会把这条守卫一起弄红，两件事就分不开（PRAGMA 那半由 `internal/store` 的
// `TestWorkspaceUniqueIndexEnforced` 在真库上覆盖）。
func TestWorkspaceTableIsRegistrationNotCarrier(t *testing.T) {
	path := filepath.Join(backendRoot(t), "internal", "store", "migrate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", path, err)
	}
	src := stripLineComments(string(raw))

	got, ok := createTableColumns(src, "workspaces")
	if !ok {
		t.Fatalf("在 %s 里解析不到 `CREATE TABLE workspaces (...)`：v14 的建表语句被改名/挪走了？"+
			"（本守卫是 fail-closed：解析不到即失败，避免它变成空转）", path)
	}
	if len(got) != len(workspaceColumnWhitelist) {
		t.Fatalf("workspaces 的列集合变了：\n got  %v\n want %v", got, workspaceColumnWhitelist)
	}
	for i := range workspaceColumnWhitelist {
		if got[i] != workspaceColumnWhitelist[i] {
			t.Fatalf("workspaces 的列集合变了：\n got  %v\n want %v", got, workspaceColumnWhitelist)
		}
	}
	for _, banned := range forbiddenWorkspaceColumns {
		if containsStr(got, banned) {
			t.Fatalf("workspaces 里出现了 `%s` 列——它是**刻意没有**的（见 forbiddenWorkspaceColumns 的逐条理由）",
				banned)
		}
	}

	// 两条索引都是判据的载体，缺一条就少一条不变量：
	//   - `path` 唯一：一个规范化目录至多一条登记（设计 ADR-1）；
	//   - `is_default` 部分唯一：「有且只有一个默认工作空间」（设计 ADR-4）。
	if !strings.Contains(src, "CREATE UNIQUE INDEX idx_workspaces_path ON workspaces(path)") {
		t.Fatal("workspaces 缺 path 的唯一索引：同一个目录会插出两条登记（设计 ADR-1）")
	}
	if !strings.Contains(src, "WHERE is_default = 1") {
		t.Fatal("workspaces 缺 is_default 的**部分**唯一索引：默认工作空间会变成两个，落点不确定" +
			"（普通唯一索引会把几十行 0 判成重复，所以这里必须带 WHERE）")
	}
}

// TestSessionWorkspaceColumnIsPlain 归属列是**普通列**，不是外键（v14 的增列与 v15 的建表两处都判）。
//
// 为什么专门守这一条：加外键在别的项目里是"更规范"的写法，本仓却恰恰相反——
//   - 本仓 SQLite driver 不注入 `foreign_keys`，写了**不生效**，于是它只提供一种虚假的安心；
//   - 更要紧的是**语义错了**：删登记**不该**连带删会话（设计 ADR-6），级联恰恰是错的方向；
//     而 RESTRICT 又会让"移除登记"在会话还挂着时直接失败——那是把数据保留做成了一次报错。
func TestSessionWorkspaceColumnIsPlain(t *testing.T) {
	path := filepath.Join(backendRoot(t), "internal", "store", "migrate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v", path, err)
	}
	src := stripLineComments(string(raw))

	// ① v14 的历史增列（表名是历史名 —— 迁移步只写字面量，见守卫⑤）。
	re := regexp.MustCompile(`ALTER TABLE conversations ADD COLUMN workspace_id[^\n]*`)
	m := re.FindString(src)
	if m == "" {
		t.Fatal("在 migrate.go 里找不到 `ALTER TABLE conversations ADD COLUMN workspace_id`：" +
			"归属列被改名/挪走了？（本守卫是 fail-closed）")
	}
	assertPlainWorkspaceColumn(t, m, "v14 增列")

	// ② v15 的建表语句（**当前形态**的权威来源）：`sessions.workspace_id` 同样是普通列。
	//
	// 只判 v14 那一处是不够的：v15 把 `conversations` 整表重建成了 `sessions`，而"建成什么样"
	// 由这段 DDL 决定——在 CREATE TABLE 里写 `REFERENCES` 不会让 v14 那行变红。
	blockRe := regexp.MustCompile(`(?s)CREATE TABLE sessions \((.*?)\n\s*\)`)
	b := blockRe.FindStringSubmatch(src)
	if b == nil {
		t.Fatal("在 migrate.go 里找不到 `CREATE TABLE sessions (...)`（v15 的建表语句）——fail-closed")
	}
	line := ""
	for _, ln := range strings.Split(b[1], "\n") {
		if strings.Contains(ln, "workspace_id") {
			line = strings.TrimSpace(ln)
		}
	}
	if line == "" {
		t.Fatal("v15 的 `CREATE TABLE sessions` 里没有 workspace_id 列——归属列丢了")
	}
	assertPlainWorkspaceColumn(t, line, "v15 建表")
}

// assertPlainWorkspaceColumn 断言归属列是**普通列**：无 REFERENCES、带 `DEFAULT ”`。
//
// 为什么专门守这一条：加外键在别的项目里是"更规范"的写法，本仓却恰恰相反——
//   - 本仓 SQLite driver 不注入 `foreign_keys`，写了**不生效**，于是它只提供一种虚假的安心；
//   - 更要紧的是**语义错了**：删登记**不该**连带删会话（设计 ADR-6），级联恰恰是错的方向；
//     RESTRICT 又会让"移除登记"在会话还挂着时直接失败——那是把数据保留做成了一次报错。
func assertPlainWorkspaceColumn(t *testing.T, col, where string) {
	t.Helper()
	if strings.Contains(strings.ToUpper(col), "REFERENCES") {
		t.Fatalf("%s 的 workspace_id 带了外键：%q\n"+
			"本仓 driver 不注入 foreign_keys，写了不生效；而它的语义（级联/RESTRICT）与"+
			"「删登记不删会话」正好相反（设计 ADR-6）", where, col)
	}
	if !strings.Contains(col, "DEFAULT ''") {
		t.Fatalf("%s 的 workspace_id 应带 DEFAULT ''（历史行天然归默认工作空间，不需要一次全表 UPDATE）：%q",
			where, col)
	}
}
