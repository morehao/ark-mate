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
	current := []Skill{{ID: "keep", Name: "Keep", Body: "# keep",
		Sources: []SkillSource{{Runtime: "opencode", Path: root, Format: "SKILL.md"}}}}

	w, r, err := ReconcileSkills(root, current)
	if err != nil {
		t.Fatal(err)
	}
	if w != 1 || r != 1 {
		t.Fatalf("期望 written=1 removed=1，got w=%d r=%d", w, r)
	}
	if b, err := os.ReadFile(filepath.Join(root, "keep", "SKILL.md")); err != nil || string(b) != "# keep" {
		t.Fatal("keep 未写出")
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
