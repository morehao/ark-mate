package capability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMaterializeSkillConvention 物化约定（2026-09-09 双底座实测修正）：
// `<root>/<id>/SKILL.md` + **保证 YAML frontmatter**（opencode 丢弃无 frontmatter 的
// SKILL.md；dsh 要求 name 与 description 同时存在）；幂等覆盖；未实测 format 跳过。
func TestMaterializeSkillConvention(t *testing.T) {
	root := t.TempDir()
	sk := Skill{ID: "forge-demo", Name: "Demo", Description: "演示技能", Body: "# demo skill\nbody",
		Sources: []SkillSource{
			{Runtime: "opencode", Path: root, Format: "SKILL.md"},
			{Runtime: "deepseekharness", Path: "/tmp/x", Format: "agents/skills"}, // 未实测 → 跳过
		}}
	p, err := MaterializeSkill(root, sk)
	if err != nil {
		t.Fatalf("MaterializeSkill: %v", err)
	}
	want := filepath.Join(root, "forge-demo", "SKILL.md")
	if p != want {
		t.Fatalf("物化路径不符: %s != %s", p, want)
	}
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	wantContent := "---\nname: \"forge-demo\"\ndescription: \"演示技能\"\n---\n" + sk.Body
	if string(b) != wantContent {
		t.Fatalf("SKILL.md 内容不符:\n%q\nwant:\n%q", b, wantContent)
	}

	// 幂等覆盖（改 body 再物化）。
	sk.Body = "# v2 body"
	if _, err := MaterializeSkill(root, sk); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(want); !strings.HasSuffix(string(b), "# v2 body") {
		t.Fatal("幂等覆盖失败")
	}

	if err := RemoveSkillDir(root, "forge-demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatal("物化目录应已删除")
	}
}

func TestMaterializeSkillValidation(t *testing.T) {
	root := t.TempDir()
	if _, err := MaterializeSkill("", Skill{ID: "a", Body: "x", Sources: []SkillSource{{Runtime: "opencode", Format: "SKILL.md"}}}); err == nil {
		t.Fatal("root 空应报错")
	}
	if _, err := MaterializeSkill(root, Skill{ID: "a", Body: "", Sources: []SkillSource{{Runtime: "opencode", Format: "SKILL.md"}}}); err == nil {
		t.Fatal("body 空应报错")
	}
	if _, err := MaterializeSkill(root, Skill{ID: "a", Body: "x", Sources: []SkillSource{{Runtime: "opencode", Path: "/tmp", Format: "agents/skills"}}}); err == nil {
		t.Fatal("无可物化 source 应报错")
	}
}

// TestReconcileSkills 删除联动 + 重校：带 forge 标记且不在注册表的目录被清，
// 用户自有（无标记）目录绝不动；注册表 skill 缺失则写出。
func TestReconcileSkills(t *testing.T) {
	root := t.TempDir()
	// 预置：forge 管理的 stale 目录（带标记）+ 用户自有 user-x（无标记）。
	if err := os.MkdirAll(filepath.Join(root, "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stale", forgeMarker), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "user-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "user-x", "SKILL.md"), []byte("user skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	current := []Skill{{ID: "keep", Name: "Keep", Description: "保留", Body: "# keep",
		Sources: []SkillSource{{Runtime: "opencode", Path: root, Format: "SKILL.md"}}}}

	w, r, err := ReconcileSkills(root, current)
	if err != nil {
		t.Fatal(err)
	}
	if w != 1 || r != 1 {
		t.Fatalf("期望 written=1 removed=1，got w=%d r=%d", w, r)
	}
	wantKeep := "---\nname: \"keep\"\ndescription: \"保留\"\n---\n# keep"
	if b, err := os.ReadFile(filepath.Join(root, "keep", "SKILL.md")); err != nil || string(b) != wantKeep {
		t.Fatalf("keep 未写出或内容不符: %q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, "stale")); !os.IsNotExist(err) {
		t.Fatal("stale（forge 标记、不在注册表）应被删除联动")
	}
	if _, err := os.Stat(filepath.Join(root, "user-x")); err != nil {
		t.Fatal("用户自有目录不应被动")
	}
	// 幂等重校：再跑应无写无删。
	if w, r, err := ReconcileSkills(root, current); err != nil || w != 0 || r != 0 {
		t.Fatalf("幂等重校应 0/0，got w=%d r=%d err=%v", w, r, err)
	}
}

// TestMaterializeWritesMarker 物化写归属标记（删除联动依据）。
func TestMaterializeWritesMarker(t *testing.T) {
	root := t.TempDir()
	sk := Skill{ID: "m1", Body: "# m1", Sources: []SkillSource{{Runtime: "opencode", Format: "SKILL.md"}}}
	if _, err := MaterializeSkill(root, sk); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "m1", forgeMarker)); err != nil {
		t.Fatalf("归属标记缺失: %v", err)
	}
}

