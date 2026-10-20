package transfer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/testutil"
)

// ——— M6-4 导入/导出验收 ———
//
// 这一组的关键不是"能导入"，而是三条边界：**幂等**（目录没变不重复动）、**绝不覆盖**
// （面板改过的行不会被文件回滚）、**往返无损**（导出→清库→导入后含未知键逐字段一致）。

// capSeedDir 造一个存量能力目录：2 条 skill（含未知键）+ 1 条 mcp + 1 个坏文件 + tools/ 一个文件。
func capSeedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, dir, "alpha", `{
  "id": "alpha",
  "name": "Alpha",
  "body": "# Alpha",
  "runtimes": ["rtx"],
  "my_note": "手写字段"
}`)
	writeSkill(t, dir, "beta", `{"id":"beta","name":"Beta","scope":"workspace"}`)
	writeSkill(t, dir, "broken", `{"id": "broken", `)
	// 非法定义（scope 非法）→ 与"读不了"一样逐条跳过，但**不整体失败**。
	writeSkill(t, dir, "bad-scope", `{"id":"bad-scope","scope":"bogus"}`)
	if err := os.MkdirAll(filepath.Join(dir, "mcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp", "files.json"),
		[]byte(`{"id":"files","transport":"sse","url":"http://localhost:9999"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// tools/ 不迁（P-1），但必须计数（静默丢弃是最坏的一种"成功"）。
	if err := os.MkdirAll(filepath.Join(dir, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tools", "web.json"),
		[]byte(`{"name":"web_search","source":"builtin"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestImportCapabilityDirCountsAndAudit(t *testing.T) {
	dir := capSeedDir(t)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	ctx := context.Background()

	stats, err := ImportCapabilityDir(ctx, st, dir, DefinitionImportOptions{}, nil)
	if err != nil {
		t.Fatalf("ImportCapabilityDir: %v", err)
	}
	if stats.Skills != 2 || stats.Mcps != 1 || stats.Invalid != 2 || stats.Tools != 1 {
		t.Fatalf("扫描计数不符（2 skill / 1 mcp / 2 坏 / 1 tools 文件）: %+v", stats)
	}
	if stats.Inserted != 3 || stats.Existing != 0 || stats.Already {
		t.Fatalf("导入计数不符: %+v", stats)
	}

	// 行与正文：未知键必须一起进来（body 是唯一权威）。
	row, found, err := st.GetSkillDefinition(ctx, "alpha")
	if err != nil || !found {
		t.Fatalf("alpha 应已导入: found=%v err=%v", found, err)
	}
	for _, want := range []string{`"name": "Alpha"`, `"my_note": "手写字段"`, `"runtimes": [`} {
		if !strings.Contains(row.Body, want) {
			t.Fatalf("导入的正文应含 %s: %s", want, row.Body)
		}
	}
	if row.Revision != 1 {
		t.Fatalf("导入的行 revision 应为 1，got %d", row.Revision)
	}
	// 坏文件与 tools 都不该留下任何痕迹。
	if _, found, _ := st.GetSkillDefinition(ctx, "broken"); found {
		t.Fatal("坏 JSON 不该被导入")
	}
	if _, found, _ := st.GetSkillDefinition(ctx, "bad-scope"); found {
		t.Fatal("过不了闸门的定义不该被导入")
	}
	// 审计：每条导入一行（action=import，actor=import），而不是一条聚合行——
	// 否则 `ListDefinitionRevisions(kind, id)` 查不到"这一行是哪来的"。
	revs, err := st.ListDefinitionRevisions(ctx, model.DefinitionKindSkill, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 1 || revs[0].Action != model.DefinitionActionImport || revs[0].Actor != model.DefinitionActorImport {
		t.Fatalf("导入审计不符: %+v", revs)
	}
	if revs[0].Digest == "" {
		t.Fatal("导入审计应带正文摘要")
	}
}

func TestImportCapabilityDirIsIdempotentAndNeverOverwrites(t *testing.T) {
	dir := capSeedDir(t)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	ctx := context.Background()

	// 装配期自动导入（看目录指纹）。
	first, err := ImportCapabilityDir(ctx, st, dir, DefinitionImportOptions{Auto: true}, nil)
	if err != nil || first.Inserted != 3 {
		t.Fatalf("首次自动导入应插 3 行: %+v err=%v", first, err)
	}
	// 面板改一版（写面路径：revision CAS，1 → 2）。
	if _, err := st.UpsertSkillDefinition(ctx, "alpha", `{"id":"alpha","name":"面板改的名字"}`, 1, "rest"); err != nil {
		t.Fatal(err)
	}

	// 第二次自动导入：目录没变 → 整体跳过，一个字节都不写，**面板那一版也不动**。
	second, err := ImportCapabilityDir(ctx, st, dir, DefinitionImportOptions{Auto: true}, nil)
	if err != nil {
		t.Fatalf("第二次自动导入: %v", err)
	}
	if !second.Already || second.Inserted != 0 {
		t.Fatalf("目录未变应整体跳过: %+v", second)
	}
	row, _, _ := st.GetSkillDefinition(ctx, "alpha")
	if !strings.Contains(row.Body, "面板改的名字") || row.Revision != 2 {
		t.Fatalf("自动导入不该覆盖写面改过的行: rev=%d body=%s", row.Revision, row.Body)
	}

	// 显式导入（不看指纹）同样不覆盖：已有行计入 Existing，缺失的补进来。
	if err := os.WriteFile(filepath.Join(dir, "skills", "gamma.json"),
		[]byte(`{"id":"gamma","name":"Gamma"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := ImportCapabilityDir(ctx, st, dir, DefinitionImportOptions{}, nil)
	if err != nil {
		t.Fatalf("显式导入: %v", err)
	}
	if third.Inserted != 1 || third.Existing != 3 {
		t.Fatalf("显式导入应补 1 条、留 3 条: %+v", third)
	}
	row, _, _ = st.GetSkillDefinition(ctx, "alpha")
	if !strings.Contains(row.Body, "面板改的名字") {
		t.Fatalf("显式导入也不该覆盖已有行: %s", row.Body)
	}
}

func TestImportExportRoundTripKeepsUnknownKeys(t *testing.T) {
	srcDir := capSeedDir(t)
	ctx := context.Background()

	src := testutil.OpenStore(t, testutil.TempDBPath(t))
	if _, err := ImportCapabilityDir(ctx, src, srcDir, DefinitionImportOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	beforeSkills, err := src.ListSkillDefinitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeMcps, err := src.ListMcpServerDefinitions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// 导出到一个**全新目录**（模拟"清库 → 只靠导出物重建"）。
	exportDir := t.TempDir()
	stats, err := ExportCapabilityDir(ctx, src, exportDir, false, nil)
	if err != nil {
		t.Fatalf("ExportCapabilityDir: %v", err)
	}
	if stats.Written != 3 || stats.Failed != 0 {
		t.Fatalf("导出计数不符: %+v", stats)
	}
	// tools 不导出（不迁），坏定义也不会长出来。
	if _, err := os.Stat(filepath.Join(exportDir, "tools")); !os.IsNotExist(err) {
		t.Fatalf("导出不该创建 tools/ 目录: %v", err)
	}

	// 用导出物重建一个空库：逐字段（逐字节）一致。
	dst := testutil.OpenStore(t, testutil.TempDBPath(t))
	if _, err := ImportCapabilityDir(ctx, dst, exportDir, DefinitionImportOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	afterSkills, err := dst.ListSkillDefinitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterMcps, err := dst.ListMcpServerDefinitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeSkills) != len(afterSkills) || len(beforeMcps) != len(afterMcps) {
		t.Fatalf("往返后条数不符: skill %d→%d mcp %d→%d",
			len(beforeSkills), len(afterSkills), len(beforeMcps), len(afterMcps))
	}
	for i := range beforeSkills {
		if beforeSkills[i].ID != afterSkills[i].ID || beforeSkills[i].Body != afterSkills[i].Body {
			t.Fatalf("skill 往返后不一致:\n before=%q\n after =%q", beforeSkills[i].Body, afterSkills[i].Body)
		}
	}
	for i := range beforeMcps {
		if beforeMcps[i].ID != afterMcps[i].ID || beforeMcps[i].Body != afterMcps[i].Body {
			t.Fatalf("mcp 往返后不一致:\n before=%q\n after =%q", beforeMcps[i].Body, afterMcps[i].Body)
		}
	}
	// 幂等：再导出一次逐字节相同（导出物是确定性的，便于进 git/diff）。
	secondDir := t.TempDir()
	if _, err := ExportCapabilityDir(ctx, src, secondDir, false, nil); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"skills/alpha.json", "skills/beta.json", "mcp/files.json"} {
		a, err := os.ReadFile(filepath.Join(exportDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(secondDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatalf("%s 两次导出不一致:\n%s\n---\n%s", rel, a, b)
		}
	}
}

func TestExportCapabilityDirDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	if _, err := st.UpsertSkillDefinition(ctx, "dry", `{"id":"dry","name":"Dry"}`, 0, "rest"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stats, err := ExportCapabilityDir(ctx, st, dir, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written != 1 || !stats.DryRun {
		t.Fatalf("干跑应报 1 条待写出: %+v", stats)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "dry.json")); !os.IsNotExist(err) {
		t.Fatalf("干跑不该写盘: %v", err)
	}
}

func TestImportExportRequireStore(t *testing.T) {
	if _, err := ImportCapabilityDir(context.Background(), nil, "/tmp/x", DefinitionImportOptions{}, nil); err == nil {
		t.Fatal("无持久层应显式失败")
	}
	if _, err := ExportCapabilityDir(context.Background(), nil, "/tmp/x", false, nil); err == nil {
		t.Fatal("无持久层应显式失败")
	}
	// 空目录 = 没有存量要搬，不是错误（sqlite 模式的写面本来就不需要目录）。
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	stats, err := ImportCapabilityDir(context.Background(), st, "", DefinitionImportOptions{}, nil)
	if err != nil || stats.Dir != "" {
		t.Fatalf("空目录应静默无事: %+v err=%v", stats, err)
	}
}

// writeSkill 写一个定义文件（raw 为准，便于验证未知键保留）。
//
// 从 `sessionruntime/skills_test.go` 复制：本文件搬到 transfer 后不再与那个包共享夹具。
func writeSkill(t *testing.T, dir, id, raw string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "skills", id+".json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}
