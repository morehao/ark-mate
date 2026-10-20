package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件是 v21「位次回归每会话」的**范围**守卫（G4）。
//
// 设计 §3 的决策 E1：`events.seq` 从"全库单调号序"变回"**会话内**位次"，取号器是
// `event_sequence(session_id, seq)`，全局水位 `meta.events_last_seq` 随之退场。
//
// 为什么这条需要机器判据：全局号序的**复活**不会让任何别的测试变红。它有三种静默长回来的方式，
// 每一种都能编过、跑通、看起来正常：
//
//	① 有人嫌"每会话一行计数器"麻烦，把取号改回 `SELECT MAX(seq)+1 FROM events`（跨会话）——
//	   两个会话同时写就互相顶号，而单会话的测试永远看不出；
//	② 有人把 `idx_events_seq`（**全局**唯一）顺手加回来"以防重复"——它会让第二个会话的
//	   第 0 条直接写不进去（UNIQUE 冲突），或者更糟：让号序悄悄变成全局的；
//	③ 有人把 `meta.events_last_seq` 当成"顺手保留的兼容水位"继续读写——它记的是**最后一个
//	   全局号**，v21 之后这个数已经不对应任何东西，读它做分页会静默漏事件。
//
// 判据两条腿：**真实 schema**（跑完整迁移后读 `sqlite_master`）管 ①/②，**剥注释后的源码**
// 管 ③ 与"取号只能有一个入口"。两条都 fail-closed。

// TestEventsHasNoGlobalSequence（G4，v21）：事件位次是**会话内**的量，全局号序不得复活。
func TestEventsHasNoGlobalSequence(t *testing.T) {
	root := backendRoot(t)

	// ① 真实 schema：`event_sequence` 必须存在且以 `session_id` 为键（每会话一行计数器）。
	db := migratedSchemaDB(t)
	var cols []string
	rows, err := db.Raw(`SELECT name FROM pragma_table_info('event_sequence') ORDER BY cid`).Rows()
	if err != nil {
		t.Fatalf("读 `event_sequence` 的列（v21 的取号器必须存在）：%v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫 `event_sequence` 列名: %v", err)
		}
		cols = append(cols, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("关闭列游标: %v", err)
	}
	if strings.Join(cols, ",") != "session_id,seq" {
		t.Fatalf("`event_sequence` 的列必须是 `session_id, seq`（一列一义：会话 + 该会话已分配到的位次），"+
			"实际 %v", cols)
	}

	// ② 真实 schema：任何"只有一个 seq 列"的唯一索引都不许存在——`events(seq)` 唯一 = 全局号序复活。
	//    判据写成**逐个索引看定义**而不是查名字：改名（`idx_events_global`）同样是复活。
	idxRows, err := db.Raw(`SELECT name, sql FROM sqlite_master
		WHERE type='index' AND tbl_name='events' AND sql IS NOT NULL`).Rows()
	if err != nil {
		t.Fatalf("读 events 的索引定义: %v", err)
	}
	seen := 0
	// 只允许"列集合里带 session_id"的唯一索引。
	uniqueOnSeqAlone := regexp.MustCompile(`(?i)UNIQUE.*\(\s*seq\s*\)`)
	for idxRows.Next() {
		var name string
		var sql *string
		if err := idxRows.Scan(&name, &sql); err != nil {
			t.Fatalf("扫 events 索引: %v", err)
		}
		seen++
		if sql == nil {
			continue
		}
		if uniqueOnSeqAlone.MatchString(*sql) {
			t.Fatalf("`events` 上出现了只对 `seq` 的 UNIQUE 索引 `%s`：%s\n"+
				"v21 起位次是**会话内**的量（`UNIQUE(session_id, seq)`）——全局唯一的 seq 会让第二个会话"+
				"的第 0 条写不进去，或者让号序悄悄变回全局", name, *sql)
		}
	}
	if err := idxRows.Close(); err != nil {
		t.Fatalf("关闭索引游标: %v", err)
	}
	if seen == 0 {
		t.Fatal("`events` 上一个索引都没读到（fail-closed：判据本身失效）")
	}

	// ③ 源码：`events_last_seq` 这个全局水位只许出现在 v21 迁移的那一条 DELETE 里。
	//    其余任何地方（读或写）都是"还在按全局号序思考"。
	allowed := map[string]bool{
		filepath.Join("internal", "persistence", "store", "migrate_v21.go"): true,
	}
	for _, f := range goFilesUnder(t, filepath.Join(root, "internal")) {
		if strings.HasSuffix(f, "_test.go") || strings.Contains(f, string(filepath.Separator)+"archtest"+string(filepath.Separator)) {
			continue
		}
		src := stripLineComments(readFileOrFatal(t, f))
		if !strings.Contains(src, "events_last_seq") {
			continue
		}
		rel, err := filepath.Rel(root, f)
		if err != nil {
			t.Fatalf("算相对路径: %v", err)
		}
		if !allowed[rel] {
			t.Fatalf("%s 里出现了 `events_last_seq`：v21 起这个全局水位已经没有任何含义"+
				"（它记的是最后一个**全局**号，而号序已经是每会话的）——读它分页会静默漏事件，"+
				"写它则是在维护一个没人该读的数", rel)
		}
	}
	v21 := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "persistence", "store", "migrate_v21.go")))
	if !strings.Contains(v21, "DELETE FROM meta WHERE key = 'events_last_seq'") {
		t.Fatal("`migrate_v21.go` 里找不到 `DELETE FROM meta WHERE key = 'events_last_seq'`：" +
			"退场的水位必须被**删掉**（留着它，下一个读到的人会以为它还有效；fail-closed）")
	}

	// ④ 源码：取号只能有一个入口（`dao/event_sequence.go` 的 NextSeq），且不得用跨会话的
	//    `MAX(seq)+1` 取号——那是全局号序的另一种写法，单会话测试永远测不出来。
	seqDao := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "persistence", "dao", "event_sequence.go")))
	if !strings.Contains(seqDao, "ON CONFLICT(session_id) DO UPDATE SET seq = seq + 1") {
		t.Fatal("`internal/persistence/dao/event_sequence.go` 里找不到 `ON CONFLICT(session_id) DO UPDATE SET seq = seq + 1`：" +
			"每会话取号必须是**单条原子的 upsert**（先 SELECT 再 UPDATE 会给并发写留下重号窗口；fail-closed）")
	}
	// 判据只在**触到 events 表**的文件上生效：`definition_revisions.seq` 的 `MAX(seq)` 是
	// 另一张表的合法聚合（v3.3 把读面搬进 store 后它就在这个扫描面里），不能一并误杀。
	maxSeqSQL := regexp.MustCompile(`(?is)SELECT\s+COALESCE\(\s*MAX\(seq\)`)
	for _, f := range goFilesUnder(t, filepath.Join(root, "internal", "persistence", "store")) {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src := stripLineComments(readFileOrFatal(t, f))
		if !maxSeqSQL.MatchString(src) {
			continue
		}
		touchesEvents := strings.Contains(src, "TableNameEvent") ||
			strings.Contains(src, "FROM events") || strings.Contains(src, "`events`")
		if touchesEvents && !strings.HasSuffix(f, "migrate_v21.go") {
			t.Fatalf("%s 里按 `MAX(seq)` 给事件取号：v21 起 seq 只在会话内唯一，跨会话的 MAX 会把两个会话"+
				"的号序搅在一起（迁移里的回填是唯一允许出现它的地方）", filepath.Base(f))
		}
	}
}
