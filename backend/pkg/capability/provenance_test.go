package capability

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkdir 建目录并写文件（测试夹具）。
func mkdir(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPayloadHashSensitivity 摘要对内容与权限都敏感（chmod 也算变更）。
func TestPayloadHashSensitivity(t *testing.T) {
	a := PayloadHash("# skill\nbody")
	if a != PayloadHash("# skill\nbody") {
		t.Fatal("同内容摘要应稳定")
	}
	if a == PayloadHash("# skill\nbody\n") {
		t.Fatal("不同内容摘要应不同")
	}

	dirA := filepath.Join(t.TempDir(), "a")
	dirB := filepath.Join(t.TempDir(), "b")
	mkdir(t, dirA, map[string]string{"SKILL.md": "x"})
	mkdir(t, dirB, map[string]string{"SKILL.md": "x"})
	if err := os.Chmod(filepath.Join(dirB, "SKILL.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	ha, err := ComputeDirHash(dirA)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := ComputeDirHash(dirB)
	if err != nil {
		t.Fatal(err)
	}
	if ha == hb {
		t.Fatal("权限不同应产生不同摘要（否则 chmod 篡改看不见）")
	}
}

// TestComputeDirHashDeterministic 摘要与遍历顺序无关、与文件集合有关。
func TestComputeDirHashDeterministic(t *testing.T) {
	d1 := filepath.Join(t.TempDir(), "d1")
	d2 := filepath.Join(t.TempDir(), "d2")
	mkdir(t, d1, map[string]string{"b.txt": "B", "a.txt": "A", "sub/c.txt": "C"})
	mkdir(t, d2, map[string]string{"sub/c.txt": "C", "a.txt": "A", "b.txt": "B"})
	h1, err := ComputeDirHash(d1)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := ComputeDirHash(d2)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("相同文件集合应得到相同摘要（排序后哈希）")
	}
	// 相对路径入哈希：把文件挪到子目录（内容不变）也要变。
	mkdir(t, filepath.Join(d2, "sub"), map[string]string{"a.txt": "A"})
	if err := os.Remove(filepath.Join(d2, "a.txt")); err != nil {
		t.Fatal(err)
	}
	h3, err := ComputeDirHash(d2)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h2 {
		t.Fatal("路径变了摘要应跟着变")
	}
}

// TestComputeDirHashSkipsBookkeeping 记账文件不入摘要——否则每次重校都会"看起来变了"。
func TestComputeDirHashSkipsBookkeeping(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	mkdir(t, dir, map[string]string{"SKILL.md": "x"})
	before, err := ComputeDirHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	mkdir(t, dir, map[string]string{forgeMarker: "id\nopencode\n", ProvenanceFileName: "{}"})
	after, err := ComputeDirHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("归属标记与摘要 sidecar 不该入载荷摘要")
	}
}

// TestComputeDirHashSymlinks 软链不 follow（字面目标入哈希），逃逸即拒。
func TestComputeDirHashSymlinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "d")
	mkdir(t, dir, map[string]string{"SKILL.md": "x", "real.txt": "same"})
	if err := os.Symlink("real.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("该平台不支持软链: %v", err)
	}
	withLink, err := ComputeDirHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 改链目标（内容仍指向同一份文本）→ 摘要必须变（目标字面量入哈希）。
	if err := os.Remove(filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("./real.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	after, err := ComputeDirHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if withLink == after {
		t.Fatal("软链目标字面量入哈希：改了 target 字符串摘要应不同")
	}

	// 逃逸：指向目录之外。
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := ComputeDirHash(dir); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("逃出物化目录的软链应被拒: %v", err)
	}
}

