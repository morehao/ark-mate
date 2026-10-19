package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件是 v15「破坏性重建仅此一次」的两条守卫：
//
//   - 守卫⑤ 迁移步只许用**历史字面量**（实体改名不得跟随进历史步骤）；
//   - 守卫⑥ 「仅此一次」的机器判据（不给内核加能力 / DROP 清单白名单 / 显式标注 / 清单对应）。
//
// 为什么这两条必须有：v15 的 `DROP TABLE` 是**一次性例外**（用户 2026-10-11 拍板），
// 而"例外退化成 SOP"的路径上没有任何别的测试会变红——半年后再改 schema 的人看到
// `DROP TABLE` + `backup: "v15"`，会把它当成既有手法照抄，而每次重建都在静默清空用户历史。

// migrationStepFiles 返回 `internal/store/migrate_v*.go`（版本步文件；内核本体 migrate.go 不在其中）。
//
// 测试文件（`*_test.go`）不在扫描面里：它们的职责恰恰是**用当前实体常量**去读那些表
// （"停在 v15 之前"的用例要用裸 SQL 造历史表，其余用例走 DAO），把它们算进来等于让守卫
// 与它自己的夹具打架。
func migrationStepFiles(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob(filepath.Join(backendRoot(t), "internal", "store", "migrate_v*.go"))
	if err != nil || len(all) == 0 {
		t.Fatalf("扫不到 internal/store/migrate_v*.go: %v（fail-closed）", err)
	}
	files := make([]string, 0, len(all))
	for _, f := range all {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("过滤掉测试文件后一个版本步都没有（fail-closed）")
	}
	return files
}

// sqlStatementMarkers 是"这个文件确实是迁移步"的判据（fail-closed 用）。
//
// 刻意比设计里写的 `CREATE TABLE`/`ALTER TABLE` 宽：`migrate_v15.go` 是**纯 apply 文件**
// （DDL 在 `migrate.go` 的 v15 条目里），它只有 SELECT/UPDATE/DELETE。收窄成只认 DDL
// 会让这条 fail-closed 把正确写法判红，而"扫错文件"这件事靠"至少有一条 SQL 语句"同样挡得住。
var sqlStatementMarkers = []string{
	"CREATE TABLE", "ALTER TABLE", "INSERT INTO", "UPDATE ", "DELETE FROM", "SELECT ",
}

// TestMigrationStepsUseHistoricalLiterals 守卫⑤：版本步只许写字面量。
//
// 判据：`internal/store/migrate_v*.go` 剥注释后不得出现 `model.TableName*` 常量。
//
// 不修会怎样：`migrate_v14.go` 的 `SELECT COUNT(*) FROM <表>` 在实体改名后会指向**当时还不
// 存在的表**，于是**新库第一次启动**（内核从 v1 顺序重放到最新）直接失败；而本机老库测不出来
// （v14 早已 applied，不会重放）。这正是 2026-10-11 抓到的那处隐患。
func TestMigrationStepsUseHistoricalLiterals(t *testing.T) {
	re := regexp.MustCompile(`model\.TableName[A-Za-z]+`)
	files := migrationStepFiles(t)
	checked := 0
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", path, err)
		}
		src := stripLineComments(string(raw))
		// fail-closed：文件里一条 SQL 都没有 ⇒ 要么 glob 扫错了，要么迁移被搬走了。
		if !containsAny(src, sqlStatementMarkers) {
			t.Fatalf("%s 里一条 SQL 语句都没有——版本步被搬走了？（fail-closed，避免守卫空转）", filepath.Base(path))
		}
		checked++
		if hits := re.FindAllString(src, -1); len(hits) > 0 {
			t.Fatalf("%s 里出现了 %v：**版本步只许写历史字面量**（表名/列名/索引名）。\n"+
				"实体改名会让这条历史步骤指向当时还不存在的表，新库从 v1 重放时直接启动失败"+
				"（老库测不出来——那一版早已 applied）。内核本体（migrate.go 里建/读 schema_migrations）"+
				"不在这条禁令里。", filepath.Base(path), hits)
		}
	}
	if checked == 0 {
		t.Fatal("一个版本步文件都没检查到（fail-closed）")
	}
}

// migrationDrop 是一处 `DROP TABLE` 的登记项。
type migrationDrop struct {
	File   string // 相对 internal/store 的文件名
	Table  string
	Reason string // 为什么它不算"清库重建"
}

