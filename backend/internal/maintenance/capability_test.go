package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/store"
)

func writeDef(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "skills", name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCapabilityMigrateDryRunMatchesApply 「--dry-run 输出 == 实跑结果」是本命令的验收项之一。
// 这里不是比对两段文本，而是比对**字节**：干跑打印出来的 JSON 必须与实跑写进文件的内容逐字节相同。
func TestCapabilityMigrateDryRunMatchesApply(t *testing.T) {
	rootDry, rootReal := t.TempDir(), t.TempDir()
	body := `{"id":"demo","name":"Demo","body":"# d","sources":[{"runtime":"rtx","path":"/x/oc","format":"SKILL.md"},{"runtime":"dsh","path":"/x/dsh","format":"SKILL.md+frontmatter"}],"x-user-note":{"keep":true}}`
	writeDef(t, rootDry, "demo.json", body)
	writeDef(t, rootReal, "demo.json", body)

	var dryOut bytes.Buffer
	if st := migrateSkillsDir(rootDry, true, &dryOut); st.Migrated != 1 || st.Failed != 0 {
		t.Fatalf("干跑统计不符: %+v", st)
	}
	// 干跑不改任何文件。
	if got, _ := os.ReadFile(filepath.Join(rootDry, "skills", "demo.json")); string(got) != body {
		t.Fatalf("干跑不该改文件:\n%s", got)
	}
	var realOut bytes.Buffer
	if st := migrateSkillsDir(rootReal, false, &realOut); st.Migrated != 1 || st.Failed != 0 {
		t.Fatalf("实跑统计不符: %+v", st)
	}

	// 从干跑输出里取出那段缩进了四格的 JSON（去掉前缀就是将要写入的字节）。
	printed := extractIndentedBlock(t, dryOut.String())
	onDisk, err := os.ReadFile(filepath.Join(rootReal, "skills", "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if printed != string(onDisk) {
		t.Fatalf("干跑预览与实跑结果不一致:\n预览:\n%s\n实盘:\n%s", printed, onDisk)
	}
	// 丢弃的 path/format 必须逐条打印出来（不然用户不知道丢了什么）。
	for _, want := range []string{"path=\"/x/oc\"", "format=\"SKILL.md+frontmatter\"", "path=\"/x/dsh\""} {
		if !strings.Contains(dryOut.String(), want) {
			t.Fatalf("输出应逐条打印被丢弃的字段，缺 %s:\n%s", want, dryOut.String())
		}
	}
}

// extractIndentedBlock 从输出里取最后一段「四格缩进」的行，去掉缩进后原样返回。
func extractIndentedBlock(t *testing.T, out string) string {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "    ") {
			lines = append(lines, strings.TrimPrefix(l, "    "))
		}
	}
	if len(lines) == 0 {
		t.Fatalf("输出里没有缩进块:\n%s", out)
	}
	return strings.Join(lines, "\n") + "\n"
}

