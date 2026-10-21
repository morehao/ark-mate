package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
)

// TestAutomationImportExportCLI `mate automation import|export` 走真实装配路径（配置解析 → 打开库 →
// 调 manager）：存量文件进库、**一个文件里的定义与运行态各自落在正确的地方**、面板改过的那一版是导出物。
func TestAutomationImportExportCLI(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "mate.db")
	autoDir := filepath.Join(home, "automations")
	t.Setenv("MATE_HOME", home)
	t.Setenv("MATE_CONFIG_DIR", filepath.Join(home, "config"))
	t.Setenv("MATE_DB", dbPath)
	t.Setenv("MATE_AUTOMATIONS", autoDir)

	cwd := t.TempDir()
	body := `{"id":"nightly","name":"夜跑","enabled":true,` +
		`"schedule":{"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0},` +
		`"target":{"cwd":"` + cwd + `","prompt":"跑一遍巡检"},` +
		`"state":"inactive","last_fired_at":"2026-10-08T01:00:00Z","last_status":"completed"}`
	writeAutomationDef(t, autoDir, "nightly.json", body)
	// 坏定义逐条报出、其余照常导入（退出码不整体失败）。
	writeAutomationDef(t, autoDir, "broken.json", `{"id":"broken",`)

	automationImport(nil)

	ctx := context.Background()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	defer st.Close()
	row, found, err := st.GetAutomationDefinition(ctx, "nightly")
	if err != nil || !found {
		t.Fatalf("导入后库里应有 nightly: found=%v err=%v", found, err)
	}
	// 运行态键进**列**，且不在正文里；`state`（定义键）留在正文。
	if row.LastFiredAt != "2026-10-08T01:00:00Z" || row.LastStatus != "completed" {
		t.Fatalf("运行态键应进列: %+v", row)
	}
	for _, k := range []string{"last_fired_at", "last_status"} {
		if strings.Contains(row.Body, `"`+k+`"`) {
			t.Fatalf("正文不该有运行态键 %s: %s", k, row.Body)
		}
	}
	if !strings.Contains(row.Body, `"state": "inactive"`) {
		t.Fatalf("state 应留在正文: %s", row.Body)
	}
	if _, found, _ := st.GetAutomationDefinition(ctx, "broken"); found {
		t.Fatal("坏定义不该进库")
	}

	// 面板改一版（写面路径），导出物必须来自 **DB** 而不是目录里那份旧文件。
	if ok, err := st.UpsertAutomationDefinition(ctx, "nightly",
		`{"id":"nightly","name":"面板改的","enabled":true,`+
			`"schedule":{"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0},`+
			`"target":{"cwd":"`+cwd+`","prompt":"面板改的 prompt"}}`,
		row.Revision, "rest"); err != nil || !ok {
		t.Fatal(err)
	}
	automationExport(nil)

	got, err := os.ReadFile(filepath.Join(autoDir, "nightly.json"))
	if err != nil {
		t.Fatalf("导出后应有文件: %v", err)
	}
	if !strings.Contains(string(got), "面板改的") {
		t.Fatalf("导出物应是 DB 那一版: %s", got)
	}
	// 导出物把运行态键合成同一个文件（老文件载体的形态），所以"导出 → 再导入"是完整往返。
	for _, want := range []string{`"last_fired_at": "2026-10-08T01:00:00Z"`, `"last_status": "completed"`} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("导出物缺少运行态键 %s:\n%s", want, got)
		}
	}
}

// writeAutomationDef 往 automation 目录写一个定义文件（v19 之前这里的夹具还兼写 agent 定义，
// 与能力域共用同一个 helper；agent 面撤销后 helper 只服务 automation，故收口改名）。
func writeAutomationDef(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
