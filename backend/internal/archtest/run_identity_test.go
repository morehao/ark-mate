package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件是 v22「轮次成为实体」的**身份与约束**守卫（G6）。
//
// 设计 §D2 / J2 / J6：一轮 prompt 的 `id` 是**平台铸造**的 `run_<26 位 ULID>`，而"一轮一行"
// 这件事由 DB 唯一约束 `UNIQUE(session_id, first_seq)` 承担——不再由"同一套窗口推导规则算出
// 同一个字符串"承担。
//
// 为什么这条需要机器判据：v22 之前 `runs.id` 是 `r-<session_id>-<first_seq>` 这种**派生**串
// （单写者 `persist.go` 的物化路径），它让"身份"与"窗口规则"绑死——窗口规则一改，历史轮次的
// id 就跟着变（M1 的位次回归正是这种改动）。三种静默回退都不会让别的测试变红：
//
//	① 实体主键被写回位置列（`FirstSeq` 带 `primaryKey`）——一轮的身份又变成"它从哪开始"，
//	   同一轮在不同窗口规则下有两个身份；
//	② 迁移层少了那条唯一索引——重放/重试时 `ON CONFLICT DO NOTHING` 失去对象，同一
//	   `(session_id, first_seq)` 会插出第二行（幂等从"约束保证"退化成"恰好没撞"）；
//	③ `persist.go` 里重新出现 `fmt.Sprintf("r-%s-%d"`——派生 id 复活，而它**能跑通全部读写路径**。
//
// 三条腿：实体（gorm 标签）、迁移层（历史字面量里的唯一索引）、真实 schema（跑完整迁移后
// `runs` 上的 UNIQUE 索引列集合）、以及"铸造点只有一处"。全部 fail-closed。

// runIDDerivationMarkers 是"派生轮次 id"的写法标记（v22 起禁止）。
var runIDDerivationMarkers = []string{
	`fmt.Sprintf("r-%s-%d"`,
	`"r-" +`,
	`"r-%s"`,
}