// v15InPlaceDrops v12/v13 的「建临时表 → 灌数 → DROP → RENAME」是**同一张表的就地重建**，
// 与 v15 的"清库重建"是两件事。逐条登记（而不是给个白名单集合），是为了让"新增一处 DROP TABLE"
// 必须**显式**写一行理由。
//
// v17 起这条判据按 **(文件, 表)** 而不是只按表名匹配——此前只按表名，于是 `migrate_v17.go` 里的
// `DROP TABLE events` 因为 v15 也重建过 `events` 而**静默通过**（2026-10-12 实测）。这正是这条
// 守卫最容易腐化的方式：新加的重建只要复用一个历史表名，就再也无人需要拍板。
var v15InPlaceDrops = []migrationDrop{
	{File: "migrate_v12.go", Table: "models", Reason: "v11 的 models 表 → models_v12（同表就地重写，无数据丢失）"},
	{File: "migrate_v13.go", Table: "providers", Reason: "v12 的 providers → providers_v13（同表就地列化，无数据丢失）"},
	{File: "migrate_v13.go", Table: "models", Reason: "v12 的 models → models_v13（同表就地列化，无数据丢失）"},
	{File: "migrate_v17.go", Table: "events", Reason: "v16 的 events → events_v17（同表就地重铸身份：行数与 seq 逐行保留，**不清空任何数据**）"},
	{File: "migrate_v21.go", Table: "events", Reason: "v17 的 events → events_v21（同表就地换义位次：id 与行数逐行保留，seq 分会话重编号，**不清空任何数据**）"},
}

// v19WholeTableDrops 是**整表撤概念**的登记簿（与 v15InPlaceDrops 的「同表就地重写」是两回事）。
//
// 为什么单列一张表：就地重写不改行数（v12/v13/v17/v21 都是「建新表 → 灌数 → DROP 旧表 → RENAME」），
// 而整表撤概念**确实丢行**——`agents` 的定义行不会搬到别处。它必须 owner 单独拍板，
// 判据同样按 (文件, 表) 匹配（换个文件写同一个表名不算豁免）。
//
// v19（2026-10-14，决策 v19）：agent 概念整个并入 runtime，底座由构建期固定、用户不可自定义，
// 于是 `agents` 表被 DROP。依据是「这个域没有独立身份」——agent 的每个字段要么是派生事实
// （`is_default`/`session_count`/`default_model`）、要么是平台事实（`permission_policy`/`provider`），
// 撤掉写面后没有剩余字段可写。全文见 docs/decisions.md「决策 v19」。
var v19WholeTableDrops = []migrationDrop{
	{File: "migrate.go", Table: "agents", Reason: "v19：agent 概念并入 runtime，定义面整体删除（决策 v19 拍板）——" +
		"行**确实**丢弃（定义行 + `default_agent`/`imported_agents` 两个 meta 键），所以它登记在这里而不是 v15InPlaceDrops"},
}

// migrationStructFields 是 `migration` 结构体**允许**的字段全集（C1：不给内核加能力）。
//
// 加 `destructive`/`rebuild`/`drop` 这类字段就是"把一次性例外变成可复用机制"的第一步。
var migrationStructFields = []string{"version", "name", "statements", "apply", "backup"}

