package capability

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRegistryLoadDirAndList 目录加载三表 + 列表排序（skill/mcp 按 id、tool 按 name）。
func TestRegistryLoadDirAndList(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "skills", "b-skill.json"), `{"id":"b-skill","name":"B","description":"bb"}`)
	writeFile(t, filepath.Join(dir, "skills", "a-skill.json"),
		`{"id":"a-skill","name":"A","body":"# A","sources":[{"runtime":"opencode","path":"x","format":"SKILL.md"}]}`)
	writeFile(t, filepath.Join(dir, "skills", "bad.json"), `not-json{`)
	writeFile(t, filepath.Join(dir, "mcp", "m2.json"), `{"id":"m2","transport":"stdio","command":"/usr/local/bin/npx","args":["-y","pkg"]}`)
	writeFile(t, filepath.Join(dir, "mcp", "m1.json"), `{"id":"m1","name":"Files","transport":"sse","url":"http://localhost:9999"}`)
	writeFile(t, filepath.Join(dir, "tools", "web.json"), `{"name":"web_search","description":"search","source":"builtin","schema":{"type":"object"}}`)
	writeFile(t, filepath.Join(dir, "tools", "ghost.json"), `{"name":"ghost","source":"bogus"}`) // 非法 source → 跳过

	r := NewRegistry()
	res := r.LoadDir(dir)
	if res.Skills != 2 || res.Mcps != 2 || res.Tools != 1 {
		t.Fatalf("加载数不符: %+v", res)
	}

	skills := r.ListSkills()
	if len(skills) != 2 || skills[0].ID != "a-skill" || skills[1].ID != "b-skill" {
		t.Fatalf("skills 排序/内容不符: %+v", skills)
	}
	if len(skills[0].Sources) != 1 || skills[0].Sources[0].Runtime != "opencode" {
		t.Fatalf("sources 解析不符: %+v", skills[0].Sources)
	}

	mcps := r.ListMcpServers()
	if len(mcps) != 2 || mcps[0].ID != "m1" || mcps[1].ID != "m2" {
		t.Fatalf("mcps 排序不符: %+v", mcps)
	}
	if mcps[0].Transport != "sse" || mcps[1].Command != "/usr/local/bin/npx" {
		t.Fatalf("mcp 字段不符: %+v", mcps)
	}

	tools := r.ListTools()
	if len(tools) != 1 || tools[0].Name != "web_search" || tools[0].Source != ToolSourceBuiltin {
		t.Fatalf("tools 不符: %+v", tools)
	}
}

// TestRegistryEmptyListsNotNil 空表列表返回非 nil 空切片（REST 输出 [] 而非 null）。
func TestRegistryEmptyListsNotNil(t *testing.T) {
	r := NewRegistry()
	if s := r.ListSkills(); s == nil || len(s) != 0 {
		t.Fatalf("空 skills 应为空切片: %v", s)
	}
	if m := r.ListMcpServers(); m == nil || len(m) != 0 {
		t.Fatalf("空 mcps 应为空切片: %v", m)
	}
	if ts := r.ListTools(); ts == nil || len(ts) != 0 {
		t.Fatalf("空 tools 应为空切片: %v", ts)
	}
}

// TestRegistryValidation 非法条目（坏 id / 坏 transport / 坏 source）注册报错。
func TestRegistryValidation(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterSkill(&Skill{Name: "no-id"}); err == nil {
		t.Fatal("skill 缺 id 应报错")
	}
	if err := r.RegisterMcpServer(&McpServer{ID: "ok", Transport: "ws"}); err == nil {
		t.Fatal("mcp 坏 transport 应报错")
	}
	if err := r.RegisterTool(&Tool{Name: "x", Source: "nope"}); err == nil {
		t.Fatal("tool 坏 source 应报错")
	}
	if err := r.RegisterMcpServer(&McpServer{ID: "rel", Transport: "stdio", Command: "npx"}); err == nil {
		t.Fatal("stdio 相对 command 应报错（ACP 需绝对路径）")
	}
	if err := r.RegisterMcpServer(&McpServer{ID: "fs", Transport: "stdio", Command: "/usr/local/bin/npx"}); err != nil {
		t.Fatalf("绝对路径 stdio mcp 应通过: %v", err)
	}
}
