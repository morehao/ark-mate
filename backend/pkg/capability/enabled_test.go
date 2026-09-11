package capability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// disabled 便捷构造：显式 false。
func disabled() *bool { f := false; return &f }

// TestSkillIsEnabledEnabled 默认启用；显式 false 才禁用（指针区分「未声明」与「关掉」）。
func TestSkillIsEnabledDefault(t *testing.T) {
	if !(Skill{ID: "a"}).IsEnabled() {
		t.Fatal("未声明 enabled 应默认启用")
	}
	if (Skill{ID: "a", Enabled: disabled()}).IsEnabled() {
		t.Fatal("enabled:false 应视为禁用")
	}
	tr := true
	if !(Skill{ID: "a", Enabled: &tr}).IsEnabled() {
		t.Fatal("enabled:true 应启用")
	}
}

// TestMaterializeDisabledSkillRejected 禁用 = 不投放：物化层直接拒绝（最后一米）。
func TestMaterializeDisabledSkillRejected(t *testing.T) {
	root := t.TempDir()
	sk := Skill{ID: "off", Body: "# off", Enabled: disabled(),
		Sources: []SkillSource{{Runtime: "opencode", Format: FormatSkillMD}}}
	_, err := MaterializeSkill(root, sk)
	if err == nil || !strings.Contains(err.Error(), "已禁用") {
		t.Fatalf("禁用 skill 应拒物化: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "off")); !os.IsNotExist(statErr) {
		t.Fatalf("禁用不应留下目录: %v", statErr)
	}
}

// TestReconcileWithdrawsDisabledSkill 禁用是 overlay：定义留着，但已物化的副本在重校时被**撤回**。
func TestReconcileWithdrawsDisabledSkill(t *testing.T) {
	root := t.TempDir()
	on := Skill{ID: "demo", Name: "Demo", Body: "# demo",
		Sources: []SkillSource{{Runtime: "opencode", Format: FormatSkillMD}}}

	written, removed, err := ReconcileSkills(root, []Skill{on})
	if err != nil || written != 1 || removed != 0 {
		t.Fatalf("首次物化不符: w=%d r=%d err=%v", written, removed, err)
	}
	dir := filepath.Join(root, "demo")
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatalf("应已物化: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, forgeMarker)); err != nil {
		t.Fatalf("应带归属标记: %v", err)
	}

	// 关掉它 → 重校应摘掉目录（且不动定义本身——定义在别处，这里只验物化侧）。
	off := on
	off.Enabled = disabled()
	written, removed, err = ReconcileSkills(root, []Skill{off})
	if err != nil || written != 0 || removed != 1 {
		t.Fatalf("禁用后重校应撤回 1 个: w=%d r=%d err=%v", written, removed, err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("禁用后目录应被撤回: %v", statErr)
	}

	// 再启用 → 重新写出（可逆）。
	written, removed, err = ReconcileSkills(root, []Skill{on})
	if err != nil || written != 1 || removed != 0 {
		t.Fatalf("重新启用应再物化: w=%d r=%d err=%v", written, removed, err)
	}
}

// TestReconcileKeepsUserOwnedDirWhenDisabled 撤回只动带归属标记的目录：禁用后
// 用户自建的同名目录绝不能被误删（overlay 的边界）。
func TestReconcileKeepsUserOwnedDirWhenDisabled(t *testing.T) {
	root := t.TempDir()
	userDir := filepath.Join(root, "demo")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "SKILL.md"), []byte("用户自己写的"), 0o644); err != nil {
		t.Fatal(err)
	}
	off := Skill{ID: "demo", Body: "# demo", Enabled: disabled(),
		Sources: []SkillSource{{Runtime: "opencode", Format: FormatSkillMD}}}
	if _, removed, err := ReconcileSkills(root, []Skill{off}); err != nil || removed != 0 {
		t.Fatalf("无归属标记的目录不该删: r=%d err=%v", removed, err)
	}
	if b, err := os.ReadFile(filepath.Join(userDir, "SKILL.md")); err != nil || string(b) != "用户自己写的" {
		t.Fatalf("用户文件被动了: %q %v", b, err)
	}
}

// TestListSkillsKeepsDisabled 启停视图要能看见被禁用的条目（否则关掉就再也开不回来）。
func TestListSkillsKeepsDisabled(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterSkill(&Skill{ID: "a", Body: "# a"}); err != nil {
		t.Fatal(err)
	}
	got, ok := r.SetSkillEnabled("a", false)
	if !ok || got.IsEnabled() {
		t.Fatalf("就地改写失败: %+v ok=%v", got, ok)
	}
	list := r.ListSkills()
	if len(list) != 1 || list[0].IsEnabled() {
		t.Fatalf("禁用条目应仍在列表里且标记为禁用: %+v", list)
	}
	// 正文/来源不受启停影响（overlay 不是重新注册）。
	if list[0].Body != "# a" {
		t.Fatalf("启停不该动正文: %+v", list[0])
	}
	if again, ok := r.SetSkillEnabled("a", true); !ok || !again.IsEnabled() {
		t.Fatalf("应能恢复: %+v ok=%v", again, ok)
	}
	if _, ok := r.SetSkillEnabled("missing", true); ok {
		t.Fatal("未知 id 应返回 ok=false")
	}
}

// TestSupportedFormat 激活判定的 format 白名单与物化层一致（两处漂移会让「适用」说谎）。
func TestSupportedFormat(t *testing.T) {
	for _, f := range []string{FormatSkillMD, FormatSkillMDFrontmatter} {
		if !SupportedFormat(f) {
			t.Fatalf("%q 应已实证", f)
		}
		if _, _, ok := materializedContentFor(Skill{ID: "x", Body: "b",
			Sources: []SkillSource{{Runtime: "opencode", Format: f}}}, "opencode"); !ok {
			t.Fatalf("%q：SupportedFormat 说可用，物化却不认", f)
		}
	}
	if SupportedFormat("agents/skills") {
		t.Fatal("未实证 format 不该报 supported")
	}
}