// TestVerifyDirDetectsTamper 摘要是"可判定事实"：改内容/加文件/改权限都能看出来。
func TestVerifyDirDetectsTamper(t *testing.T) {
	root := t.TempDir()
	sk := Skill{ID: "demo", Name: "Demo", Body: "# demo",}
	if _, err := MaterializeSkill(ocSpec(root), sk); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "demo")
	prov, err := VerifyDir(dir)
	if err != nil {
		t.Fatalf("刚物化应自洽: %v", err)
	}
	if prov.SkillID != "demo" || prov.Format != FormatSkillMD || prov.Hash == "" || prov.WrittenAt.IsZero() {
		t.Fatalf("摘要内容不符: %+v", prov)
	}
	if prov.Bytes == 0 {
		t.Fatal("应记录载荷字节数")
	}

	cases := []struct {
		name   string
		mutate func(t *testing.T)
	}{
		{"改内容", func(t *testing.T) {
			p := filepath.Join(dir, "SKILL.md")
			b, _ := os.ReadFile(p)
			if err := os.WriteFile(p, append(b, []byte("注入一行\n")...), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"加文件", func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "extra.md"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"改权限", func(t *testing.T) {
			if err := os.Chmod(filepath.Join(dir, "SKILL.md"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		// 每个用例都在干净产物上做一次（重新物化覆盖）。
		if _, err := MaterializeSkill(ocSpec(root), sk); err != nil {
			t.Fatal(err)
		}
		c.mutate(t)
		if _, err := VerifyDir(dir); err == nil {
			t.Fatalf("%s：摘要应报不一致", c.name)
		} else {
			var mismatch *HashMismatchError
			if !errors.As(err, &mismatch) {
				t.Fatalf("%s：应为 HashMismatchError，got %v", c.name, err)
			}
		}
	}
}

// TestMaterializeRefusesNonOwnedDir §13.4 S3：目标目录已存在但不带归属标记 → 拒绝，且不动用户文件。
func TestMaterializeRefusesNonOwnedDir(t *testing.T) {
	root := t.TempDir()
	userFile := filepath.Join(root, "demo", "SKILL.md")
	mkdir(t, filepath.Join(root, "demo"), map[string]string{"SKILL.md": "用户自己写的"})

	sk := Skill{ID: "demo", Body: "# 平台的",}
	_, err := MaterializeSkill(ocSpec(root), sk)
	if err == nil || !strings.Contains(err.Error(), "归属标记") {
		t.Fatalf("非自有目录应拒绝覆盖: %v", err)
	}
	if b, _ := os.ReadFile(userFile); string(b) != "用户自己写的" {
		t.Fatalf("用户文件被覆盖: %q", b)
	}
	// 失败路径不留 staging 残留。
	assertNoLeftovers(t, root)
}

// TestMaterializeRefusesSymlinkDir 目标目录本身是软链 → 拒绝（往软链写等于写到别处）。
func TestMaterializeRefusesSymlinkDir(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, "demo")); err != nil {
		t.Skipf("该平台不支持软链: %v", err)
	}
	sk := Skill{ID: "demo", Body: "# x",}
	if _, err := MaterializeSkill(ocSpec(root), sk); err == nil || !strings.Contains(err.Error(), "软链") {
		t.Fatalf("软链目标应拒绝: %v", err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("不该写到软链指向的目录: %v", entries)
	}
}

// TestMaterializeReplacesAtomically 替换后：只有新内容、无 staging/backup 残留、摘要自洽。
func TestMaterializeReplacesAtomically(t *testing.T) {
	root := t.TempDir()
	sk := Skill{ID: "demo", Name: "Demo", Body: "# v1",}
	if _, err := MaterializeSkill(ocSpec(root), sk); err != nil {
		t.Fatal(err)
	}
	first, err := ReadProvenance(filepath.Join(root, "demo"))
	if err != nil {
		t.Fatal(err)
	}

	sk.Body = "# v2"
	if _, err := MaterializeSkill(ocSpec(root), sk); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "demo", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "# v2") {
		t.Fatalf("应替换为新内容: %q", b)
	}
	second, err := VerifyDir(filepath.Join(root, "demo"))
	if err != nil {
		t.Fatalf("替换后摘要应自洽: %v", err)
	}
	if first.Hash == second.Hash {
		t.Fatal("内容变了摘要应变")
	}
	// 归属集合在替换中保留（换内容是"换内容"，不是重置归属）。
	if !strings.Contains(second.SkillID, "demo") {
		t.Fatalf("摘要的 skill id 不符: %+v", second)
	}
	assertNoLeftovers(t, root)
}

// assertNoLeftovers 物化根下不该留 staging/backup 目录。
func assertNoLeftovers(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".forge-stage-") || strings.HasSuffix(e.Name(), ".forge-old") {
			t.Fatalf("残留临时目录: %s", e.Name())
		}
	}
}

// TestReconcileRepairsTamperedArtifact 审计闭环：产物被改 → 重校发现并修复（摘要先判"过期"）。
func TestReconcileRepairsTamperedArtifact(t *testing.T) {
	root := t.TempDir()
	sk := Skill{ID: "demo", Name: "Demo", Body: "# demo",}
	if _, _, err := ReconcileSkills(ocSpec(root), []Skill{sk}); err != nil {
		t.Fatal(err)
	}
	// 幂等：内容/摘要都没变 → 0 写。
	if w, r, err := ReconcileSkills(ocSpec(root), []Skill{sk}); err != nil || w != 0 || r != 0 {
		t.Fatalf("幂等重校应 0/0: w=%d r=%d err=%v", w, r, err)
	}
	// 篡改：往 SKILL.md 追加一行（内容比对与摘要都能看出，但这里验的是重校会**修**）。
	p := filepath.Join(root, "demo", "SKILL.md")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("被塞进来的一行\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := VerifyDir(filepath.Join(root, "demo")); err == nil {
		t.Fatal("篡改后摘要应报不一致")
	}

	w, r, err := ReconcileSkills(ocSpec(root), []Skill{sk})
	if err != nil || w != 1 || r != 0 {
		t.Fatalf("重校应修复 1 个: w=%d r=%d err=%v", w, r, err)
	}
	if _, err := VerifyDir(filepath.Join(root, "demo")); err != nil {
		t.Fatalf("修复后应自洽: %v", err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "被塞进来的一行") {
		t.Fatalf("篡改内容应被覆盖回去: %q", b)
	}
	assertNoLeftovers(t, root)
}

// TestReconcileUpgradesLegacyArtifact 老产物（无摘要 sidecar）在第一次重校时被升级，之后幂等。
func TestReconcileUpgradesLegacyArtifact(t *testing.T) {
	root := t.TempDir()
	sk := Skill{ID: "demo", Name: "Demo", Body: "# demo",}
	if _, err := MaterializeSkill(ocSpec(root), sk); err != nil {
		t.Fatal(err)
	}
	// 模拟旧版本产物：删掉 sidecar，只留 SKILL.md + 归属标记（内容与期望一致）。
	dir := filepath.Join(root, "demo")
	if err := os.Remove(filepath.Join(dir, ProvenanceFileName)); err != nil {
		t.Fatal(err)
	}
	w, r, err := ReconcileSkills(ocSpec(root), []Skill{sk})
	if err != nil || w != 1 || r != 0 {
		t.Fatalf("无摘要的老产物应升级一次: w=%d r=%d err=%v", w, r, err)
	}
	if _, err := VerifyDir(dir); err != nil {
		t.Fatalf("升级后应有自洽摘要: %v", err)
	}
	if w, r, err := ReconcileSkills(ocSpec(root), []Skill{sk}); err != nil || w != 0 || r != 0 {
		t.Fatalf("升级后应幂等: w=%d r=%d err=%v", w, r, err)
	}
}

// TestReconcileCleansStagingLeftovers 崩溃残留（staging 目录 / 旧产物备份）在下一次重校被清扫，
// 且清扫**不动**别的目录——否则一个失败的替换会在物化根里永久躺一个没人认领的目录。
func TestReconcileCleansStagingLeftovers(t *testing.T) {
	root := t.TempDir()
	sk := Skill{ID: "demo", Name: "Demo", Body: "# demo",}
	if _, _, err := ReconcileSkills(ocSpec(root), []Skill{sk}); err != nil {
		t.Fatal(err)
	}
	// 模拟中断残留 + 一个用户自有目录（名字不撞保留名）。
	mkdir(t, filepath.Join(root, ".forge-stage-123456"), map[string]string{"SKILL.md": "半成品"})
	mkdir(t, filepath.Join(root, "demo.forge-old"), map[string]string{"SKILL.md": "旧产物"})
	mkdir(t, filepath.Join(root, "user-own"), map[string]string{"note.txt": "用户的东西"})

	if _, _, err := ReconcileSkills(ocSpec(root), []Skill{sk}); err != nil {
		t.Fatal(err)
	}
	assertNoLeftovers(t, root)
	if _, err := os.Stat(filepath.Join(root, "user-own", "note.txt")); err != nil {
		t.Fatalf("用户目录不该被清扫: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "demo", "SKILL.md")); err != nil {
		t.Fatalf("正常产物不该受影响: %v", err)
	}
}
