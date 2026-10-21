package archtest

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/infra/dbclient"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/persistence/store"

	// 守卫 10 要开一个临时库跑真实迁移，而 `dbclient.Open` → `glog.NewLogger` 需要 zap driver：
	// 日志出口归装配根（`cmd/mate` 的匿名引入），test 二进制不含它（同 `internal/persistence/store` 的做法）。
	_ "github.com/morehao/golib/glog/driver/zap"
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
	entityPath := filepath.Join(root, "internal", "persistence", "model", "run.go")
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
	migrateDir := filepath.Join(root, "internal", "persistence", "store")
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
	// （`manager/run.go` → `internal/domain/session/runner.go`，P4a 第五十五刀已完成），而 `filepath.Glob(dir, "*.go")`
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

// ——— v27 + S7：崩溃残轮**只在恢复路径**收口（ADR-8 的两半） ———
//
// ADR-8 的两处行为是**相反**的，而这正是它需要**两条判据**（而不是一条"统一"判据）的原因：
//
//	实时路径（`executeRun` 的 `intentional` 分支：daemon 停机 / 显式停 agent）
//	  → **不发**终态、`markSession(interrupted)`。伪终态会污染历史、失败轮列表与重试入口。
//	恢复路径（`Restore` 把残留 `in_progress` 收敛成 `interrupted` 时）
//	  → **补写** `run.status(interrupted)`，`runs` 行因此不再永久停在 `running`。
//
// 为什么这需要机器判据：两处**看起来**不一致，下一个人很容易顺手"统一"掉。而统一到任一侧
// 都有明确代价——只做实时（发伪终态）→ 历史被污染；只做恢复（现状 v26）→ `runs` 表永久残留
// `running` 行、界面永久要用户点「停止」去解除一个其实已经死掉的轮次。两种结果都不会让任何
// 既有测试变红。
//
// 锚点取法是刻意的：**不**扫 `BusRunStatus` 字面量（`manager` 里根本不该出现它，由
// `TestRunStatusIsSingleSource` 钉），而是扫**恢复路径的入口函数**——"有铸造函数但没人调用"
// 是这一族里最典型的静默 no-op。

// TestInterruptedRunsAreClosedOnRestoreButNotLive 守卫 9（ADR-8）：恢复收口在、实时伪终态不在。
func TestInterruptedRunsAreClosedOnRestoreButNotLive(t *testing.T) {
	root := backendRoot(t)

	// ① 恢复路径：`persist.go` 里的收口只有一份实现，且**真的被 `Restore` 调用**。
	persistPath := filepath.Join(root, "internal", "domain", "session", "persist.go")
	persist := stripLineComments(readFileOrFatal(t, persistPath))
	if !strings.Contains(persist, "func (pl *Plane) closeInterruptedRuns(") {
		t.Fatalf("%s 里找不到 `func (pl *Plane) closeInterruptedRuns(`。"+
			"恢复收口是 ADR-8 的后半：没有它，崩溃残轮在 `runs` 表里**永久**停在 running，"+
			"而界面只能让用户点「停止」去解除一个已经不存在的轮次", persistPath)
	}
	if !strings.Contains(persist, "pl.closeInterruptedRuns(ctx, interruptedIDs)") {
		t.Fatalf("%s 里的 `pl.closeInterruptedRuns(` 没有被 `Restore` 调用。"+
			"**有实现没接线**是这一族最典型的静默失效：铸造函数写好了、调用点漏了，"+
			"而进程启动、会话、日志全部正常", persistPath)
	}
	if strings.Count(persist, "pl.PublishRunInterrupted(") != 1 {
		t.Fatalf("`PublishRunInterrupted(` 在 %s 里出现 %d 次（want 1）：恢复收口必须**只有一个**发射点——"+
			"多出来的那一处只能是实时路径，而实时路径刻意不发它", persistPath,
			strings.Count(persist, "pl.PublishRunInterrupted("))
	}
	// 恢复收口必须排在**事件注入之后**（先发会让 bus 的 nextSeq 把随后回放的历史事件逐个顶号，
	// 位次是续传游标，顶号等于把客户端的 since_seq 全部错开——而表面上一切正常）。
	injectAt := strings.Index(persist, "pl.bus.PublishAt(")
	closeAt := strings.Index(persist, "pl.closeInterruptedRuns(ctx, interruptedIDs)")
	if injectAt < 0 || closeAt < 0 {
		t.Fatalf("%s 里解析不到「事件注入」或「恢复收口」的位置（fail-closed）", persistPath)
	}
	if closeAt < injectAt {
		t.Fatalf("%s 里的恢复收口排在事件注入**之前**：先发的收口事件会把 bus 的 nextSeq 推到最新，"+
			"随后逐条 `PublishAt` 回放的历史事件被逐个顶号（位次是续传游标）", persistPath)
	}

	// ② 实时路径：`executeRun` 的 `intentional` 分支**保持零事件**。
	runPath := filepath.Join(root, "internal", "domain", "session", "runner.go")
	runSrc := stripLineComments(readFileOrFatal(t, runPath))
	block, ok := switchCaseBlock(runSrc, "case intentional:")
	if !ok {
		t.Fatalf("%s 里找不到 `case intentional:` 分支（改名/挪走了？本守卫 fail-closed）", runPath)
	}
	for _, marker := range []string{"BusRun", "PublishRunStatus", "CloseRunStatus", "RunStatusFor"} {
		if strings.Contains(block, marker) {
			t.Fatalf("%s 的 `case intentional:` 分支里出现了 %q：\n"+
				"ADR-8 的前半是**实时路径继续不发伪终态**（原注释逐字：「伪终态会污染历史、失败轮"+
				"列表与重试入口」）。这两处行为相反是刻意的，各有一条判据——不要「统一」它们", runPath, marker)
		}
	}
}