// TestDestructiveRebuildIsOneShot 守卫⑥：v15 的破坏性重建**仅此一次**，且不得长成 SOP。
//
// 四段判据（对应设计 §8.3.2 的 C1/C2/C3 + §8.5 的 R6）：
//  1. `migration` 结构体字段集合 == 白名单（内核本身零改动）；
//  2. `DROP TABLE` 只允许出现在登记过的地方（v15 自己那 7 张 + v12/v13/v17/v21 的就地重建）；
//     匹配按 **(文件, 表)**——只按表名匹配时，新迁移复用历史表名即可静默通过（v17 实测）。
//  3. v15 的 `name` 写明"破坏性重建（仅此一次）"、`backup == "v15"`；
//  4. R6：v15 的 DROP 清单 ↔ 元数据清理清单**一一对应**（`v15TablePlan`），
//     且"未被 DROP 但重铸主键"的表（`v15IDRecastPlan`）与 DROP 清单不相交、引用清单非空。
//
// fail-closed：解析不到 `migration` 结构体、或 v15 条目 → 红。
func TestDestructiveRebuildIsOneShot(t *testing.T) {
	storeDir := filepath.Join(backendRoot(t), "internal", "store")

	// ① 内核结构体零改动。
	migrateRaw, err := os.ReadFile(filepath.Join(storeDir, "migrate.go"))
	if err != nil {
		t.Fatalf("读不到 migrate.go: %v", err)
	}
	migrateSrc := stripLineComments(string(migrateRaw))
	structRe := regexp.MustCompile(`(?s)type migration struct \{(.*?)\n\}`)
	sm := structRe.FindStringSubmatch(migrateSrc)
	if sm == nil {
		t.Fatal("解析不到 `type migration struct`（fail-closed）：内核结构体被改名/挪走了？")
	}
	var gotFields []string
	for _, line := range strings.Split(sm[1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if f := strings.Fields(line); len(f) > 0 {
			gotFields = append(gotFields, f[0])
		}
	}
	sort.Strings(gotFields)
	wantFields := append([]string(nil), migrationStructFields...)
	sort.Strings(wantFields)
	if strings.Join(gotFields, ",") != strings.Join(wantFields, ",") {
		t.Fatalf("`migration` 结构体的字段集合变了：\n got  %v\n want %v\n"+
			"「仅此一次」的第一条就是**不给内核加能力**——`destructive`/`rebuild`/`drop` 这类字段"+
			"会把一次性例外变成可复用机制（设计 §8.3.2 C1）。", gotFields, wantFields)
	}

	// ② DROP TABLE 白名单（按 (文件, 表) 归属：v15 的清单只豁免 **migrate.go 自己**那些，
	//    别处复用一个历史表名不构成豁免——见 v15InPlaceDrops 的注释）。
	v15Drops := v15DropTables(t, migrateSrc)
	if len(v15Drops) == 0 {
		t.Fatal("v15 的 statements 里解析不到 `DROP TABLE`（fail-closed）：v15 被改名/挪走了？")
	}
	allDrops := collectDropTables(t, storeDir)
	for _, d := range allDrops {
		if d.File == "migrate.go" && containsStr(v15Drops, d.Table) {
			continue
		}
		if isRegisteredInPlaceDrop(d) {
			continue
		}
		if isRegisteredWholeTableDrop(d) {
			continue
		}
		t.Fatalf("%s 里的 `DROP TABLE %s` 不在白名单里：\n"+
			"  · v15 自己（migrate.go）的清单 = %v（只有它允许\"清库重建\"）\n"+
			"  · v12/v13/v17/v21 的\"建临时表 → 灌数 → DROP → RENAME\"必须登记进 v15InPlaceDrops（写明理由），\n"+
			"    且判据按 (文件, 表) 匹配——**换个文件写同一个表名不算豁免**\n"+
			"新增一处 `DROP TABLE` = 又一次静默清空用户历史，必须 owner 单独拍板（设计 §8.3.2 C2）。",
			d.File, d.Table, v15Drops)
	}

	// ③ 显式标注。
	nameRe := regexp.MustCompile(`(?s)version:\s*15,.*?name:\s*"([^"]*)"`)
	nm := nameRe.FindStringSubmatch(migrateSrc)
	if nm == nil {
		t.Fatal("解析不到 v15 的 `name`（fail-closed）")
	}
	for _, must := range []string{"破坏性重建", "仅此一次"} {
		if !strings.Contains(nm[1], must) {
			t.Fatalf("v15 的 name 必须写明 %q（实际 %q）——显式标注是把\"例外\"写死的第三步", must, nm[1])
		}
	}
	backupRe := regexp.MustCompile(`(?s)version:\s*15,.*?backup:\s*"([^"]*)"`)
	bm := backupRe.FindStringSubmatch(migrateSrc)
	if bm == nil || bm[1] != "v15" {
		t.Fatalf("v15 的 `backup` 必须是 \"v15\"（`mate doctor` 靠它打印回退路径），实际 %v", bm)
	}

	// ④ R6：DROP 清单 ↔ 清理清单一一对应。
	plan := parseV15TablePlan(t, storeDir)
	if len(plan) == 0 {
		t.Fatal("解析不到 `v15TablePlan`（fail-closed）：R6 的登记表被删了？")
	}
	var planTables []string
	for _, e := range plan {
		if strings.TrimSpace(e.Metadata) == "" {
			t.Fatalf("v15TablePlan 的 %s 没有写元数据处置——没有也要显式写\"无\"（R6）", e.Table)
		}
		planTables = append(planTables, e.Table)
	}
	sort.Strings(planTables)
	sortedDrops := append([]string(nil), v15Drops...)
	sort.Strings(sortedDrops)
	if strings.Join(planTables, ",") != strings.Join(sortedDrops, ",") {
		t.Fatalf("v15 的 DROP 清单与 `v15TablePlan` 对不上：\n DROP  %v\n plan  %v\n"+
			"每张被重建的表都要登记\"它有没有元数据要清\"（R6）——清单漏一张，那张表的元数据就会静默留在旧水位上。",
			sortedDrops, planTables)
	}
	// 登记的清理项必须真的出现在 apply 里：删掉清理代码而留着登记表，是 R6 最常见的腐化方式
	// （反证：把 `model.MetaKeyImportMarker` 从 DELETE 的参数里删掉 → 这条红）。
	//
	// 判据比对的是 **Go 常量名**而不是字面值：apply 里用的是 `model.MetaKey*`，而登记表写的是
	// 人类可读的键名（`meta.imported_transcripts`）——只查字面值会让这条判据永远为真（注释里
	// 就写着那个键名），那正是"空转的守卫"。
	applySrc := stripLineComments(readFileOrFatal(t, filepath.Join(storeDir, "migrate_v15.go")))
	metaConst := readMetaKeyConstants(t)
	for _, e := range plan {
		if e.Metadata == "无" {
			continue
		}
		value := strings.TrimPrefix(e.Metadata, "meta.")
		constName, ok := metaConst[value]
		if !ok {
			t.Fatalf("`v15TablePlan` 登记了 %s，但 `internal/model/enum.go` 里没有值为 %q 的 MetaKey 常量"+
				"——登记表写了一个不存在的键（R6 的登记必须能落到代码上）", e.Metadata, value)
		}
		if !strings.Contains(applySrc, "model."+constName) {
			t.Fatalf("`v15TablePlan` 登记了 %s 的清理项 %s（`model.%s`），但 migrate_v15.go 里找不到它："+
				"删掉清理而留着登记 = R6 腐化（§8.4 ① 的静默失效就是它）", e.Table, e.Metadata, constName)
		}
	}

	// ⑤ R7：重铸清单与 DROP 清单不相交，且引用清单非空。
	recast := parseV15RecastPlan(t, storeDir)
	if len(recast) == 0 {
		t.Fatal("解析不到 `v15IDRecastPlan`（fail-closed）")
	}
	for _, r := range recast {
		if containsStr(v15Drops, r.Table) {
			t.Fatalf("%s 同时出现在 DROP 清单与重铸清单里——"+
				"重建 + 重铸是两种互斥的处置（要么整体清空、要么保行重铸），同时登记说明有一侧是错的", r.Table)
		}
		if len(r.References) == 0 {
			t.Fatalf("%s 重铸了主键却没有登记引用重写清单（R7）：没有引用的也要显式写一条并说明",
				r.Table)
		}
		for _, ref := range r.References {
			key := strings.TrimPrefix(ref, "meta.")
			if !strings.Contains(applySrc, key) {
				t.Fatalf("%s 的引用 %s 没有在 migrate_v15.go 里被重写（R7：主键重铸 ⇒ 引用必须同事务改写）",
					r.Table, ref)
			}
		}
	}

	// ⑥ `mate migrate` 不得出现 `--rebuild` 之类的入口（C1）。
	//
	// 扫描面跟着实现走：v3.2 刀 10 把命令体搬进了 `internal/maintenance`（命令层只剩分发）。
	// 判据仍然是"flag 集合恰好是 db,transcripts"——换位置不改判据。
	cmdSrc := stripLineComments(readFileOrFatal(t,
		filepath.Join(backendRoot(t), "internal", "maintenance", "migrate.go")))
	flagRe := regexp.MustCompile(`fs\.(?:String|Bool|Int)\(\s*"([^"]+)"`)
	var flags []string
	for _, m := range flagRe.FindAllStringSubmatch(cmdSrc, -1) {
		flags = append(flags, m[1])
	}
	sort.Strings(flags)
	if strings.Join(flags, ",") != "db,transcripts" {
		t.Fatalf("`mate migrate` 的 flag 集合变了：got %v want [db transcripts]\n"+
			"给它加 `--rebuild` 这类开关就是把破坏性重建做成了可调用的入口（设计 §8.3.2 C1）。", flags)
	}
}

// v15DropTables 从 v15 条目的 statements 里取出 `DROP TABLE` 的表名。
func v15DropTables(t *testing.T, migrateSrc string) []string {
	t.Helper()
	start := strings.Index(migrateSrc, "version: 15,")
	if start < 0 {
		t.Fatal("migrate.go 里找不到 `version: 15,`（fail-closed）")
	}
	rest := migrateSrc[start:]
	if end := strings.Index(rest[1:], "version: "); end >= 0 {
		rest = rest[:end+1]
	}
	re := regexp.MustCompile("`DROP TABLE (?:IF EXISTS )?([a-z_]+)`")
	var out []string
	for _, m := range re.FindAllStringSubmatch(rest, -1) {
		out = append(out, m[1])
	}
	return out
}

// collectDropTables 扫 `internal/store/migrate*.go` 里全部 `DROP TABLE`。
func collectDropTables(t *testing.T, storeDir string) []migrationDrop {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(storeDir, "migrate*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("扫不到 migrate*.go: %v（fail-closed）", err)
	}
	re := regexp.MustCompile("`DROP TABLE (?:IF EXISTS )?([a-z_]+)`")
	var out []migrationDrop
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src := stripLineComments(readFileOrFatal(t, path))
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			out = append(out, migrationDrop{File: filepath.Base(path), Table: m[1]})
		}
	}
	return out
}

