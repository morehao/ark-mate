package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/dto/dtolibrary"
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/service/svclibrary"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// libraryRig 一套四类齐全的 Library 夹具：
// 两个 runtime（一个已解析 skill 投影、一个声明了但未解析）+ skill/mcp/tool/agent 各一条。
func libraryRig(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	reg := runtime.NewRegistry(
		runtime.Spec{ID: "opencode", Name: "opencode", McpTransports: []string{"stdio"},
			Projections: []runtime.Projection{opencodeSkillProjectionForTest()}},
		// deepseekharness 没有投影解析（未配 FORGE_PROJECTION_DIRS）→ not_configured 分支。
		runtime.Spec{ID: "deepseekharness", Name: "dsh", McpTransports: []string{"stdio"}},
	)
	svc := NewService(reg, eventbus.New())
	svc.LoadCapabilityDefinitions(dir)
	svc.SetSkillProjections([]SkillProjection{{
		Runtime: "opencode",
		Projection: runtime.Projection{Kind: runtime.ProjectionSkill, Scope: runtime.ScopeGlobal,
			Root: t.TempDir(), Layout: runtime.LayoutIDToken + "/SKILL.md",
			Format: capability.FormatSkillMD, Tested: false},
	}})
	svc.agents["a-local"] = &AgentRec{ID: "a-local", Name: "本地 agent", Runtimes: []string{"opencode", "nope"}}
	svc.agents["dsh"] = &AgentRec{ID: "dsh", Name: "播种", Runtimes: []string{"opencode"}}
	return svc
}

func libCtx() svclibrary.Query { return svclibrary.Query{} }