// TestRunIdentityIsMintedAndUnique（G6，v22）：轮次身份铸造 + `(session_id, first_seq)` 唯一。
func TestRunIdentityIsMintedAndUnique(t *testing.T) {
	root := backendRoot(t)

	// ① 实体：`RunEntity.ID` 是平台铸造的主键；位置列（FirstSeq/LastSeq）**不得**参与主键。
	entityPath := filepath.Join(root, "internal", "model", "run.go")
	entity := stripLineComments(readFileOrFatal(t, entityPath))
	idField := regexp.MustCompile(`(?m)^\s*ID\s+string\s+` + "`" + `gorm:"([^"]*)"` + "`")
	m := idField.FindStringSubmatch(entity)
	if m == nil {
		t.Fatalf("%s 里找不到 `ID string` + gorm 标签（本守卫 fail-closed：解析不到即失败——"+
			"实体被改名/挪文件时这条判据会静默空转）", entityPath)
	}
	if !strings.Contains(m[1], "primaryKey") {
		t.Fatalf("`RunEntity.ID` 必须是主键（`column:id;primaryKey`），实际标签 %q：轮次的身份是**铸造**出来的，"+
			"不是算出来的", m[1])
	}
	for _, positional := range []string{"FirstSeq", "LastSeq"} {
		re := regexp.MustCompile(`(?m)^\s*` + positional + `\s+int64\s+` + "`" + `gorm:"([^"]*)"` + "`")
		pm := re.FindStringSubmatch(entity)
		if pm == nil {
			t.Fatalf("%s 里找不到 `%s int64` + gorm 标签（fail-closed）", entityPath, positional)
		}
		if strings.Contains(pm[1], "primaryKey") {
			t.Fatalf("`RunEntity.%s` 带上了 primaryKey（%q）：主键一旦是位置列，同一轮在不同窗口规则下就有两个身份",
				positional, pm[1])
		}
	}

	// ② 迁移层：唯一索引必须真的建在 `runs(session_id, first_seq)` 上。
	//    扫**全部** `migrate_v*.go` 而不是钉住某个版本文件：文件会随版本往后长，
	//    钉死会在下次迁移时解析不到——本守卫依旧 fail-closed（宁可真红，也不静默）。
	uniqueOnWindow := regexp.MustCompile(`(?is)CREATE\s+UNIQUE\s+INDEX\s+\S+\s+ON\s+runs\s*\(\s*session_id\s*,\s*first_seq\s*\)`)
	migrateDir := filepath.Join(root, "internal", "store")
	paths, err := filepath.Glob(filepath.Join(migrateDir, "migrate_v*.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("扫不到 %s/migrate_v*.go: %v（fail-closed：迁移源码找不到即失败）", migrateDir, err)
	}
	found := ""
	for _, p := range paths {
		if uniqueOnWindow.MatchString(stripLineComments(readFileOrFatal(t, p))) {
			found = p
			break
		}
	}
	if found == "" {
		t.Fatalf("迁移层里找不到 `CREATE UNIQUE INDEX … ON runs(session_id, first_seq)`：\n" +
			"v22 起" + "\"一轮一行\"由这条约束承担（`ON CONFLICT DO NOTHING` 的冲突目标），\n" +
			"没有它，重放同一轮会插出第二行——而**单写者的测试路径恰好不会撞**，所以别的判据都还是绿的")
	}

	// ③ 真实 schema：跑完整迁移的库里，`runs` 上必须存在一个 UNIQUE 索引，其列**恰好**是
	//    `(session_id, first_seq)`。用列集合判，不看索引名（改名同样要能拦住）。
	db := migratedSchemaDB(t)
	rows, err := db.Raw(`SELECT name, sql FROM sqlite_master
		WHERE type='index' AND tbl_name='runs' AND sql IS NOT NULL`).Rows()
	if err != nil {
		t.Fatalf("读 runs 的索引定义: %v", err)
	}
	sawAny := false
	for rows.Next() {
		var name string
		var sql *string
		if err := rows.Scan(&name, &sql); err != nil {
			_ = rows.Close()
			t.Fatalf("扫 runs 索引: %v", err)
		}
		sawAny = true
		if sql == nil {
			continue
		}
		if uniqueOnWindow.MatchString(*sql) {
			if err := rows.Close(); err != nil {
				t.Fatalf("关闭索引游标: %v", err)
			}
			// 命中即过；继续往下走第 ④ 段。
			goto minted
		}
	}
	_ = rows.Close()
	if !sawAny {
		t.Fatalf("`runs` 上一索引都没有：迁移没跑到？")
	}
	t.Fatalf("真实 schema 里没有覆盖 `(session_id, first_seq)` 的 UNIQUE 索引——" +
		"J6 的\"不变量靠约束\"在**交付的 DDL**里不成立（迁移文本里写了而实际没建出来，同样会走到这里）")

minted:
	// ④ 铸造点：`internal/` 全树里必须有且只有**一处运行时**铸轮次 id
	//    （`objid.New(objid.Run)`），且不得出现派生写法。
	//
	// 锚点是**符号计数 + 递归扫描**，不是文件路径。为什么：这个点会随包搬家
	// （`manager/run.go` → 目标 `internal/domain/session`），而 `filepath.Glob(dir, "*.go")`
	// **不递归**——文件一旦进子目录，判据就静默空转（照样绿，什么都没检查）。
	// 放宽到整棵树同时是**更强**的判据：派生写法或第二处铸号出现在 `internal/` 任何地方都红。
	//
	// 而且"恰好一处"要按**分部**看：`store/migrate_v22.go` 的历史回填也是一处，混成一个
	// 数字就无法区分「运行时 + 迁移」与「运行时 + 运行时」——后者才是要拦的。
	internalDir := filepath.Join(root, "internal")
	treeFiles := goFilesUnder(t, internalDir)
	if len(treeFiles) == 0 {
		t.Fatalf("扫不到 %s 下的 Go 文件（fail-closed：守卫会静默空转）", internalDir)
	}
	mintedRuntime, mintedMigration, scanned := 0, 0, 0
	for _, p := range treeFiles {
		relSlash := filepath.ToSlash(rel(root, p))
		if strings.HasSuffix(p, "_test.go") || strings.HasPrefix(relSlash, "internal/archtest/") {
			continue
		}
		scanned++
		src := stripLineComments(readFileOrFatal(t, p))
		if n := strings.Count(src, "objid.New(objid.Run)"); n > 0 {
			if strings.Contains(filepath.Base(p), "migrate_v") {
				mintedMigration += n
			} else {
				mintedRuntime += n
			}
		}
		for _, marker := range runIDDerivationMarkers {
			if strings.Contains(src, marker) {
				t.Fatalf("%s 里出现了派生轮次 id 的写法 %s：\n"+
					"v22 起轮次身份一律 `objid.New(objid.Run)`（`run_<26 位 ULID>`）——"+
					"派生 id 会让\"身份\"跟着窗口规则变，而它跑得通全部读写路径（这正是 M1 那段历史踩过的坑）",
					relSlash, marker)
			}
		}
	}
	if scanned == 0 {
		t.Fatalf("`internal/` 非测试、非 archtest 的扫描面为空（fail-closed）")
	}
	if mintedRuntime != 1 || mintedMigration != 1 {
		t.Fatalf("轮次铸号点的分部变了：运行时 %d 处（want 1）、迁移回填 %d 处（want 1）。\n"+
			"「运行时只铸一次」这条不变式只在**迁移之外**成立——迁移是历史数据的回填，与运行时铸号"+
			"是两件事；把两者混成一个数字，等于允许第二条运行时铸号路径存在。", mintedRuntime, mintedMigration)
	}
}
