package transfer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	pkgautomation "github.com/morehao/ark-mate/internal/automation"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/testutil"
)

// sameJSONObject 逐键比对两份 JSON 正文（顺序无关；任一份不是 JSON → 立刻失败）。
// 从已删除的 agent 导入用例里搬来：它是通用的比对工具，不依赖任何被撤掉的定义域。
func sameJSONObject(t *testing.T, a, b string) bool {
	t.Helper()
	var (
		ma map[string]any
		mb map[string]any
	)
	if err := json.Unmarshal([]byte(a), &ma); err != nil {
		t.Fatalf("正文不是 JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(b), &mb); err != nil {
		t.Fatalf("正文不是 JSON: %v", err)
	}
	return reflect.DeepEqual(ma, mb)
}

// ——— M6-7：定时任务的存量搬家（导入 / 导出） ———
//
// 与前两个域最大的差别：一个文件里住着**两类东西**（用户写的定义 + 平台写回运行态）。
// 导入按 `pkgautomation.ReservedKeys` 切分，导出反向合成——所以这里的往返断言既比正文、也比运行态列。

// automationSeedDir 造一个"存量 AutomationDir"：2 条好定义（一条带运行态）+ 3 条坏定义
// + 1 个点开头的临时文件（既不算定义也不算坏定义）。
func automationSeedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cwd := t.TempDir()
	full := `{
	  "id": "u1",
	  "name": "夜跑",
	  "enabled": true,
	  "schedule": {"kind": "daily", "timezone": "Asia/Shanghai", "hour": 9, "minute": 0},
	  "target": {"agent_id": "a1", "cwd": %q, "prompt": "跑一遍巡检"},
	  "state": "inactive",
	  "last_fired_at": "2026-10-08T01:00:00+08:00",
	  "last_status": "completed",
	  "last_reason": "上一次很顺利",
	  "last_session_id": "c-1",
	  "consecutive_failures": 2,
	  "recent_runs": [
	    {"scheduled_for": "2026-10-08T01:00:00Z", "fired_at": "2026-10-08T01:00:01Z", "status": "completed", "trigger": "scheduled"}
	  ]
	}`
	write("u1.json", strings.ReplaceAll(full, "%q", `"`+cwd+`"`))
	write("u2.json", `{"name":"U2（id 取自文件名）","enabled":false,`+
		`"schedule":{"kind":"weekly","timezone":"UTC","hour":3,"minute":30,"days_of_week":[1]},`+
		`"target":{"agent_id":"a1","cwd":"`+cwd+`","prompt":"周报"}}`)
	write("broken.json", `{"id":"broken",`)
	// id 里带斜杠 = 会变成"子路径"，与写面同一道闸门（`defn.ValidDefinitionID`）→ 拒收。
	write("slash.json", `{"id":"a/b","name":"id 带斜杠"}`)
	// 保留键类型不对（`last_fired_at` 写成数字）：**报错**而不是静默丢掉——"我写了它为什么不生效"
	// 比"这条导不进来"更难排查。
	write("badrt.json", `{"id":"badrt","name":"运行态写坏了","last_fired_at":123,`+
		`"schedule":{"kind":"daily","timezone":"UTC","hour":1,"minute":0},`+
		`"target":{"agent_id":"a1","cwd":"`+cwd+`","prompt":"x"}}`)
	write(".u1.json.tmp", `{"id":"tmp"}`)
	return dir
}