// isRegisteredInPlaceDrop 判断一处 DROP 是否登记为"同一张表的就地重建"。
func isRegisteredInPlaceDrop(d migrationDrop) bool {
	for _, w := range v15InPlaceDrops {
		if strings.TrimSpace(w.Reason) == "" {
			continue
		}
		if w.File == d.File && w.Table == d.Table {
			return true
		}
	}
	return false
}

// isRegisteredWholeTableDrop 判断一处 DROP 是否登记为"整表撤概念"（v19 的那一类）。
func isRegisteredWholeTableDrop(d migrationDrop) bool {
	for _, w := range v19WholeTableDrops {
		if strings.TrimSpace(w.Reason) == "" {
			continue
		}
		if w.File == d.File && w.Table == d.Table {
			return true
		}
	}
	return false
}

// v15PlanEntry 是 `v15TablePlan` 的一行。
type v15PlanEntry struct {
	Table    string
	Metadata string
}

// parseV15TablePlan 解析 `migrate_v15.go` 里的 `v15TablePlan` 字面量。
func parseV15TablePlan(t *testing.T, storeDir string) []v15PlanEntry {
	t.Helper()
	src := stripLineComments(readFileOrFatal(t, filepath.Join(storeDir, "migrate_v15.go")))
	start := strings.Index(src, "var v15TablePlan = []v15TableEntry{")
	if start < 0 {
		return nil
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		return nil
	}
	body := src[start : start+end]
	re := regexp.MustCompile(`\{Table:\s*"([a-z_]+)",\s*Metadata:\s*"([^"]*)"\}`)
	var out []v15PlanEntry
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out = append(out, v15PlanEntry{Table: m[1], Metadata: m[2]})
	}
	return out
}

