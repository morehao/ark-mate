package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/testutil"
)

// ——— M6-6：agent 定义的存量搬家（导入 / 导出） ———

// agentSeedDir 造一个"存量 AgentDir"：2 条好定义 + 1 个坏 JSON + 1 个保留前缀 id，
// 外加两个**目录级配置**文件（它们必须被跳过，而不是被当成 agent 定义）。
func agentSeedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("u1.json", `{"id":"u1","name":"U1","runtimes":["stub"],"provider":"openai:gpt-4o"}`)
	write("u2.json", `{"name":"U2（id 取自文件名）","runtimes":["dsh"]}`)
	write("broken.json", `{"id":"broken",`)
	write("agent-legacy.json", `{"id":"agent-legacy","name":"保留前缀","runtimes":["stub"]}`)
	write("providers.json", `{"p1":{"type":"openai","model":"gpt-4o"}}`)
	write("default.json", `{"default_agent":"u1"}`)
	return dir
}

// sameJSONObject 两个 JSON 对象正文是否语义相同（键序/空白无关）。
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

func TestImportAgentDirCountsAndDefaultSelection(t *testing.T) {
	ctx := context.Background()
	dir := agentSeedDir(t)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))

	stats, err := ImportAgentDir(ctx, st, dir, DefinitionImportOptions{}, nil)
	if err != nil {
		t.Fatalf("ImportAgentDir: %v", err)
	}
	if stats.Agents != 2 || stats.Invalid != 2 || stats.Inserted != 2 || stats.Existing != 0 {
		t.Fatalf("统计不符（agents=2 invalid=2 inserted=2 existing=0）: %+v", stats)
	}
	if stats.DefaultAgent != "u1" {
		t.Fatalf("default.json 的显式选择应被采纳: %+v", stats)
	}
	if id, ok, _ := st.GetDefaultAgentID(ctx); !ok || id != "u1" {
		t.Fatalf("meta.default_agent 不符: id=%q ok=%v", id, ok)
	}
	if stats.Marker == "" || !strings.HasPrefix(stats.Marker, "sha256:") {
		t.Fatalf("目录指纹应被算出: %q", stats.Marker)
	}

	// id 取自文件名（文件里没写 id）；正文里没有 providers.json / default.json。
	rows, err := st.ListAgentDefinitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "u1,u2" {
		t.Fatalf("导入的 id 集合不符: %v", ids)
	}
	for _, r := range rows {
		if strings.Contains(r.Body, "providers.json") {
			t.Fatalf("目录级配置文件不该被导入: %s", r.Body)
		}
	}
	// 导入是 **typed 归一**（落库正文按 `agentDefinitionFile` 重编），不是原样搬字节：老定义里
	// 已下线的 `runtimes` 键会被丢掉——留着它会让"这个键还有意义"看起来成立。
	for _, id := range []string{"u1", "u2"} {
		row, _, _ := st.GetAgentDefinition(ctx, id)
		if strings.Contains(row.Body, "runtimes") {
			t.Fatalf("%s 正文里不该留下已下线的 runtimes 键: %s", id, row.Body)
		}
	}
	// id 取自文件名的那条：name 原样保留，id 由文件名回填。
	u2, _, _ := st.GetAgentDefinition(ctx, "u2")
	if !strings.Contains(u2.Body, "U2（id 取自文件名）") || !strings.Contains(u2.Body, `"id": "u2"`) {
		t.Fatalf("u2 正文应保留 name 并由文件名回填 id: %s", u2.Body)
	}
	// 导入动作留痕（actor=import，action=import）。
	revs, err := st.ListDefinitionRevisions(ctx, model.DefinitionKindAgent, "u1")
	if err != nil || len(revs) != 1 || revs[0].Action != model.DefinitionActionImport || revs[0].Actor != model.DefinitionActorImport {
		t.Fatalf("导入审计不符: %+v err=%v", revs, err)
	}
}

