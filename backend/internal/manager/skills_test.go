package manager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/internal/dto/dtocapability"
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/ark-forge/pkg/runtime"
	"github.com/morehao/golib/gerror"
)

// skillRig 造一个带能力目录与物化目标的 Service：<dir>/skills/<id>.json 为定义文件，
// <root> 为物化目标（已登记，启停后应立刻重校）。
func skillRig(t *testing.T) (svc *Service, dir, root string) {
	t.Helper()
	var bus *eventbus.Bus
	svc, _, bus = rigWithRuntime(t)
	dir = t.TempDir()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if n := svc.LoadCapabilityDefinitions(dir); n != 0 {
		t.Fatalf("空目录应装载 0 条: %d", n)
	}
	_ = bus
	svc.SetSkillMaterializeTargets([]SkillMaterializeTarget{{Root: root}})
	return svc, dir, root
}

// writeSkill 写一个定义文件（raw 为准，便于验证未知键保留）。
func writeSkill(t *testing.T, dir, id, raw string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "skills", id+".json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSetSkillEnabledWritesBackAndWithdraws 启停三件事：写盘（保留未知键）/ 更新注册表 / 立刻重校物化。
func TestSetSkillEnabledWritesBackAndWithdraws(t *testing.T) {
	svc, dir, root := skillRig(t)
	writeSkill(t, dir, "demo", `{
  "id": "demo",
  "name": "Demo",
  "description": "演示",
  "body": "# demo",
  "sources": [{"runtime": "opencode", "format": "SKILL.md"}],
  "x-user-note": {"keep": ["me", 1]}
}`)
	if n := svc.LoadCapabilityDefinitions(dir); n != 1 {
		t.Fatalf("应装载 1 条: %d", n)
	}
	if w, _, err := svc.ReconcileSkills(root); err != nil || w != 1 {
		t.Fatalf("预置物化失败: w=%d err=%v", w, err)
	}
	materialized := filepath.Join(root, "demo", "SKILL.md")
	if _, err := os.Stat(materialized); err != nil {
		t.Fatalf("应已物化: %v", err)
	}

	// —— 禁用 ——
	resp, err := svc.SetSkillEnabled(context.Background(), "demo", false)
	if err != nil {
		t.Fatalf("禁用失败: %v", err)
	}
	if resp.Enabled || resp.Removed != 1 || resp.Written != 0 {
		t.Fatalf("禁用响应不符: %+v", resp)
	}
	if resp.Path == "" || resp.NextAction == "" {
		t.Fatalf("响应应回带写回路径与说明: %+v", resp)
	}
	// 定义文件：enabled=false + 未知键原样保留 + 其余字段不动。
	raw, err := os.ReadFile(filepath.Join(dir, "skills", "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("写回的不是合法 JSON: %v", err)
	}
	if string(doc["enabled"]) != "false" {
		t.Fatalf("enabled 应为 false: %s", doc["enabled"])
	}
	var note map[string][]any
	if err := json.Unmarshal(doc["x-user-note"], &note); err != nil {
		t.Fatalf("未知键应原样保留: %s (%v)", doc["x-user-note"], err)
	}
	if len(note["keep"]) != 2 {
		t.Fatalf("未知键内容变了: %+v", note)
	}
	if string(doc["body"]) != `"# demo"` {
		t.Fatalf("正文不该被改写: %s", doc["body"])
	}
	// 物化副本已被撤回（agent 下次拉起看不到它），但定义目录仍在。
	if _, err := os.Stat(filepath.Join(root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("禁用后物化目录应被撤回: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "demo.json")); err != nil {
		t.Fatalf("定义文件必须保留（overlay）: %v", err)
	}
	// 注册表里仍在，且标记为禁用（关了还能开回来）。
	list, err := svc.ListSkills()
	if err != nil || len(list) != 1 || list[0].Enabled {
		t.Fatalf("列表应含被禁用条目: %+v err=%v", list, err)
	}

	// —— 重新启用 ——
	resp, err = svc.SetSkillEnabled(context.Background(), "demo", true)
	if err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	if !resp.Enabled || resp.Written != 1 || resp.Removed != 0 {
		t.Fatalf("启用响应不符: %+v", resp)
	}
	if _, err := os.Stat(materialized); err != nil {
		t.Fatalf("启用后应重新物化: %v", err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "skills", "demo.json"))
	if !strings.Contains(string(raw), `"enabled": true`) {
		t.Fatalf("启回应写回 true: %s", raw)
	}
}

// TestSetSkillEnabledErrors 三条显式失败：未知 id / 无定义文件 / 目录未登记。
// 都要求「宁可显式失败」——不能假装生效（假成功会让用户以为改完了）。
func TestSetSkillEnabledErrors(t *testing.T) {
	svc, dir, _ := skillRig(t)

	// 1) 未知 id → 40401（ClassRequest 里的 notFoundCodes 提档：改参数不会有结果，得先把定义建好）。
	if _, err := svc.SetSkillEnabled(context.Background(), "nope", false); err == nil {
		t.Fatal("未知 skill 应报错")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != code.NotFound {
			t.Fatalf("未知 skill 应为 %d，got %v", code.NotFound, err)
		}
	}

	// 2) 内存注册（无定义文件）→ skill_not_persistable，并给出「建成文件」的出路。
	if err := svc.caps.RegisterSkill(&capability.Skill{ID: "in-memory", Body: "# x"}); err != nil {
		t.Fatal(err)
	}
	// acl 的稳定码不进 gerror 文案（msg = Details），故断言人话 + 信封码两条。
	_, err := svc.SetSkillEnabled(context.Background(), "in-memory", false)
	if err == nil || !strings.Contains(err.Error(), "没有定义文件") {
		t.Fatalf("无定义文件应显式失败: %v", err)
	}
	if ge := envelopeCode(t, err); ge != code.InvalidRequest {
		t.Fatalf("无定义文件应落 40001（参数/环境问题，不是资源不存在），got %d (%v)", ge, err)
	}
	if !strings.Contains(err.Error(), "skills/in-memory.json") {
		t.Fatalf("错误应指出应有的路径: %v", err)
	}

	// 3) 未装载能力目录（capabilityDir 为空）→ 同样显式失败。
	empty, _, _ := rigWithRuntime(t)
	if _, err := empty.SetSkillEnabled(context.Background(), "whatever", false); err == nil {
		t.Fatal("未装载目录应报错")
	}

	// 4) 坏 JSON 定义 → 报错且不改文件（启停不该顺手「修好」用户的文件）。
	writeSkill(t, dir, "broken", `{"id": "broken", `)
	svc.LoadCapabilityDefinitions(dir) // 装载容错：坏文件跳过，不报错
	if err := svc.caps.RegisterSkill(&capability.Skill{ID: "broken", Body: "# b"}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SetSkillEnabled(context.Background(), "broken", false)
	if err == nil || !strings.Contains(err.Error(), "不是合法 JSON 对象") {
		t.Fatalf("坏定义应显式失败: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "skills", "broken.json")); string(b) != `{"id": "broken", ` {
		t.Fatalf("坏文件不该被改写: %s", b)
	}
}

// TestSkillActivationReasons 激活判定（§13.4 S5）：适用与否都与「启用」正交，且每条都有原因。
func TestSkillActivationReasons(t *testing.T) {
	inAgent := map[string]bool{"opencode": true}
	cases := []struct {
		name       string
		sk         capability.Skill
		applicable bool
		reasonHas  string
	}{
		{
			name: "applicable",
			sk: capability.Skill{ID: "a", Sources: []capability.SkillSource{
				{Runtime: "opencode", Format: capability.FormatSkillMD}}},
			applicable: true,
		},
		{
			name: "disabled-but-would-apply",
			sk: capability.Skill{ID: "b", Enabled: boolPtr(false), Sources: []capability.SkillSource{
				{Runtime: "opencode", Format: capability.FormatSkillMD}}},
			reasonHas: "已被禁用",
		},
		{
			name: "runtime-not-declared",
			sk: capability.Skill{ID: "c", Sources: []capability.SkillSource{
				{Runtime: "echo", Format: capability.FormatSkillMD}}},
			reasonHas: "都没有在 sources 里声明",
		},
		{
			name:      "no-source",
			sk:        capability.Skill{ID: "d"},
			reasonHas: "未声明任何物化 source",
		},
		{
			name: "format-unproven",
			sk: capability.Skill{ID: "e", Sources: []capability.SkillSource{
				{Runtime: "opencode", Format: "agents/skills"}}},
			reasonHas: "format 未经实证",
		},
	}
	for _, c := range cases {
		act := SkillActivationFor(c.sk, []string{"opencode"}, inAgent)
		if act.Applicable != c.applicable {
			t.Fatalf("%s: applicable=%v want %v（reasons=%v）", c.name, act.Applicable, c.applicable, act.Reasons)
		}
		if c.applicable {
			if len(act.Reasons) != 0 {
				t.Fatalf("%s: 适用不该有原因: %v", c.name, act.Reasons)
			}
			continue
		}
		if len(act.Reasons) == 0 {
			t.Fatalf("%s: 不适用必须给人话原因", c.name)
		}
		joined := strings.Join(act.Reasons, " | ")
		if !strings.Contains(joined, c.reasonHas) {
			t.Fatalf("%s: 原因不含 %q: %s", c.name, c.reasonHas, joined)
		}
	}
	// 禁用的 skill 命中原因里要给出恢复动作（不然用户只看到「不可用」）。
	act := SkillActivationFor(capability.Skill{ID: "f", Enabled: boolPtr(false)}, []string{"opencode"}, inAgent)
	if !strings.Contains(strings.Join(act.Reasons, " | "), "enabled:true") {
		t.Fatalf("禁用原因应给出恢复动作: %v", act.Reasons)
	}
}

// TestAgentCapabilitiesListsInapplicableSkills §13.4 S5：agent 能力目录**不再静默过滤**不适用技能。
func TestAgentCapabilitiesListsInapplicableSkills(t *testing.T) {
	svc, _, _ := rigWithRuntime(t, runtime.Spec{ID: "opencode", McpTransports: []string{"stdio"}})
	svc.agents["ag-1"] = &AgentRec{ID: "ag-1", Name: "A1", Runtimes: []string{"opencode"}, State: AgentStateStopped}
	// 适用（opencode 源）、不适用（只有 echo 源）、禁用但本来适用。
	must := func(s capability.Skill) {
		t.Helper()
		if err := svc.caps.RegisterSkill(&s); err != nil {
			t.Fatal(err)
		}
	}
	must(capability.Skill{ID: "ok", Sources: []capability.SkillSource{{Runtime: "opencode", Format: capability.FormatSkillMD}}})
	must(capability.Skill{ID: "echo-only", Sources: []capability.SkillSource{{Runtime: "echo", Format: capability.FormatSkillMD}}})
	must(capability.Skill{ID: "off", Enabled: boolPtr(false), Sources: []capability.SkillSource{{Runtime: "opencode", Format: capability.FormatSkillMD}}})

	resp, err := svc.AgentCapabilities("ag-1")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]bool{}
	for _, s := range resp.Skills {
		byID[s.ID] = true
	}
	for _, id := range []string{"ok", "echo-only", "off"} {
		if !byID[id] {
			t.Fatalf("不适用/禁用技能也必须列出（否则「技能凭空不见」）: 缺 %s（%+v）", id, resp.Skills)
		}
	}
	for _, s := range resp.Skills {
		switch s.ID {
		case "ok":
			if !s.Enabled || !s.Applicable || len(s.Reasons) != 0 {
				t.Fatalf("ok 应启用且适用: %+v", s)
			}
		case "echo-only":
			if !s.Enabled || s.Applicable || len(s.Reasons) == 0 {
				t.Fatalf("echo-only 应启用但不适用 + 有原因: %+v", s)
			}
		case "off":
			if s.Enabled || s.Applicable {
				t.Fatalf("off 应禁用且不适用: %+v", s)
			}
		}
	}
}

// TestSetSkillEnabledReconcileFailureIsReported 重校失败不回滚定义变更，但必须如实回带原因
// （定义已改，谎报失败会让人以为没生效）。
func TestSetSkillEnabledReconcileFailureIsReported(t *testing.T) {
	svc, dir, root := skillRig(t)
	writeSkill(t, dir, "demo", `{"id":"demo","body":"# d","sources":[{"runtime":"opencode","format":"SKILL.md"}]}`)
	if n := svc.LoadCapabilityDefinitions(dir); n != 1 {
		t.Fatalf("应装载 1 条: %d", n)
	}
	// 把 root 换成一个「文件」——重校扫描必然失败。
	bad := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.SetSkillMaterializeTargets([]SkillMaterializeTarget{{Root: bad}})
	resp, err := svc.SetSkillEnabled(context.Background(), "demo", false)
	if err != nil {
		t.Fatalf("重校失败不该让启停整体失败: %v", err)
	}
	if len(resp.MaterializeErrors) == 0 {
		t.Fatalf("应回带重校失败原因: %+v", resp)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "skills", "demo.json"))
	if !strings.Contains(string(b), "false") {
		t.Fatalf("定义仍应已写回: %s", b)
	}
	_ = root
}

// envelopeCode 取错误信封里的对外码（服务层错误经 code.FromError 归一）。
func envelopeCode(t *testing.T, err error) int {
	t.Helper()
	var ge gerror.Error
	if !errors.As(err, &ge) {
		t.Fatalf("错误不带信封码: %v", err)
	}
	return ge.Code
}

// boolPtr 测试用 *bool。
func boolPtr(v bool) *bool { return &v }

// matStatus 取某 root 的体检状态（测试断言用）。
func matStatus(t *testing.T, svc *Service, id, root string) dtocapability.SkillMaterializationState {
	t.Helper()
	resp, err := svc.SkillMaterialization(id)
	if err != nil {
		t.Fatalf("SkillMaterialization: %v", err)
	}
	for _, st := range resp.States {
		if st.Root == root {
			return st
		}
	}
	t.Fatalf("体检结果里没有 root=%s: %+v", root, resp.States)
	return dtocapability.SkillMaterializationState{}
}

// TestSkillMaterializationStatuses §13.4 S2 审计面：每个 status 都可判定、都对应一个真实情况。
func TestSkillMaterializationStatuses(t *testing.T) {
	svc, dir, root := skillRig(t)
	writeSkill(t, dir, "demo", `{"id":"demo","name":"Demo","body":"# demo",
		"sources":[{"runtime":"opencode","format":"SKILL.md"}]}`)
	svc.LoadCapabilityDefinitions(dir)
	// 用「按 runtime 分 root」的多 root 路径（单 root 兼容路径不登记归属方，见 addRuntime）。
	svc.SetSkillMaterializeTargets([]SkillMaterializeTarget{{Root: root, Runtime: "opencode"}})
	sk, _ := svc.caps.SkillByID("demo")

	// 1) 还没物化 → missing。
	if st := matStatus(t, svc, "demo", root); st.Status != "missing" || st.Hash == "" {
		t.Fatalf("未物化应为 missing 且给出期望摘要: %+v", st)
	}
	// 2) 物化后 → in_sync（期望摘要 == 实测摘要）。
	if w, _, err := svc.ReconcileSkillsFor(root, "opencode"); err != nil || w != 1 {
		t.Fatalf("物化失败: w=%d err=%v", w, err)
	}
	st := matStatus(t, svc, "demo", root)
	if st.Status != "in_sync" || st.Hash != st.ActualHash || st.WrittenAt == "" {
		t.Fatalf("物化后应为 in_sync 且期望==实测: %+v", st)
	}
	if len(st.Owners) != 1 || st.Owners[0] != "opencode" {
		t.Fatalf("应记录归属 runtime: %+v", st.Owners)
	}
	// 3) 篡改 → tampered（且给出实测摘要）。
	matFile := filepath.Join(root, "demo", "SKILL.md")
	b, _ := os.ReadFile(matFile)
	if err := os.WriteFile(matFile, append(b, []byte("塞一行\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	st = matStatus(t, svc, "demo", root)
	if st.Status != "tampered" || st.ActualHash == st.Hash {
		t.Fatalf("篡改后应为 tampered: %+v", st)
	}
	// 4) 重校修复 → 回到 in_sync。
	if _, _, err := svc.ReconcileSkillsFor(root, "opencode"); err != nil {
		t.Fatal(err)
	}
	if st = matStatus(t, svc, "demo", root); st.Status != "in_sync" {
		t.Fatalf("重校后应修复为 in_sync: %+v", st)
	}
	// 5) 删掉摘要 sidecar（模拟老产物）→ stale。
	if err := os.Remove(filepath.Join(root, "demo", capability.ProvenanceFileName)); err != nil {
		t.Fatal(err)
	}
	if st = matStatus(t, svc, "demo", root); st.Status != "stale" || !strings.Contains(st.Detail, "摘要") {
		t.Fatalf("无摘要应为 stale: %+v", st)
	}
	// 6) 非自有目录（无归属标记）→ user_owned。
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc.SetSkillMaterializeTargets([]SkillMaterializeTarget{{Root: other}})
	if st = matStatus(t, svc, "demo", other); st.Status != "user_owned" {
		t.Fatalf("无标记目录应为 user_owned: %+v", st)
	}
	// 7) 该 runtime 没有已实证 format 的 source → not_applicable。
	svc.SetSkillMaterializeTargets([]SkillMaterializeTarget{{Root: other, Runtime: "echo"}})
	if st = matStatus(t, svc, "demo", other); st.Status != "not_applicable" {
		t.Fatalf("未声明该 runtime 应为 not_applicable: %+v", st)
	}
	// 8) 禁用：副本还在 → stale；重校撤回后 → disabled。
	svc.SetSkillMaterializeTargets([]SkillMaterializeTarget{{Root: root, Runtime: "opencode"}})
	if _, _, err := svc.ReconcileSkillsFor(root, "opencode"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetSkillEnabled(context.Background(), "demo", false); err != nil {
		t.Fatal(err)
	}
	// SetSkillEnabled 已同步重校撤回 → 期望 disabled。
	if st = matStatus(t, svc, "demo", root); st.Status != "disabled" {
		t.Fatalf("禁用且已撤回应为 disabled: %+v", st)
	}
	// 手动放回一个受管副本 → stale（禁用但副本还在）。
	if _, err := capability.MaterializeSkill(root, sk); err != nil {
		t.Fatal(err) // sk.Enabled 仍是 nil（副本来自注册表快照）→ 可物化
	}
	if st = matStatus(t, svc, "demo", root); st.Status != "stale" || !strings.Contains(st.Detail, "禁用") {
		t.Fatalf("禁用但副本在应为 stale: %+v", st)
	}
	// 9) 没有登记物化目标 → notes（而不是空数组装没事）。
	empty, _, _ := rigWithRuntime(t)
	if err := empty.caps.RegisterSkill(&capability.Skill{ID: "x", Body: "# x"}); err != nil {
		t.Fatal(err)
	}
	resp, err := empty.SkillMaterialization("x")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.States) != 0 || len(resp.Notes) == 0 {
		t.Fatalf("无物化目标应给 notes: %+v", resp)
	}
	// 10) 未知 skill → 40401。
	if _, err := svc.SkillMaterialization("nope"); err == nil {
		t.Fatal("未知 skill 应报错")
	} else if envelopeCode(t, err) != code.NotFound {
		t.Fatalf("未知 skill 应为 %d，got %v", code.NotFound, err)
	}
}
