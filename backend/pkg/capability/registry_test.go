package capability

import (
	"os"
	"path/filepath"
	"strings"
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
		`{"id":"a-skill","name":"A","body":"# A","runtimes":["opencode"],"scope":"workspace"}`)
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
	// M1 起条目侧只声明「给谁用」（runtimes）与作用域；「放哪、什么格式」在 runtime 投影声明里。
	if len(skills[0].Runtimes) != 1 || skills[0].Runtimes[0] != "opencode" || skills[0].EffectiveScope() != SkillScopeWorkspace {
		t.Fatalf("runtimes/scope 解析不符: %+v scope=%q", skills[0].Runtimes, skills[0].EffectiveScope())
	}
	if skills[1].EffectiveScope() != SkillScopeGlobal {
		t.Fatalf("未声明 scope 应为 global，got %q", skills[1].EffectiveScope())
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

// TestRegisterSkillIDLexiconFollowsBase 钉住 skill id 的词法**上限来自底座**。
//
// dsh 的 `dsh-skill` 用 `SKILL_NAME = /^[a-z0-9]+(?:-[a-z0-9]+)*$/` 校验 skill 文件 frontmatter 的
// `name`，不合规只打一条 warn 就**静默丢弃整条**；而平台物化时把 id 原样写成 name，所以平台闸门
// 一旦比它宽，就会出现「平台自报 in_sync、底座永远不加载」。两个方向都要钉：宽了要拒，**收得比
// 底座更紧也不行**（那会把底座明明能加载的 id 挡在门外）。
func TestRegisterSkillIDLexiconFollowsBase(t *testing.T) {
	r := NewRegistry()
	// 合法：单字符、连字符分段、纯数字段、恰好 64 位。
	for _, id := range []string{"a", "my-skill", "skill-1", "a1-b2-c3", "2026-10-09", strings.Repeat("a", 64), strings.Repeat("a-", 31) + "a"} {
		if err := r.RegisterSkill(&Skill{ID: id, Body: "# x"}); err != nil {
			t.Fatalf("id %q 应通过（底座词法允许）：%v", id, err)
		}
	}
	// 非法：大写 / 下划线 / 点 / 空格 / 首尾连字符 / 连续连字符 / 非 ASCII / 路径分隔符 / 超 64 位。
	for _, id := range []string{
		"", "Probe_Bad.Id", "UPPER", "under_score", "dot.name", "has space",
		"-lead", "trail-", "double--hyphen", "中文", "a/b", `a\b`, strings.Repeat("a", 65),
	} {
		if err := r.RegisterSkill(&Skill{ID: id, Body: "# x"}); err == nil {
			t.Fatalf("id %q 应被拒（不符底座词法 ^[a-z0-9]+(?:-[a-z0-9]+)*$ 且 ≤64 位）", id)
		}
	}
	// MCP 的规则**不跟着收紧**：它不喂给底座的 skill 校验，历史上更宽（大写/下划线仍合法）。
	if err := r.RegisterMcpServer(&McpServer{ID: "My_Mcp", Transport: "stdio", Command: "/usr/local/bin/npx"}); err != nil {
		t.Fatalf("MCP id 不该被 skill 词法波及：%v", err)
	}
}

// TestRegisterSkillRuntimesAndScope 条目侧只声明「给谁用」（runtimes）与作用域（scope）；
// 两者都在**入表前**校验：写错的取值会让投放判据失准，不能静默放过。
func TestRegisterSkillRuntimesAndScope(t *testing.T) {
	r := NewRegistry()
	// 合法：去空白 + 生效作用域默认 global。
	if err := r.RegisterSkill(&Skill{ID: "ok", Runtimes: []string{" opencode ", "deepseekharness"}}); err != nil {
		t.Fatalf("合法 runtimes 应通过: %v", err)
	}
	sk, _ := r.SkillByID("ok")
	if len(sk.Runtimes) != 2 || sk.Runtimes[0] != "opencode" || sk.EffectiveScope() != SkillScopeGlobal {
		t.Fatalf("runtimes 未规整或 scope 默认不符: %+v scope=%q", sk.Runtimes, sk.EffectiveScope())
	}
	// 空 = 全部（升级安全网：旧定义没有该字段，若空表示"谁都不用"，升级会集体撤回所有技能）。
	all := Skill{ID: "all"}
	if !all.AppliesToRuntime("anything") {
		t.Fatal("runtimes 空应表示全部 runtime")
	}
	// 声明了白名单就不再是全部。
	sk2 := Skill{Runtimes: []string{"opencode"}}
	if !sk2.AppliesToRuntime("opencode") || sk2.AppliesToRuntime("dsh") {
		t.Fatal("白名单语义不符")
	}

	for _, bad := range []struct {
		name string
		sk   Skill
	}{
		{"scope 非法", Skill{ID: "x1", Scope: "project"}},
		{"runtimes 含空项", Skill{ID: "x2", Runtimes: []string{"opencode", " "}}},
		{"runtimes 重复", Skill{ID: "x3", Runtimes: []string{"opencode", "opencode"}}},
	} {
		if err := r.RegisterSkill(&bad.sk); err == nil {
			t.Fatalf("%s 应被拒", bad.name)
		}
	}
	if err := r.RegisterSkill(&Skill{ID: "ws", Scope: SkillScopeWorkspace}); err != nil {
		t.Fatalf("workspace scope 应合法（M1 只落语义不消费）: %v", err)
	}
}

// TestLoadLegacySourcesWarns 旧 `sources` 键被忽略这件事**必须可见**。
//
// `encoding/json` 丢未知键是静默的，而这里丢掉的后果是投放面被放宽（「只给 opencode」变成
// 「给所有 runtime」）。装载期为这类文件挂一条 info 告警，把人引到 `mate capability migrate`。
func TestLoadLegacySourcesWarns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "skills", "legacy.json"),
		`{"id":"legacy","name":"L","body":"# l","sources":[{"runtime":"opencode","path":"x","format":"SKILL.md"}]}`)
	writeFile(t, filepath.Join(dir, "skills", "fresh.json"),
		`{"id":"fresh","name":"F","body":"# f","runtimes":["opencode"]}`)

	r := NewRegistry()
	if res := r.LoadDir(dir); res.Skills != 2 {
		t.Fatalf("应装载 2 条: %+v", res)
	}
	legacy, _ := r.SkillByID("legacy")
	if len(legacy.Runtimes) != 0 {
		t.Fatalf("旧 sources 不该被解析成 runtimes: %+v", legacy.Runtimes)
	}
	var found *Warning
	for i := range legacy.Warnings {
		if legacy.Warnings[i].Pattern == "legacy_sources_key" {
			found = &legacy.Warnings[i]
		}
	}
	if found == nil {
		t.Fatalf("含 sources 的定义应挂 legacy_sources_key 告警: %+v", legacy.Warnings)
	}
	if !strings.Contains(found.Message, "mate capability migrate") {
		t.Fatalf("告警要给出出路: %q", found.Message)
	}
	// 没有旧键的定义不该被挂告警（避免噪声把真告警淹掉）。
	fresh, _ := r.SkillByID("fresh")
	for _, w := range fresh.Warnings {
		if w.Pattern == "legacy_sources_key" {
			t.Fatalf("新式定义不该有 legacy 告警: %+v", fresh.Warnings)
		}
	}
}