func TestImportAgentDirAutoIsOneShotButExplicitReruns(t *testing.T) {
	ctx := context.Background()
	dir := agentSeedDir(t)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))

	first, err := ImportAgentDir(ctx, st, dir, DefinitionImportOptions{Auto: true}, nil)
	if err != nil || first.Already || first.Inserted != 2 {
		t.Fatalf("首次自动导入不符: %+v err=%v", first, err)
	}
	// 目录没变 → 整体跳过（**一个字节都不写**）：用户删掉的行不会自己回来。
	second, err := ImportAgentDir(ctx, st, dir, DefinitionImportOptions{Auto: true}, nil)
	if err != nil || !second.Already || second.Inserted != 0 {
		t.Fatalf("二次自动导入应整体跳过: %+v err=%v", second, err)
	}

	if ok, err := st.DeleteAgentDefinition(ctx, "u1", 1, "rest"); err != nil || !ok {
		t.Fatalf("删行: ok=%v err=%v", ok, err)
	}
	if _, err := ImportAgentDir(ctx, st, dir, DefinitionImportOptions{Auto: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := st.GetAgentDefinition(ctx, "u1"); found {
		t.Fatal("目录没变时自动导入不得把删掉的行带回来")
	}
	// 显式导入不看指纹、只看行：文件还在 → 补回来（这正是"新丢一个文件跑一次"的同一条路径）。
	explicit, err := ImportAgentDir(ctx, st, dir, DefinitionImportOptions{}, nil)
	if err != nil || explicit.Already || explicit.Inserted != 1 || explicit.Existing != 1 {
		t.Fatalf("显式导入应按行判重补一条: %+v err=%v", explicit, err)
	}
	if _, found, _ := st.GetAgentDefinition(ctx, "u1"); !found {
		t.Fatal("显式导入应把缺失的行补回来")
	}
}

func TestImportAgentDirNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	dir := agentSeedDir(t)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	mustSeedAgentRow(t, st, "u1", `{"id":"u1","name":"面板上改过的"}`)
	if ok, err := st.UpsertAgentDefinition(ctx, "u1", `{"id":"u1","name":"面板上改过的 v2"}`, 1, "rest"); err != nil || !ok {
		t.Fatalf("面板改一版: ok=%v err=%v", ok, err)
	}

	stats, err := ImportAgentDir(ctx, st, dir, DefinitionImportOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Inserted != 1 || stats.Existing != 1 {
		t.Fatalf("应只补缺失的那条: %+v", stats)
	}
	row, _, _ := st.GetAgentDefinition(ctx, "u1")
	if !strings.Contains(row.Body, "面板上改过的 v2") || row.Revision != 2 {
		t.Fatalf("导入绝不覆盖：面板那一版必须原样留着: rev=%d body=%s", row.Revision, row.Body)
	}
	if revs, _ := st.ListDefinitionRevisions(ctx, model.DefinitionKindAgent, "u1"); len(revs) != 2 {
		t.Fatalf("被跳过的行不该产生审计: %+v", revs)
	}
}

func TestExportAgentDirRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := agentSeedDir(t)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	if _, err := ImportAgentDir(ctx, st, src, DefinitionImportOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	// 面板改一版：导出物必须来自 DB，而不是目录里那份旧文件。
	if ok, err := st.UpsertAgentDefinition(ctx, "u1", `{"id":"u1","name":"面板改的","provider":"openai:gpt-4o"}`, 1, "rest"); err != nil || !ok {
		t.Fatal(err)
	}

	dst := t.TempDir()
	stats, err := ExportAgentDir(ctx, st, dst, false, nil)
	if err != nil {
		t.Fatalf("ExportAgentDir: %v", err)
	}
	if stats.Written != 3 || stats.Failed != 0 { // u1 + u2 + default.json
		t.Fatalf("导出计数不符（2 条定义 + default.json）: %+v", stats)
	}
	b, err := os.ReadFile(filepath.Join(dst, "u1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "面板改的") {
		t.Fatalf("导出物应来自 DB: %s", b)
	}
	def, err := os.ReadFile(filepath.Join(dst, defaultAgentFile))
	if err != nil {
		t.Fatalf("「当前 Agent」应一并导出: %v", err)
	}
	var doc struct {
		DefaultAgent string `json:"default_agent"`
	}
	if err := json.Unmarshal(def, &doc); err != nil || doc.DefaultAgent != "u1" {
		t.Fatalf("default.json 内容不符: %s (%v)", def, err)
	}
	// 往返：导出的目录再导进另一个库，行集合与正文一致。
	st2 := testutil.OpenStore(t, testutil.TempDBPath(t))
	if _, err := ImportAgentDir(ctx, st2, dst, DefinitionImportOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	a, _, _ := st.GetAgentDefinition(ctx, "u1")
	c, _, _ := st2.GetAgentDefinition(ctx, "u1")
	// 正文比**语义**而不是字节：导出会把行正文归一为写面同形的缩进（手工 SQL 写进去的紧凑
	// 一行也会被摊开）——这正是导出物"两次导出逐字节相同"的来源。
	if !sameJSONObject(t, a.Body, c.Body) {
		t.Fatalf("往返后正文语义不一致:\n%s\n%s", a.Body, c.Body)
	}
	if _, found, _ := st2.GetAgentDefinition(ctx, "default"); found {
		t.Fatal("default.json 不该被当成 agent 定义导入")
	}
}

func TestExportAgentDirDryRunAndReservedID(t *testing.T) {
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	mustSeedAgentRow(t, st, "u1", `{"id":"u1","name":"U1"}`)
	// id 合法但与保留文件同名 → 不导出（否则会覆盖 providers.json 这类目录级配置）。
	mustSeedAgentRow(t, st, "providers", `{"id":"providers","name":"撞名"}`)

	dst := t.TempDir()
	stats, err := ExportAgentDir(ctx, st, dst, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written != 1 || stats.Failed != 1 {
		t.Fatalf("干跑计数不符（1 条将写出 + 1 条撞保留名）: %+v", stats)
	}
	if entries, _ := os.ReadDir(dst); len(entries) != 0 {
		t.Fatalf("--dry-run 不得写盘: %v", entries)
	}
}

func TestImportExportAgentDirRequireStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := ImportAgentDir(ctx, nil, dir, DefinitionImportOptions{}, nil); err == nil {
		t.Fatal("导入需要持久层")
	}
	if _, err := ExportAgentDir(ctx, nil, dir, false, nil); err == nil {
		t.Fatal("导出需要持久层")
	}
	// 目录没配 = 没有存量可搬（不是错误）；导出没目录才是错误（不知道该写到哪）。
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	stats, err := ImportAgentDir(ctx, st, "", DefinitionImportOptions{}, nil)
	if err != nil || stats.Dir != "" || stats.Inserted != 0 {
		t.Fatalf("空目录的导入应是无声空操作: %+v err=%v", stats, err)
	}
	if _, err := ExportAgentDir(ctx, st, "", false, nil); err == nil {
		t.Fatal("空目录的导出应报错")
	}
}