func TestImportAutomationDirSplitsRuntimeColumns(t *testing.T) {
	ctx := context.Background()
	dir := automationSeedDir(t)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))

	stats, err := ImportAutomationDir(ctx, st, dir, DefinitionImportOptions{}, nil)
	if err != nil {
		t.Fatalf("ImportAutomationDir: %v", err)
	}
	if stats.Automations != 2 || stats.Invalid != 3 || stats.Inserted != 2 || stats.Existing != 0 {
		t.Fatalf("统计不符（automations=2 invalid=3 inserted=2 existing=0）: %+v", stats)
	}
	if stats.Marker == "" || !strings.HasPrefix(stats.Marker, "sha256:") {
		t.Fatalf("目录指纹应被算出: %q", stats.Marker)
	}
	rows, err := st.ListAutomationDefinitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "u1" || rows[1].ID != "u2" {
		t.Fatalf("导入的 id 集合不符: %+v", rows)
	}
	u1 := rows[0]
	// 运行态键进**列**，并且时间归一成唯一格式（UTC + RFC3339Nano）——它是认领 CAS 的比较值。
	if u1.LastFiredAt != "2026-10-07T17:00:00Z" {
		t.Fatalf("last_fired_at 未归一为 UTC RFC3339Nano: %q", u1.LastFiredAt)
	}
	if u1.LastStatus != "completed" || u1.LastReason != "上一次很顺利" ||
		u1.LastSessionID != "c-1" || u1.ConsecutiveFailures != 2 {
		t.Fatalf("运行态列不符: %+v", u1)
	}
	if !strings.Contains(u1.RecentRuns, `"completed"`) {
		t.Fatalf("recent_runs 未落库: %s", u1.RecentRuns)
	}
	// 定义正文里**不得**留着运行态键（P-10：结构上不可写），`state` 是定义键要留着。
	for _, k := range pkgautomation.ReservedKeys {
		if strings.Contains(u1.Body, `"`+k+`"`) {
			t.Fatalf("定义正文不该有运行态键 %s: %s", k, u1.Body)
		}
	}
	if !strings.Contains(u1.Body, `"state": "inactive"`) {
		t.Fatalf("state 是定义键，应留在正文里: %s", u1.Body)
	}
	// 导入动作留痕（actor=import，action=import），逐条一行。
	revs, err := st.ListDefinitionRevisions(ctx, model.DefinitionKindAutomation, "u1")
	if err != nil || len(revs) != 1 || revs[0].Action != model.DefinitionActionImport ||
		revs[0].Actor != model.DefinitionActorImport {
		t.Fatalf("导入审计不符: %+v err=%v", revs, err)
	}
}