// switchCaseBlock 取 `case <标签>` 到**下一个同缩进 case**（或 switch 收尾）之间的源码块。
//
// 判据写成"同一分支内"而不是"同一文件"：`executeRun` 的四个终态分支在同一个 switch 里，
// 按文件扫会把相邻分支的发射算进来（假红），按分支扫才是"这一支有没有发事件"。
func switchCaseBlock(src, label string) (string, bool) {
	start := strings.Index(src, label)
	if start < 0 {
		return "", false
	}
	rest := src[start+len(label):]
	// 分支体到下一个 "\n\tcase " / "\n\tdefault:" 或函数收尾 "\n}" 为止（gofmt 下 case 缩进一层制表符）。
	end := len(rest)
	for _, sep := range []string{"\n\tcase ", "\n\tdefault:", "\n}"} {
		if i := strings.Index(rest, sep); i >= 0 && i < end {
			end = i
		}
	}
	return rest[:end], true
}

// TestRunReadModelHasNoDanglingRunning 守卫 10（ADR-8 / J7）：重建之后**不该有**悬空的 running 行。
//
// 判据落在**真实库 + 真实重建**上，而不是扫源码：这一条要钉的是「重跑读模型之后那一行是什么
// 状态」，而扫 `closeRunsWithTerminal` 里有没有某个字面量只能证明"看起来会收口"。
//
// 造的场景是**崩溃残轮**：会话被收敛成 `interrupted`（进程死了），该轮只有 `run.started`、
// 没有任何终态事件。重建之后它必须不是 `running`——否则 `mate doctor` 的
// 「残留 running 行数 = 0」那条判据永远过不了，而界面会一直给出「可点停止解除」。
func TestRunReadModelHasNoDanglingRunning(t *testing.T) {
	ctx := context.Background()
	st := openTempRunStore(t)

	sessionID := objid.New(objid.Session)
	runID := objid.New(objid.Run)
	if err := st.UpsertSession(ctx, model.SessionEntity{
		ID: sessionID, Cwd: t.TempDir(), Status: session.SessionStatusInProgress,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("写会话行: %v", err)
	}
	// 只有开轮事件，没有终态（崩溃就是这样留下现场的）。
	firstSeq, err := st.AppendEvent(ctx, objid.New(objid.Event), sessionID, session.BusRunStarted, map[string]any{
		"session_id": sessionID, "run_id": runID, "text": "跑一下",
	})
	if err != nil {
		t.Fatalf("写 run.started: %v", err)
	}
	if err := st.CreateRun(ctx, model.RunEntity{
		ID: runID, SessionID: sessionID, UserText: "跑一下",
		Status: session.SessionStatusInProgress, FirstSeq: firstSeq, LastSeq: -1,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("写开轮行: %v", err)
	}

	pl := session.NewPlane()
	pl.SetStore(st)
	// 恢复收敛后的形态：会话 `interrupted`（这是 `Restore` 对残留 in_progress 的处置结果）。
	pl.RegisterSession(&session.SessionRec{
		ID: sessionID, Cwd: t.TempDir(), Status: session.SessionStatusInterrupted,
	})

	if err := pl.RebuildSessionReadModels(ctx, sessionID); err != nil {
		t.Fatalf("重建读模型: %v", err)
	}

	runs, err := st.ListRuns(ctx, sessionID)
	if err != nil {
		t.Fatalf("读 runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("重建后 runs 行数 = %d（want 1）：行都不在了，下面那条断言会空转", len(runs))
	}
	if got := runs[0].Status; got == session.SessionStatusInProgress {
		t.Fatalf("重建之后该轮仍是 %q：会话已经不在 in_progress，一条会话不可能还有一轮在跑。"+
			"`mate doctor` 的「残留 running = 0」与界面的「可点停止解除」都靠这条判据", got)
	}
	if got := runs[0].Status; got != session.SessionStatusInterrupted {
		t.Fatalf("该轮收口取值 = %q（want %q）：恢复路径收的是「被重启带走」——"+
			"说「失败」是代底座下结论，平台只知道自己被杀了", got, session.SessionStatusInterrupted)
	}
}

// openTempRunStore 造一个临时库并跑完整迁移，返回可写的 store（连接在测试结束时关闭）。
func openTempRunStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := dbclient.Open(filepath.Join(t.TempDir(), "run-closer.db"))
	if err != nil {
		t.Fatalf("开临时库: %v", err)
	}
	st := store.New(db)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("跑完整迁移: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