// TestRegistryReset Reset 是"整体替换"式装载的前提：三张表都必须真的清空。
//
// 缺了它，装载源换成 store 之后"库里删了一行"不会让能力从内存消失（幽灵定义，见 Reset 注释）。
// 断言三张表而不是只断言 skill：只清一部分的写法（漏掉 tools）会留下"删了 server 但工具还在"
// 的残影，而那种残影在列表上看不出来。
func TestRegistryReset(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterSkill(&Skill{ID: "s1", Name: "S"}); err != nil {
		t.Fatalf("RegisterSkill: %v", err)
	}
	if err := r.RegisterMcpServer(&McpServer{ID: "m1", Transport: TransportSSE, URL: "http://localhost:1"}); err != nil {
		t.Fatalf("RegisterMcpServer: %v", err)
	}
	if err := r.RegisterTool(&Tool{Name: "t1", Source: ToolSourceBuiltin}); err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}
	r.Reset()
	if got := r.ListSkills(); len(got) != 0 {
		t.Fatalf("Reset 后 skills 应为空: %+v", got)
	}
	if got := r.ListMcpServers(); len(got) != 0 {
		t.Fatalf("Reset 后 mcps 应为空: %+v", got)
	}
	if got := r.ListTools(); len(got) != 0 {
		t.Fatalf("Reset 后 tools 应为空: %+v", got)
	}
	// 清空之后必须还能正常注册（Reset 不能把 map 置成 nil：那样写回会 panic，
	// 而 panic 会出现在启动路径上——比"没清干净"严重得多）。
	if err := r.RegisterSkill(&Skill{ID: "s2", Name: "S2"}); err != nil {
		t.Fatalf("Reset 后注册: %v", err)
	}
	if got := r.ListSkills(); len(got) != 1 || got[0].ID != "s2" {
		t.Fatalf("Reset 后注册不符: %+v", got)
	}
}