func TestImportAutomationDirBoundsRecentRuns(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cwd := t.TempDir()
	runs := make([]string, 0, pkgautomation.RecentRunsMax+3)
	for i := 0; i < pkgautomation.RecentRunsMax+3; i++ {
		runs = append(runs, `{"status":"completed","trigger":"scheduled"}`)
	}
	body := `{"id":"many","name":"运行史很长","enabled":true,` +
		`"schedule":{"kind":"daily","timezone":"UTC","hour":1,"minute":0},` +
		`"target":{"agent_id":"a1","cwd":"` + cwd + `","prompt":"x"},` +
		`"recent_runs":[` + strings.Join(runs, ",") + `]}`
	if err := os.WriteFile(filepath.Join(dir, "many.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	if _, err := ImportAutomationDir(ctx, st, dir, DefinitionImportOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	row, _, _ := st.GetAutomationDefinition(ctx, "many")
	var got []pkgautomation.RunRecord
	if err := json.Unmarshal([]byte(row.RecentRuns), &got); err != nil {
		t.Fatalf("recent_runs 不是数组: %v（%s）", err, row.RecentRuns)
	}
	if len(got) != pkgautomation.RecentRunsMax {
		t.Fatalf("运行史列有界（%d），got %d", pkgautomation.RecentRunsMax, len(got))
	}
}

func TestImportAutomationDirAutoIsOneShotButExplicitReruns(t *testing.T) {
	ctx := context.Background()
	dir := automationSeedDir(t)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))

	first, err := ImportAutomationDir(ctx, st, dir, DefinitionImportOptions{Auto: true}, nil)
	if err != nil || first.Already || first.Inserted != 2 {
		t.Fatalf("首次自动导入不符: %+v err=%v", first, err)
	}
	// 目录没变 → 整体跳过（**一个字节都不写**）：用户删掉的行不会自己回来。
	second, err := ImportAutomationDir(ctx, st, dir, DefinitionImportOptions{Auto: true}, nil)
	if err != nil || !second.Already || second.Inserted != 0 {
		t.Fatalf("二次自动导入应整体跳过: %+v err=%v", second, err)
	}
	row, _, _ := st.GetAutomationDefinition(ctx, "u1")
	if ok, err := st.DeleteAutomationDefinition(ctx, "u1", row.Revision, "rest"); err != nil || !ok {
		t.Fatalf("删行: ok=%v err=%v", ok, err)
	}
	if _, err := ImportAutomationDir(ctx, st, dir, DefinitionImportOptions{Auto: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := st.GetAutomationDefinition(ctx, "u1"); found {
		t.Fatal("目录没变时自动导入不得把删掉的行带回来")
	}
	// 显式导入不看指纹、只看行：文件还在 → 补回来（并带回它当时的运行态）。
	explicit, err := ImportAutomationDir(ctx, st, dir, DefinitionImportOptions{}, nil)
	if err != nil || explicit.Already || explicit.Inserted != 1 || explicit.Existing != 1 {
		t.Fatalf("显式导入应按行判重补一条: %+v err=%v", explicit, err)
	}
	back, _, _ := st.GetAutomationDefinition(ctx, "u1")
	if back.LastStatus != "completed" || back.ConsecutiveFailures != 2 {
		t.Fatalf("补回来的行应带上文件里的运行态: %+v", back)
	}
}

func TestImportAutomationDirNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	dir := automationSeedDir(t)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	// 面板上改过的那一版（含平台写回过的运行态）。
	if _, err := ImportAutomationDir(ctx, st, dir, DefinitionImportOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	row, _, _ := st.GetAutomationDefinition(ctx, "u1")
	panelBody := `{"id":"u1","name":"面板上改过的","enabled":true,` +
		`"schedule":{"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0},` +
		`"target":{"agent_id":"a1","cwd":"/tmp","prompt":"面板改的 prompt"},"state":"active"}`
	if ok, err := st.UpsertAutomationDefinition(ctx, "u1", panelBody, row.Revision, "rest"); err != nil || !ok {
		t.Fatalf("面板改一版: ok=%v err=%v", ok, err)
	}
	if ok, err := st.SaveAutomationRuntime(ctx, "u1", row.LastFiredAt, model.AutomationRuntimeState{LastFiredAt: "2026-10-09T01:00:00Z", LastStatus: pkgautomation.RunCompleted}); err != nil || !ok {
		t.Fatalf("面板跑一轮: ok=%v err=%v", ok, err)
	}

	stats, err := ImportAutomationDir(ctx, st, dir, DefinitionImportOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Inserted != 0 || stats.Existing != 2 {
		t.Fatalf("两条都已在库里 → 一条都不该补: %+v", stats)
	}
	after, _, _ := st.GetAutomationDefinition(ctx, "u1")
	if !strings.Contains(after.Body, "面板上改过的") || after.LastFiredAt != "2026-10-09T01:00:00Z" {
		t.Fatalf("导入绝不覆盖：面板那一版（含运行态）必须原样留着: %+v", after)
	}
	if revs, _ := st.ListDefinitionRevisions(ctx, model.DefinitionKindAutomation, "u1"); len(revs) != 2 {
		t.Fatalf("被跳过的行不该产生审计: %+v", revs)
	}
}

func TestExportAutomationDirRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := automationSeedDir(t)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	if _, err := ImportAutomationDir(ctx, st, src, DefinitionImportOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	stats, err := ExportAutomationDir(ctx, st, dst, false, nil)
	if err != nil || stats.Written != 2 || stats.Failed != 0 {
		t.Fatalf("导出计数不符: %+v err=%v", stats, err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "u1.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 导出物 = 定义键 + 运行态键，且时间已归一（UTC）。
	for _, want := range []string{`"last_fired_at": "2026-10-07T17:00:00Z"`, `"last_status": "completed"`,
		`"consecutive_failures": 2`, `"recent_runs"`, `"state": "inactive"`, `"id": "u1"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("导出物缺少 %s:\n%s", want, b)
		}
	}
	// 两次导出逐字节相同（导出物的唯一形态）。
	if _, err := ExportAutomationDir(ctx, st, dst, false, nil); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(dst, "u1.json"))
	if string(again) != string(b) {
		t.Fatalf("两次导出应逐字节相同:\n%s\n%s", b, again)
	}
	// 往返：导出的目录再导进一个**空库**，行（定义 + 运行态）逐字段一致，再导出逐字节相同。
	st2 := testutil.OpenStore(t, testutil.TempDBPath(t))
	if _, err := ImportAutomationDir(ctx, st2, dst, DefinitionImportOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	a, _, _ := st.GetAutomationDefinition(ctx, "u1")
	c, _, _ := st2.GetAutomationDefinition(ctx, "u1")
	if !sameJSONObject(t, a.Body, c.Body) {
		t.Fatalf("往返后定义正文语义不一致:\n%s\n%s", a.Body, c.Body)
	}
	if a.LastFiredAt != c.LastFiredAt || a.LastStatus != c.LastStatus ||
		a.ConsecutiveFailures != c.ConsecutiveFailures || a.RecentRuns != c.RecentRuns {
		t.Fatalf("往返后运行态列不一致:\n%+v\n%+v", a, c)
	}
	dst2 := t.TempDir()
	if _, err := ExportAutomationDir(ctx, st2, dst2, false, nil); err != nil {
		t.Fatal(err)
	}
	rt, _ := os.ReadFile(filepath.Join(dst2, "u1.json"))
	if string(rt) != string(b) {
		t.Fatalf("导出 → 空库导入 → 再导出应逐字节相同:\n%s\n%s", b, rt)
	}
}

func TestExportAutomationDirDryRun(t *testing.T) {
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	if _, err := ImportAutomationDir(ctx, st, automationSeedDir(t), DefinitionImportOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	stats, err := ExportAutomationDir(ctx, st, dst, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written != 2 || !stats.DryRun {
		t.Fatalf("干跑计数不符: %+v", stats)
	}
	if entries, _ := os.ReadDir(dst); len(entries) != 0 {
		t.Fatalf("--dry-run 不得写盘: %v", entries)
	}
}

func TestImportExportAutomationDirRequireStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := ImportAutomationDir(ctx, nil, dir, DefinitionImportOptions{}, nil); err == nil {
		t.Fatal("导入需要持久层")
	}
	if _, err := ExportAutomationDir(ctx, nil, dir, false, nil); err == nil {
		t.Fatal("导出需要持久层")
	}
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	stats, err := ImportAutomationDir(ctx, st, "", DefinitionImportOptions{}, nil)
	if err != nil || stats.Dir != "" || stats.Inserted != 0 {
		t.Fatalf("空目录的导入应是无声空操作: %+v err=%v", stats, err)
	}
	if _, err := ExportAutomationDir(ctx, st, "", false, nil); err == nil {
		t.Fatal("空目录的导出应报错")
	}
}

// TestImportAutomationDirKeepsBadDefinitionLoadable 导入不做可执行性判定：
// schedule/target 有问题的定义照样进库，由**装载期**留成带 error 的列表项（与文件模式逐字一致）。
func TestImportAutomationDirKeepsBadDefinitionLoadable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// 缺 target.prompt（Validate 会拒）——但它是一条**完整**的 JSON 定义，导入不该吞掉它。
	body := `{"id":"nocwd","name":"缺 prompt","enabled":true,` +
		`"schedule":{"kind":"daily","timezone":"UTC","hour":1,"minute":0},` +
		`"target":{"agent_id":"a1","cwd":"/tmp"}}`
	if err := os.WriteFile(filepath.Join(dir, "nocwd.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	stats, err := ImportAutomationDir(ctx, st, dir, DefinitionImportOptions{}, nil)
	if err != nil || stats.Inserted != 1 || stats.Invalid != 0 {
		t.Fatalf("不可执行但结构完好的定义应照常导入: %+v err=%v", stats, err)
	}
	if _, err := ExportAutomationDir(ctx, st, t.TempDir(), true, nil); err != nil {
		t.Fatalf("导出不该因为定义不可执行而失败: %v", err)
	}
}

// TestImportAutomationDirClockSkewNormalized 同一时刻的不同写法归一后是**同一个 CAS 判据**。
func TestImportAutomationDirClockSkewNormalized(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cwd := t.TempDir()
	body := `{"id":"tz","name":"时区写法","enabled":true,` +
		`"schedule":{"kind":"daily","timezone":"UTC","hour":1,"minute":0},` +
		`"target":{"agent_id":"a1","cwd":"` + cwd + `","prompt":"x"},` +
		`"last_fired_at":"2026-10-08T09:00:00+08:00"}`
	if err := os.WriteFile(filepath.Join(dir, "tz.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	if _, err := ImportAutomationDir(ctx, st, dir, DefinitionImportOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	row, _, _ := st.GetAutomationDefinition(ctx, "tz")
	want := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if row.LastFiredAt != want {
		t.Fatalf("+08:00 的写法应归一成 %q，got %q", want, row.LastFiredAt)
	}
}