// TestLibraryKindsAndOrigin 四类齐全 + origin 判据只看"有没有事实源文件"。
func TestLibraryKindsAndOrigin(t *testing.T) {
	svc := libraryRig(t)
	ctx := context.Background()

	if _, err := svc.CreateSkill(ctx, writeReq(t, `{"id":"demo","name":"演示 skill","body":"# d"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateMcpServer(ctx, writeMcpReq(t,
		`{"id":"fs","name":"文件","transport":"stdio","command":"/usr/local/bin/npx"}`)); err != nil {
		t.Fatal(err)
	}
	if err := svc.RegisterTool(capability.Tool{Name: "builtin_echo", Source: capability.ToolSourceBuiltin}); err != nil {
		t.Fatal(err)
	}
	// 手工放一份 agent 定义文件（并装载）→ 该 agent 变成 local（判据是文件，不是 id）。
	agentDir := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	def := `{"id":"a-local","name":"本地 agent","runtimes":["opencode"]}`
	if err := os.WriteFile(filepath.Join(agentDir, "a-local.json"), []byte(def), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.LoadAgentDefinitions(agentDir)

	items, err := svc.ListLibrary(libCtx())
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string][]dtolibrary.LibraryItem{}
	for _, it := range items {
		byKind[it.Kind] = append(byKind[it.Kind], it)
	}
	for _, k := range []string{dtolibrary.KindSkill, dtolibrary.KindMcp, dtolibrary.KindAgent, dtolibrary.KindTool} {
		if len(byKind[k]) == 0 {
			t.Fatalf("Library 缺 %s 类（items=%+v）", k, items)
		}
	}
	if got := byKind[dtolibrary.KindSkill][0]; got.Origin != dtolibrary.OriginLocal || got.SourceFile == "" {
		t.Fatalf("文件里的 skill 应是 local 且带 source_file：%+v", got)
	}
	if got := byKind[dtolibrary.KindMcp][0]; got.Origin != dtolibrary.OriginLocal {
		t.Fatalf("文件里的 mcp 应是 local：%+v", got)
	}
	for _, it := range byKind[dtolibrary.KindTool] {
		if it.Origin != dtolibrary.OriginImported {
			t.Fatalf("builtin 工具应是 imported（无事实源文件）：%+v", it)
		}
	}
	// 名称回落：未声明 name 的条目显示 id。
	for _, it := range byKind[dtolibrary.KindTool] {
		if it.Name == "" || it.ID == "" {
			t.Fatalf("条目必须有 id 与回落后的 name：%+v", it)
		}
	}
	// 排序稳定：kind、id 升序。
	for i := 1; i < len(items); i++ {
		a, b := items[i-1], items[i]
		if a.Kind > b.Kind || (a.Kind == b.Kind && a.ID > b.ID) {
			t.Fatalf("排序应按 kind、id 升序：%+v 出现在 %+v 之后", a, b)
		}
	}
}

// TestLibraryKindFilterAndQuery `?kind=` 过滤、`?q=` 大小写不敏感子串、未知 kind → 40000。
func TestLibraryKindFilterAndQuery(t *testing.T) {
	svc := libraryRig(t)
	if _, err := svc.CreateSkill(context.Background(), writeReq(t, `{"id":"PDF-Reader","name":"读 PDF","description":"解析文档"}`)); err != nil {
		t.Fatal(err)
	}
	items, err := svc.ListLibrary(svclibrary.Query{Kind: dtolibrary.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != dtolibrary.KindSkill {
		t.Fatalf("kind 过滤失效: %+v", items)
	}
	for _, q := range []string{"pdf", "PDF", "读 pdf", "解析"} {
		got, err := svc.ListLibrary(svclibrary.Query{Q: q})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			t.Fatalf("子串 %q 应命中（大小写不敏感、id/name/description 都算）", q)
		}
	}
	got, err := svc.ListLibrary(svclibrary.Query{Kind: dtolibrary.KindSkill, Q: "nonexistent"})
	if err != nil || len(got) != 0 {
		t.Fatalf("不命中应返回空表: %+v err=%v", got, err)
	}
	if _, err := svc.ListLibrary(svclibrary.Query{Kind: "command"}); err == nil {
		t.Fatal("未知 kind 应报错（M5 之前 command 不合法）")
	} else if numericCode(err) != code.InvalidParam {
		t.Fatalf("未知 kind 应是 40000，got %d", numericCode(err))
	}
}

// TestLibrarySkillApplicability 逐 runtime 状态：untested（会投放但不担保）/ not_configured /
// not_applicable（白名单没它）/ disabled。**刻意不给合并结论**。
func TestLibrarySkillApplicability(t *testing.T) {
	svc := libraryRig(t)
	ctx := context.Background()
	if _, err := svc.CreateSkill(ctx, writeReq(t, `{"id":"demo","body":"# d"}`)); err != nil {
		t.Fatal(err)
	}
	statusFor := func(id, rt string) dtolibrary.Applicability {
		t.Helper()
		items, err := svc.ListLibrary(svclibrary.Query{Kind: dtolibrary.KindSkill})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.ID != id {
				continue
			}
			for _, a := range it.Applicability {
				if a.Runtime == rt {
					return a
				}
			}
		}
		t.Fatalf("没找到 %s 在 %s 上的可达性（items=%+v）", id, rt, items)
		return dtolibrary.Applicability{}
	}

	// opencode 已解析投影但 Tested=false → untested，且 OK=true（照常投放）。
	if a := statusFor("demo", "opencode"); a.Status != dtolibrary.StatusUntested || !a.OK {
		t.Fatalf("未实测的投影应是 untested 且 OK=true：%+v", a)
	}
	// deepseekharness 声明了投影但本进程没解析出来 → not_configured（它压根不在投影列表里，
	// 所以这里断言的是"没有它的条目"而不是硬编一个状态——见下一条说明）。
	// 落点说明：投影列表来自 adapter 声明 + 配置解析，未解析的 runtime 不会出现在列表里；
	// not_configured 由**声明了但解析失败**的投影产生（p.Err != ""）。
	svc.SetSkillProjections([]SkillProjection{
		{Runtime: "opencode", Projection: runtime.Projection{Kind: runtime.ProjectionSkill,
			Root: t.TempDir(), Layout: runtime.LayoutIDToken + "/SKILL.md",
			Format: capability.FormatSkillMD, Tested: true}},
		{Runtime: "deepseekharness", Err: "未配置投影根"},
	})
	if a := statusFor("demo", "deepseekharness"); a.Status != dtolibrary.StatusNotConfigured || a.OK {
		t.Fatalf("解析失败的投影应是 not_configured 且 OK=false：%+v", a)
	}
	if a := statusFor("demo", "opencode"); a.Status != dtolibrary.StatusProjected {
		t.Fatalf("Tested=true 后应是 projected：%+v", a)
	}

	// 白名单不含某 runtime → not_applicable（并说清怎么改）。
	if _, err := svc.UpdateSkill(ctx, "demo", writeReq(t, `{"runtimes":["opencode"]}`)); err != nil {
		t.Fatal(err)
	}
	if sk, ok := svc.caps.SkillByID("demo"); !ok || len(sk.Runtimes) != 1 {
		t.Fatalf("skills 应已更新: %+v ok=%v", sk, ok)
	}
	if a := statusFor("demo", "deepseekharness"); a.Status != dtolibrary.StatusNotApplicable {
		t.Fatalf("白名单未含该 runtime 应是 not_applicable：%+v", a)
	}

	// 禁用 → disabled（优先级最高：用户的显式决定最先说）。
	if _, err := svc.UpdateSkill(ctx, "demo", writeReq(t, `{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	if a := statusFor("demo", "opencode"); a.Status != dtolibrary.StatusDisabled {
		t.Fatalf("禁用应是 disabled：%+v", a)
	}
}

// TestLibraryMcpAndToolApplicability MCP 按 transport 承载；tool 继承归属 server 的作用域与可达性；
// 归属 server 消失的工具要给出警告（而不是静默留在列表里）。
func TestLibraryMcpAndToolApplicability(t *testing.T) {
	svc := libraryRig(t)
	ctx := context.Background()
	if _, err := svc.CreateMcpServer(ctx, writeMcpReq(t,
		`{"id":"fs","transport":"stdio","command":"/usr/local/bin/npx"}`)); err != nil {
		t.Fatal(err)
	}
	if err := svc.RegisterTool(capability.Tool{Name: "fs_read", Source: capability.ToolSourceMCP, McpServer: "fs"}); err != nil {
		t.Fatal(err)
	}
	items, err := svc.ListLibrary(libCtx())
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Kind == dtolibrary.KindMcp && it.ID == "fs" {
			if len(it.Applicability) != 2 {
				t.Fatalf("应逐已注册 runtime 报可达性：%+v", it.Applicability)
			}
			for _, a := range it.Applicability {
				if a.Status != dtolibrary.StatusProjected || !a.OK {
					t.Fatalf("两个 runtime 都承载 stdio，应 projected：%+v", a)
				}
			}
		}
		if it.Kind == dtolibrary.KindTool && it.ID == "fs_read" {
			if len(it.Applicability) == 0 || it.Applicability[0].Status != dtolibrary.StatusProjected {
				t.Fatalf("工具应继承 server 的可达性：%+v", it)
			}
		}
	}

	// 归属 server 被删（工具条目被一并摘除）——再造一个"悬空工具"看警告分支。
	if err := svc.RegisterTool(capability.Tool{Name: "ghost", Source: capability.ToolSourceMCP, McpServer: "gone"}); err != nil {
		t.Fatal(err)
	}
	items, err = svc.ListLibrary(svclibrary.Query{Kind: dtolibrary.KindTool, Q: "ghost"})
	if err != nil || len(items) != 1 {
		t.Fatalf("应能找到悬空工具: %+v err=%v", items, err)
	}
	if len(items[0].Warnings) == 0 || !strings.Contains(items[0].Warnings[0], "已不在注册表") {
		t.Fatalf("悬空归属必须给警告（否则用户看见一个调不通的能力）：%+v", items[0])
	}
	if len(items[0].Applicability) != 0 {
		t.Fatalf("悬空归属不该声称任何 runtime 可达：%+v", items[0].Applicability)
	}
}

// TestLibraryAgentApplicability agent 的候选 runtime 就是它的使用边界：未注册的候选 → not_applicable。
func TestLibraryAgentApplicability(t *testing.T) {
	svc := libraryRig(t)
	items, err := svc.ListLibrary(svclibrary.Query{Kind: dtolibrary.KindAgent, Q: "a-local"})
	if err != nil || len(items) != 1 {
		t.Fatalf("应找到 a-local: %+v err=%v", items, err)
	}
	got := map[string]dtolibrary.Applicability{}
	for _, a := range items[0].Applicability {
		got[a.Runtime] = a
	}
	if a := got["opencode"]; a.Status != dtolibrary.StatusProjected || !a.OK {
		t.Fatalf("已注册候选应 projected：%+v", a)
	}
	if a := got["nope"]; a.Status != dtolibrary.StatusNotApplicable || a.OK {
		t.Fatalf("未注册候选应 not_applicable：%+v", a)
	}
	if !items[0].Enabled {
		t.Fatal("agent 没有禁用开关，Enabled 恒 true（State 是运行态，不是可用性）")
	}
}

// TestLibraryScale 规模验收：300 条 < 50ms（计划点名的性能线；这里用宽松上限防 CI 抖动，
// 真实值在 -bench 里看）。
func TestLibraryScale(t *testing.T) {
	svc := libraryRig(t)
	ctx := context.Background()
	for i := 0; i < 300; i++ {
		id := fmt.Sprintf("skill-%03d", i)
		if _, err := svc.CreateSkill(ctx, writeReq(t, fmt.Sprintf(`{"id":%q,"name":"技能 %d","description":"批量"}`, id, i))); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	items, err := svc.ListLibrary(libCtx())
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if len(items) < 300 {
		t.Fatalf("应有 300+ 条，got %d", len(items))
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("300 条聚合耗时 %v，超过 50ms 的验收线", elapsed)
	}
	t.Logf("300+ 条 Library 聚合耗时 %v", elapsed)
}

// BenchmarkLibraryList 基准（`go test -bench=BenchmarkLibraryList`；不做缓存是刻意的，见 library.go 顶部注释）。
func BenchmarkLibraryList(b *testing.B) {
	dir := b.TempDir()
	reg := runtime.NewRegistry(runtime.Spec{ID: "opencode", McpTransports: []string{"stdio"},
		Projections: []runtime.Projection{opencodeSkillProjectionForTest()}})
	svc := NewService(reg, eventbus.New())
	svc.LoadCapabilityDefinitions(dir)
	for i := 0; i < 300; i++ {
		sk := capability.Skill{ID: fmt.Sprintf("skill-%03d", i), Name: "技能", Description: "批量"}
		if err := svc.caps.RegisterSkill(&sk); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.ListLibrary(libCtx()); err != nil {
			b.Fatal(err)
		}
	}
}