// TestCapabilityMigrateIdempotent 幂等：连跑两次结果一致，第二次零改动（连 mtime 都不动）。
func TestCapabilityMigrateIdempotent(t *testing.T) {
	root := t.TempDir()
	writeDef(t, root, "demo.json", `{"id":"demo","sources":[{"runtime":"rtx","path":"p","format":"SKILL.md"}]}`)

	var out1 bytes.Buffer
	if st := migrateSkillsDir(root, false, &out1); st.Migrated != 1 || st.Skipped != 0 {
		t.Fatalf("首轮统计不符: %+v", st)
	}
	first, err := os.ReadFile(filepath.Join(root, "skills", "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	fi1, err := os.Stat(filepath.Join(root, "skills", "demo.json"))
	if err != nil {
		t.Fatal(err)
	}

	var out2 bytes.Buffer
	if st := migrateSkillsDir(root, false, &out2); st.Migrated != 0 || st.Skipped != 1 {
		t.Fatalf("次轮应跳过（无 sources）: %+v", st)
	}
	second, _ := os.ReadFile(filepath.Join(root, "skills", "demo.json"))
	if string(first) != string(second) {
		t.Fatalf("幂等重跑内容不一致:\n%s\n%s", first, second)
	}
	fi2, _ := os.Stat(filepath.Join(root, "skills", "demo.json"))
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("无 sources 的文件连 mtime 都不该动")
	}
	// 幂等也包括第三次干跑：已迁移 → 无待迁移项。
	var out3 bytes.Buffer
	if st := migrateSkillsDir(root, true, &out3); st.Migrated != 0 || st.Skipped != 1 {
		t.Fatalf("已迁移后干跑应跳过: %+v", st)
	}
}

// TestCapabilityMigratePreservesUnknownKeysAndDedupes 未知键逐字保留 + runtime 去重升序。
func TestCapabilityMigratePreservesUnknownKeysAndDedupes(t *testing.T) {
	root := t.TempDir()
	writeDef(t, root, "demo.json", `{"id":"demo","sources":[
		{"runtime":"rtx","path":"a","format":"SKILL.md"},
		{"runtime":"rtx","path":"b","format":"SKILL.md"},
		{"runtime":"dsh","path":"c","format":"SKILL.md"},
		{"runtime":"  ","path":"d","format":"SKILL.md"}],
		"x-custom":{"nested":[1,{"deep":null}]},"enabled":false}`)
	writeDef(t, root, "other.json", `{"id":"other","body":"# 无 sources（已迁移）"}`)
	writeDef(t, root, "notjson.json", `broken{`)
	// 非 json 文件也算失败（不静默跳过）。
	writeDef(t, root, "notes.txt", `不是定义文件，应被忽略`)

	var out bytes.Buffer
	st := migrateSkillsDir(root, false, &out)
	if st.Migrated != 1 || st.Skipped != 1 || st.Failed != 1 {
		t.Fatalf("统计不符: %+v\n%s", st, out.String())
	}

	raw, err := os.ReadFile(filepath.Join(root, "skills", "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("迁移结果不是合法 JSON: %v\n%s", err, raw)
	}
	if _, stillThere := doc["sources"]; stillThere {
		t.Fatalf("sources 应已摘除:\n%s", raw)
	}
	// runtimes = 去重升序、空项丢弃。
	got, _ := doc["runtimes"].([]any)
	if len(got) != 2 || got[0] != "dsh" || got[1] != "rtx" {
		t.Fatalf("runtimes 应为去重升序 [dsh rtx]: %v", got)
	}
	// 未知键与其它已知键原样保留（含嵌套结构与 false）。
	custom, ok := doc["x-custom"].(map[string]any)
	if !ok {
		t.Fatalf("未知键 x-custom 丢了: %v", doc)
	}
	nested, _ := custom["nested"].([]any)
	if len(nested) != 2 {
		t.Fatalf("未知键的嵌套结构被改动: %v", custom)
	}
	if v, ok := doc["enabled"].(bool); !ok || v {
		t.Fatalf("已知键 enabled 应原样保留 false: %v", doc["enabled"])
	}
	if doc["id"] != "demo" {
		t.Fatalf("id 不该变: %v", doc["id"])
	}
	// .txt 不是定义文件，未被触碰。
	if b, _ := os.ReadFile(filepath.Join(root, "skills", "notes.txt")); string(b) != "不是定义文件，应被忽略" {
		t.Fatalf("非 json 文件不该被改: %s", b)
	}
}

// TestCapabilityMigrateEmptyRuntimes 全是空 runtime 的 sources：不写 `runtimes`（空数组会让人
// 误读成"谁都不给"，而"没有该键"与"空数组"在本平台都表示**全部**，不如不写）。
func TestCapabilityMigrateEmptyRuntimes(t *testing.T) {
	root := t.TempDir()
	writeDef(t, root, "demo.json", `{"id":"demo","sources":[{"path":"a","format":"SKILL.md"}]}`)
	var out bytes.Buffer
	if st := migrateSkillsDir(root, false, &out); st.Migrated != 1 {
		t.Fatalf("统计不符: %+v", st)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "skills", "demo.json"))
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["runtimes"]; ok {
		t.Fatalf("无 runtime 时不该写 runtimes 键:\n%s", raw)
	}
	if !strings.Contains(out.String(), "全部 runtime") {
		t.Fatalf("应说明迁移后的语义:\n%s", out.String())
	}
}

// TestCapabilityMigrateMissingDir 目录不存在 → 明确提示且不算失败（首次部署就会遇到）。
func TestCapabilityMigrateMissingDir(t *testing.T) {
	var out bytes.Buffer
	st := migrateSkillsDir(filepath.Join(t.TempDir(), "nope"), false, &out)
	if st.Failed != 0 || st.Migrated != 0 {
		t.Fatalf("目录不存在不该算失败: %+v", st)
	}
	if !strings.Contains(out.String(), "不存在") {
		t.Fatalf("应给出明确提示:\n%s", out.String())
	}
}

// TestCapabilityImportExportCLI 端到端接线：CLI 层只做"解析配置 → 调领域函数 → 打统计"，
// 但**取错库路径/取错能力目录**这类接线错误只有真跑一次才看得见（而且它在生产里表现为
// "导入成功了，可重启后能力面还是空的"——最难查的一类）。
//
// 全程私有 HOME（MATE_HOME/MATE_CONFIG_DIR/MATE_DB/MATE_CAPABILITIES 都指向临时目录），
// 不碰用户真实的 ~/.ark-mate（本机隔离纪律）。
func TestCapabilityImportExportCLI(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "mate.db")
	capDir := filepath.Join(home, "capabilities")
	t.Setenv("MATE_HOME", home)
	t.Setenv("MATE_CONFIG_DIR", filepath.Join(home, "config"))
	t.Setenv("MATE_DB", dbPath)
	t.Setenv("MATE_CAPABILITIES", capDir)

	writeDef(t, capDir, "demo.json", `{"id":"demo","name":"Demo","x-keep":{"nested":true}}`)
	if err := os.MkdirAll(filepath.Join(capDir, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capDir, "tools", "web.json"), []byte(`{"name":"web","source":"builtin"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	capabilityImport(nil) // 会把统计打到 stdout（本仓 CLI 的既有风格）

	ctx := context.Background()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	defer st.Close()
	row, found, err := st.GetSkillDefinition(ctx, "demo")
	if err != nil || !found {
		t.Fatalf("导入后库里应有 demo: found=%v err=%v", found, err)
	}
	if !strings.Contains(row.Body, `"x-keep"`) {
		t.Fatalf("未知键应一起导入: %s", row.Body)
	}

	// 面板改一版（写面路径），导出物必须来自 **DB** 而不是目录里那份旧文件。
	if _, err := st.UpsertSkillDefinition(ctx, "demo", `{"id":"demo","name":"面板改的","x-keep":{"nested":true}}`, 1, "rest"); err != nil {
		t.Fatal(err)
	}
	capabilityExport(nil)

	got, err := os.ReadFile(filepath.Join(capDir, "skills", "demo.json"))
	if err != nil {
		t.Fatalf("导出后应有文件: %v", err)
	}
	if !strings.Contains(string(got), "面板改的") || !strings.Contains(string(got), `"x-keep"`) {
		t.Fatalf("导出物应是 DB 那一版（含未知键）: %s", got)
	}
	// tools 不导出（不迁）。
	if _, err := os.Stat(filepath.Join(capDir, "tools", "web.json")); err != nil {
		t.Fatalf("导出不该动 tools/：%v", err)
	}
}
