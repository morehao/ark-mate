package capability

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMaterializeSkillOpenCodeConvention 按 opencode v1 实证约定物化
// （<root>/<id>/SKILL.md，自动扫描无需 config）；幂等覆盖；未实测 format 跳过。
func TestMaterializeSkillOpenCodeConvention(t *testing.T) {
	root := t.TempDir()
	sk := Skill{ID: "forge-demo", Name: "Demo", Body: "# demo skill\nbody",
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
	if string(b) != sk.Body {
		t.Fatalf("SKILL.md 内容不符")
	}

	// 幂等覆盖（改 body 再物化）。
	sk.Body = "# v2 body"
	if _, err := MaterializeSkill(root, sk); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(want); string(b) != "# v2 body" {
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
