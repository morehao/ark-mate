package archtest

// 本文件钉住 `runs.status` 的**进行中取值只有一个**（v30，2026-10-17）。
//
// 已发生的静默缺陷：`runs` 表的 v1 默认值是 `'running'`，而 v21 起的写入方
// （`domain/session/persist.go` 的 `OpenRun`）显式写 `SessionStatusInProgress`（`in_progress`）。
// 查询方 `dao/run.go` 的 `RunningRows` 与 `store/usage.go` 的计数却读 `running` ⇒ 前者恒空
// （`runRowFor` 的兜底成了死代码），后者恒 0。两边各自都能编译、都能跑，没有任何测试变红。
//
// 判据四段（都 fail-closed，锚找不到即失败）：
//
//	① `OpenRun` 写的是 `SessionStatusInProgress`；
//	② `dao/run.go`（剥注释）含 `in_progress`、且不含 `"running"`；
//	③ `store/usage.go`（剥注释）含 `case "in_progress":`、且不含 `case "running":`；
//	④ v30 迁移存在、已登记，且归一方向可读（同时出现 `'running'` 与 `in_progress`）。
//
// 反证（实跑）：把 DAO 的过滤改回 `running` → ② 红；删掉 v30 的 UPDATE 语句 → ④ 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunInProgressStatusIsSingleSourced 见文件头。
func TestRunInProgressStatusIsSingleSourced(t *testing.T) {
	root := backendRoot(t)
	read := func(rel string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("读 %s 失败: %v（判据失去对象）", rel, err)
		}
		return string(raw)
	}

	// ① 写入方
	openRun := read("internal/domain/session/persist.go")
	if !strings.Contains(openRun, "Status: SessionStatusInProgress") {
		t.Errorf("persist.go 的 OpenRun 不再写 SessionStatusInProgress——写入方与查询方的契约断了")
	}

	// ② DAO 过滤
	daoRun := stripGoComments(read("internal/dao/run.go"))
	if !strings.Contains(daoRun, `"in_progress"`) {
		t.Errorf("dao/run.go 的 RunningRows 不含 in_progress——它必须与 OpenRun 写入的值同源")
	}
	if strings.Contains(daoRun, `"running"`) {
		t.Errorf(`dao/run.go 仍过滤 "running"——写入方不写这个值，查询会再次恒空（v30 已把历史行归一）`)
	}

	// ③ 用量计数
	usage := stripGoComments(read("internal/store/usage.go"))
	if !strings.Contains(usage, `case "in_progress":`) {
		t.Errorf(`store/usage.go 的 Running 计数不再认 in_progress——进行中的轮会被算成"其它"`)
	}
	if strings.Contains(usage, `case "running":`) {
		t.Errorf(`store/usage.go 仍有 case "running"——v30 之后库里不该再有这个取值`)
	}

	// ④ 迁移：存在、已登记、方向可读
	migRaw, err := os.ReadFile(filepath.Join(root, "internal", "store", "migrate_v30.go"))
	if err != nil {
		t.Fatalf("读 migrate_v30.go 失败: %v（v30 是历史数据归一的唯一落点）", err)
	}
	mig := stripGoComments(string(migRaw))
	if !strings.Contains(mig, "'running'") || !strings.Contains(mig, "'in_progress'") {
		t.Errorf("migrate_v30.go 的归一句子不可见——历史 running 行不会被迁到 in_progress")
	}
	if !strings.Contains(read("internal/store/migrate.go"), "migrationV30") {
		t.Errorf("migrate.go 没有登记 migrationV30——迁移写了但没人执行")
	}
}
