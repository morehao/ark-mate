package sessionruntime

import (
	"context"
	"testing"

	"github.com/morehao/ark-mate/internal/core/capability"
)

// TestLoadCapabilityDefinitionsFromStore M6-2 的验收核心：sqlite 模式下 DB 是能力面的事实源。
//
// 两条断言各自对应一个真实缺陷面：
//  1. **装载出来的就是 store 里的那几条**（含清单对齐：id/字段来自 body，id 以行主键为准）；
//  2. **删掉一行再装载，那条能力必须消失**——这是"幽灵定义"回归测试。`RegisterXxx` 只覆盖同 id、
//     不摘除旧条目，所以装载前不清场的话，库里删掉的能力会永远留在内存里继续被列出、继续被注入，
//     而 REST 与物化都会自报"一切正常"（设计文档 §3.5-⑤）。
func TestLoadCapabilityDefinitionsFromStore(t *testing.T) {
	r := newRig(t, func(c *rigConfig) { c.store = true })
	ctx := context.Background()

	// 两条 skill + 一条 mcp，外加**一条过不了闸门的坏定义**（scope 非法）。
	mustUpsertSkill(t, r, "a-skill", `{"name":"A","description":"aa","runtimes":["rtx"]}`)
	mustUpsertSkill(t, r, "b-skill", `{"name":"B"}`)
	mustUpsertSkill(t, r, "bad-skill", `{"name":"坏","scope":"bogus"}`)
	if ok, err := r.St.UpsertMcpServerDefinition(ctx, "m1", `{"transport":"sse","url":"http://localhost:9999"}`, 0, "test"); err != nil || !ok {
		t.Fatalf("写 mcp 定义: ok=%v err=%v", ok, err)
	}

	// 坏定义不该让整个能力面起不来（容错口径与 LoadDir 逐字一致）。
	if n := planesOf(r.Svc).LoadCapabilityDefinitionsFromStore(ctx, ""); n != 3 {
		t.Fatalf("应装载 3 条（2 skill + 1 mcp，坏定义跳过），got %d", n)
	}
	skills := planesOf(r.Svc).Capabilities.ListSkills()
	if len(skills) != 2 || skills[0].ID != "a-skill" || skills[1].ID != "b-skill" {
		t.Fatalf("skill 装载不符: %+v", skills)
	}
	if skills[0].Name != "A" || len(skills[0].Runtimes) != 1 || skills[0].Runtimes[0] != "rtx" {
		t.Fatalf("skill 字段未从 body 还原: %+v", skills[0])
	}
	mcps := planesOf(r.Svc).Capabilities.ListMcpServers()
	if len(mcps) != 1 || mcps[0].ID != "m1" || mcps[0].Transport != "sse" {
		t.Fatalf("mcp 装载不符: %+v", mcps)
	}
	// id 以**行主键**为准：body 里写别的 id 不该让这条能力挂到另一个名字下
	// （行主键是 CAS 与审计的键，body 里的 id 只是历史残留）。
	mustUpsertSkill(t, r, "c-skill", `{"id":"wrong-id","name":"C"}`)
	if n := planesOf(r.Svc).LoadCapabilityDefinitionsFromStore(ctx, ""); n != 4 {
		t.Fatalf("第二次装载应 4 条，got %d", n)
	}
	for _, sk := range planesOf(r.Svc).Capabilities.ListSkills() {
		if sk.ID == "wrong-id" {
			t.Fatalf("body 里的 id 不该覆盖行主键: %+v", sk)
		}
	}

	// —— 幽灵定义回归：删一行（带 revision CAS）后重新装载，该能力必须消失 ——
	if ok, err := r.St.DeleteSkillDefinition(ctx, "a-skill", 1, "test"); err != nil || !ok {
		t.Fatalf("删除 skill 行: ok=%v err=%v", ok, err)
	}
	if n := planesOf(r.Svc).LoadCapabilityDefinitionsFromStore(ctx, ""); n != 3 {
		t.Fatalf("删除后装载应 3 条，got %d", n)
	}
	for _, sk := range planesOf(r.Svc).Capabilities.ListSkills() {
		if sk.ID == "a-skill" {
			t.Fatalf("库里已删除的 skill 仍在注册表里（幽灵定义）: %+v", planesOf(r.Svc).Capabilities.ListSkills())
		}
	}
	// 变空也必须真的变空（只删一条的用例看不出"清场"，全删才看得出）。
	if ok, err := r.St.DeleteSkillDefinition(ctx, "b-skill", 1, "test"); err != nil || !ok {
		t.Fatalf("删除 b-skill: ok=%v err=%v", ok, err)
	}
	if ok, err := r.St.DeleteSkillDefinition(ctx, "c-skill", 1, "test"); err != nil || !ok {
		t.Fatalf("删除 c-skill: ok=%v err=%v", ok, err)
	}
	if ok, err := r.St.DeleteMcpServerDefinition(ctx, "m1", 1, "test"); err != nil || !ok {
		t.Fatalf("删除 m1: ok=%v err=%v", ok, err)
	}
	if n := planesOf(r.Svc).LoadCapabilityDefinitionsFromStore(ctx, ""); n != 0 {
		t.Fatalf("全删后应 0 条，got %d", n)
	}
	if got := planesOf(r.Svc).Capabilities.ListSkills(); len(got) != 0 {
		t.Fatalf("全删后 skill 面应为空: %+v", got)
	}
	if got := planesOf(r.Svc).Capabilities.ListMcpServers(); len(got) != 0 {
		t.Fatalf("全删后 mcp 面应为空: %+v", got)
	}
}

// TestLoadCapabilityDefinitionsFromStoreRequiresStore 无持久层 → 0 条（不是 panic、也不是"文件兜底"）。
//
// sqlite 模式的装载入口拿到 nil store 只可能来自装配错误；返回 0 让上层照常启动（能力面为空、
// 日志可见），比崩在启动路径上更符合本仓"装载失败只告警不阻断"的既有口径。
func TestLoadCapabilityDefinitionsFromStoreRequiresStore(t *testing.T) {
	r := newRig(t) // 不带库
	if n := planesOf(r.Svc).LoadCapabilityDefinitionsFromStore(context.Background(), ""); n != 0 {
		t.Fatalf("无库时应 0 条，got %d", n)
	}
}

// mustUpsertSkill 往 store 里写一条 skill 定义（新建，expectedRev=0）。
func mustUpsertSkill(t *testing.T, r *rig, id, body string) {
	t.Helper()
	if ok, err := r.St.UpsertSkillDefinition(context.Background(), id, body, 0, "test"); err != nil || !ok {
		t.Fatalf("写 skill 定义 %s: ok=%v err=%v", id, ok, err)
	}
}

// TestSkillScopeDeclarationGuard 兜一下本文件的前提：`scope: "bogus"` 确实过不了闸门。
//
// 上面那条测试用"坏定义被跳过"来证明容错，若哪天闸门放宽到接受任意 scope，那条测试会**静默变成
// 4 条全装载**（仍然绿）。把它钉在这里，改闸门的人会看到这条红。
func TestSkillScopeDeclarationGuard(t *testing.T) {
	caps := capability.NewRegistry()
	if err := caps.RegisterSkill(&capability.Skill{ID: "bad", Scope: "bogus"}); err == nil {
		t.Fatal(`scope "bogus" 应被 RegisterSkill 拒绝（合法取值只有 global|workspace）`)
	}
}