// v15RecastEntry 是 `v15IDRecastPlan` 的一行。
type v15RecastEntry struct {
	Table      string
	References []string
}

// parseV15RecastPlan 解析 `migrate_v15.go` 里的 `v15IDRecastPlan` 字面量。
func parseV15RecastPlan(t *testing.T, storeDir string) []v15RecastEntry {
	t.Helper()
	src := stripLineComments(readFileOrFatal(t, filepath.Join(storeDir, "migrate_v15.go")))
	start := strings.Index(src, "var v15IDRecastPlan = []v15IDRecastEntry{")
	if start < 0 {
		return nil
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		return nil
	}
	body := src[start : start+end]
	re := regexp.MustCompile(`\{Table:\s*"([a-z_]+)",\s*References:\s*\[\]string\{([^}]*)\}\}`)
	var out []v15RecastEntry
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		var refs []string
		for _, r := range strings.Split(m[2], ",") {
			r = strings.Trim(strings.TrimSpace(r), `"`)
			if r != "" {
				refs = append(refs, r)
			}
		}
		out = append(out, v15RecastEntry{Table: m[1], References: refs})
	}
	return out
}

// readMetaKeyConstants 读 `internal/model/enum.go`，返回 meta 键**值 → 常量名**的映射。
//
// 为什么需要它：登记表写人类可读的键名（`imported_transcripts`），代码里写常量
// （`model.MetaKeyImportMarker`）——不建这条映射，R6 的"清理项真的在 apply 里"就只能靠
// 字面值匹配，而字面值恰好出现在注释里，判据会永远为真。
func readMetaKeyConstants(t *testing.T) map[string]string {
	t.Helper()
	src := stripLineComments(readFileOrFatal(t, filepath.Join(backendRoot(t), "internal", "model", "enum.go")))
	re := regexp.MustCompile(`(?m)^\s*(MetaKey\w+)\s*=\s*"([^"]+)"`)
	out := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out[m[2]] = m[1]
	}
	if len(out) == 0 {
		t.Fatal("在 internal/model/enum.go 里解析不到任何 `MetaKey*` 常量（fail-closed）")
	}
	return out
}

// readFileOrFatal 读文件，读不到即 fatal。
func readFileOrFatal(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v", path, err)
	}
	return string(raw)
}

// containsAny 判断 s 是否含 markers 里任意一项。
func containsAny(s string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}
