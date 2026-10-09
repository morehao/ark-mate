package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/store"
)

func writeAgentDef(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAgentImportExportCLI `mate agent import|export` 走真实装配路径（配置解析 → 打开库 → 调 manager）：
// 存量文件进库、面板改过的那一版是导出物、目录级配置不被当成定义。
func TestAgentImportExportCLI(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "mate.db")
	agentDir := filepath.Join(home, "agents")
	t.Setenv("MATE_HOME", home)
	t.Setenv("MATE_CONFIG_DIR", filepath.Join(home, "config"))
	t.Setenv("MATE_DB", dbPath)
	t.Setenv("MATE_AGENTS", agentDir)

	writeAgentDef(t, agentDir, "u1.json", `{"id":"u1","name":"U1","runtimes":["stub"],"provider":"openai:gpt-4o"}`)
	writeAgentDef(t, agentDir, "providers.json", `{"p1":{"type":"openai","model":"gpt-4o"}}`)
	writeAgentDef(t, agentDir, "default.json", `{"default_agent":"u1"}`)

	agentImport(nil) // 统计打到 stdout（与 capability 的 CLI 同一风格）

	ctx := context.Background()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	defer st.Close()
	row, found, err := st.GetAgentDefinition(ctx, "u1")
	if err != nil || !found {
		t.Fatalf("导入后库里应有 u1: found=%v err=%v", found, err)
	}
	if !strings.Contains(row.Body, "openai:gpt-4o") {
		t.Fatalf("runtimes/provider 应一起导入: %s", row.Body)
	}
	// 目录级配置不进库；default.json 的显式选择进 meta。
	if _, found, _ := st.GetAgentDefinition(ctx, "providers"); found {
		t.Fatal("providers.json 不是 agent 定义")
	}
	if id, ok, _ := st.GetDefaultAgentID(ctx); !ok || id != "u1" {
		t.Fatalf("default.json 的显式选择应进 meta: id=%q ok=%v", id, ok)
	}

	// 面板改一版（写面路径），导出物必须来自 **DB** 而不是目录里那份旧文件。
	if ok, err := st.UpsertAgentDefinition(ctx, "u1",
		`{"id":"u1","name":"面板改的","runtimes":["stub"]}`, 1, "rest"); err != nil || !ok {
		t.Fatal(err)
	}
	agentExport(nil)

	got, err := os.ReadFile(filepath.Join(agentDir, "u1.json"))
	if err != nil {
		t.Fatalf("导出后应有文件: %v", err)
	}
	if !strings.Contains(string(got), "面板改的") {
		t.Fatalf("导出物应是 DB 那一版: %s", got)
	}
	// providers.json 是文件侧的住户：导入导出都不得动它。
	pb, err := os.ReadFile(filepath.Join(agentDir, "providers.json"))
	if err != nil || !strings.Contains(string(pb), "gpt-4o") {
		t.Fatalf("providers.json 不该被动: %s (%v)", pb, err)
	}
}