// TestMaterializeSkillDshFrontmatter dsh 约定（2026-09-09 实证：dsh-skill-filesystem 丢弃
// 无 YAML frontmatter 的 SKILL.md）：format=SKILL.md+frontmatter 时自动合成 name/description。
func TestMaterializeSkillDshFrontmatter(t *testing.T) {
	root := t.TempDir()
	sk := Skill{ID: "plan", Name: "规划", Description: "先规划再动手", Body: "# plan\n正文",
		Sources: []SkillSource{{Runtime: "deepseekharness", Path: root, Format: FormatSkillMDFrontmatter}}}
	p, err := MaterializeSkill(root, sk)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	want := "---\nname: \"plan\"\ndescription: \"先规划再动手\"\n---\n# plan\n正文"
	if got != want {
		t.Fatalf("dsh 物化内容不符:\n%q\nwant:\n%q", got, want)
	}

	// 已有 frontmatter 的 body：保留用户内容，只补缺失必填键。
	sk2 := Skill{ID: "s2", Description: "描述", Body: "---\nname: \"custom\"\nwhenToUse: x\n---\nbody",
		Sources: []SkillSource{{Runtime: "deepseekharness", Format: FormatSkillMDFrontmatter}}}
	if _, err := MaterializeSkill(root, sk2); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(root, "s2", "SKILL.md"))
	got2 := string(b2)
	for _, wantSub := range []string{"name: \"custom\"", "whenToUse: x", "description: \"描述\"", "body"} {
		if !strings.Contains(got2, wantSub) {
			t.Fatalf("保留/补全不符，缺 %q：\n%s", wantSub, got2)
		}
	}
	if strings.Count(got2, "name:") != 1 {
		t.Fatalf("name 不应重复：\n%s", got2)
	}

	// description 空 → 退化为 name（dsh 缺 description 会丢弃整条）。
	sk3 := Skill{ID: "s3", Name: "N3", Body: "x", Sources: []SkillSource{{Runtime: "deepseekharness", Format: FormatSkillMDFrontmatter}}}
	if _, err := MaterializeSkill(root, sk3); err != nil {
		t.Fatal(err)
	}
	b3, _ := os.ReadFile(filepath.Join(root, "s3", "SKILL.md"))
	if !strings.Contains(string(b3), `description: "N3"`) {
		t.Fatalf("空 description 应退化为 name：\n%s", b3)
	}

	// 多 source（opencode + dsh）指向同一目标文件：两种 format 语义等价（都保证 frontmatter），
	// 取首个即可，产物对两个底座同时可加载。
	sk4 := Skill{ID: "s4", Name: "S4", Description: "d", Body: "# s4",
		Sources: []SkillSource{
			{Runtime: "opencode", Format: FormatSkillMD},
			{Runtime: "deepseekharness", Format: FormatSkillMDFrontmatter},
		}}
	if _, err := MaterializeSkill(root, sk4); err != nil {
		t.Fatal(err)
	}
	if b4, _ := os.ReadFile(filepath.Join(root, "s4", "SKILL.md")); string(b4) != "---\nname: \"s4\"\ndescription: \"d\"\n---\n# s4" {
		t.Fatalf("多 source 产物应带 frontmatter：%q", b4)
	}

	// 重校幂等：dsh 格式内容一致时不再写（独立 root，避免上面的 s2/s3/s4 触发删除联动）。
	idemRoot := t.TempDir()
	current := []Skill{{ID: "plan", Name: "规划", Description: "先规划再动手", Body: "# plan\n正文",
		Sources: []SkillSource{{Runtime: "deepseekharness", Path: idemRoot, Format: FormatSkillMDFrontmatter}}}}
	if w, _, err := ReconcileSkills(idemRoot, current); err != nil || w != 1 {
		t.Fatalf("首次重校应写 1，got w=%d err=%v", w, err)
	}
	if w, r, err := ReconcileSkills(idemRoot, current); err != nil || w != 0 || r != 0 {
		t.Fatalf("dsh 格式幂等重校应 0/0，got w=%d r=%d err=%v", w, r, err)
	}
	// body 变更 → 重校写出。
	current[0].Body = "# plan v2"
	if w, _, err := ReconcileSkills(idemRoot, current); err != nil || w != 1 {
		t.Fatalf("body 变更应重写，got w=%d err=%v", w, err)
	}
	if b5, _ := os.ReadFile(filepath.Join(idemRoot, "plan", "SKILL.md")); !strings.HasSuffix(string(b5), "# plan v2") {
		t.Fatalf("重校内容不符：\n%s", b5)
	}
}
