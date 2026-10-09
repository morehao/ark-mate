package capability

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/pkg/runtime"
)

// testSpec 构造一处投影形态（落点 <id>/SKILL.md，即两个 adapter 当前的原生约定）。
func testSpec(root, owner, format string) MaterializeSpec {
	return MaterializeSpec{Root: root, Layout: layoutIDToken + "/SKILL.md", Format: format, Owner: owner}
}

// ocSpec / dshSpec 两个底座的形态（当前 Format 语义等价，落点相同）。
func ocSpec(root string) MaterializeSpec { return testSpec(root, "opencode", FormatSkillMD) }
func dshSpec(root string) MaterializeSpec {
	return testSpec(root, "deepseekharness", FormatSkillMDFrontmatter)
}

// TestMaterializeSkillConvention 物化约定（2026-09-09 双底座实测修正）：
// `<root>/<id>/SKILL.md` + **保证 YAML frontmatter**（opencode 丢弃无 frontmatter 的
// SKILL.md；dsh 要求 name 与 description 同时存在）；幂等覆盖。
func TestMaterializeSkillConvention(t *testing.T) {
	root := t.TempDir()
	spec := ocSpec(root)
	sk := Skill{ID: "forge-demo", Name: "Demo", Description: "演示技能", Body: "# demo skill\nbody"}
	p, err := MaterializeSkill(spec, sk)
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
	if _, err := MaterializeSkill(spec, sk); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(want); !strings.HasSuffix(string(b), "# v2 body") {
		t.Fatal("幂等覆盖失败")
	}
}

func TestMaterializeSkillValidation(t *testing.T) {
	root := t.TempDir()
	rootless := ocSpec("")
	if _, err := MaterializeSkill(rootless, Skill{ID: "a", Body: "x"}); err == nil {
		t.Fatal("root 空应报错")
	}
	if _, err := MaterializeSkill(ocSpec(root), Skill{ID: "a", Body: ""}); err == nil {
		t.Fatal("body 空应报错")
	}
	// format 未实证：拒绝（Format 取自投影声明，声明错了要在这里炸，而不是写出个读不到的文件）。
	if _, err := MaterializeSkill(testSpec(root, "opencode", "agents/skills"), Skill{ID: "a", Body: "x"}); err == nil {
		t.Fatal("未实证 format 应报错")
	} else if !strings.Contains(err.Error(), "未实证") {
		t.Fatalf("format 报错应说明未实证：%v", err)
	}
	// 落点非法：平铺成单个文件（没有独占目录 → 归属标记与 sidecar 无处可落）。
	flat := MaterializeSpec{Root: root, Layout: layoutIDToken + ".md", Format: FormatSkillMD, Owner: "opencode"}
	if _, err := MaterializeSkill(flat, Skill{ID: "a", Body: "x"}); err == nil {
		t.Fatal("无独占目录的落点应报错")
	} else if !strings.Contains(err.Error(), "目录") {
		t.Fatalf("落点报错应说明缺目录：%v", err)
	}
	// id 注入：带路径分隔符的 id 会把产物写到投影根之外。
	inj := Skill{ID: "../escape", Body: "x"}
	if _, err := MaterializeSkill(ocSpec(root), inj); err == nil {
		t.Fatal("含路径分隔符的 id 应被拒")
	}
	// 词法闸（导出 API 的第二道，与注册表同一条规则）：id 会**原样写进 frontmatter 的 name**，
	// 而 dsh 对 name 强制 `^[a-z0-9]+(?:-[a-z0-9]+)*$`、不合规只 warn 就丢弃整条 ——
	// 所以「路径安全但词法不合规」的 id 也必须在这里被拒，否则平台自报 in_sync、底座永不加载。
	badLex := Skill{ID: "Probe_Bad.Id", Body: "x"}
	if _, err := MaterializeSkill(ocSpec(root), badLex); err == nil {
		t.Fatal("不符底座词法的 id 应被拒")
	} else if !strings.Contains(err.Error(), "词法") {
		t.Fatalf("id 报错应说明词法：%v", err)
	}
}

// TestApplyLayoutMatchesRuntime 两处落点规则的取值必须一致。
//
// `pkg/capability` 不 import `pkg/runtime`（P-1 的包边界），所以落点模板的判定在两边各有一份
// ——这是刻意的重复，但**重复必须可验证**：这条测试把两边的判定结果钉在一起，任一边改了规则
// 而另一边没改，这里立刻红。
func TestApplyLayoutMatchesRuntime(t *testing.T) {
	layouts := []string{
		"<id>/SKILL.md", "<id>/nested/SKILL.md", "skills/<id>/SKILL.md",
		"<id>.md", "SKILL.md", "/abs/<id>/SKILL.md", "../<id>/SKILL.md", "<id>/../x.md",
	}
	for _, layout := range layouts {
		_, capErr := applyLayout(layout, "demo")
		rtErr := runtime.ValidateProjectionLayout(layout)
		if (capErr == nil) != (rtErr == nil) {
			t.Fatalf("落点 %q 两边判定不一致：capability=%v runtime=%v", layout, capErr, rtErr)
		}
		if capErr == nil {
			capPath, _ := applyLayout(layout, "demo")
			rtPath, rErr := runtime.ApplyProjectionLayout(layout, "demo")
			if rErr != nil || filepath.FromSlash(rtPath) != capPath {
				t.Fatalf("落点 %q 实例化不一致：capability=%q runtime=%q err=%v", layout, capPath, rtPath, rErr)
			}
		}
	}
	// id 注入防护也要一致。
	for _, bad := range []string{"a/b", `a\b`, ".", "..", ""} {
		_, capErr := applyLayout("<id>/SKILL.md", bad)
		_, rtErr := runtime.ApplyProjectionLayout("<id>/SKILL.md", bad)
		if (capErr == nil) != (rtErr == nil) {
			t.Fatalf("id %q 两边判定不一致：capability=%v runtime=%v", bad, capErr, rtErr)
		}
	}
}

// TestReconcileSkills 删除联动 + 重校：带 forge 标记且不在注册表的目录被清，
// 用户自有（无标记）目录绝不动；注册表 skill 缺失则写出。
func TestReconcileSkills(t *testing.T) {
	root := t.TempDir()
	spec := ocSpec(root)
	// 预置：forge 管理的 stale 目录（带标记）+ 用户自有 user-x（无标记）。
	if err := os.MkdirAll(filepath.Join(root, "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stale", forgeMarker), []byte("stale\nopencode\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "user-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "user-x", "SKILL.md"), []byte("user skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	current := []Skill{{ID: "keep", Name: "Keep", Description: "保留", Body: "# keep"}}

	w, r, err := ReconcileSkills(spec, current)
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
	if w, r, err := ReconcileSkills(spec, current); err != nil || w != 0 || r != 0 {
		t.Fatalf("幂等重校应 0/0，got w=%d r=%d err=%v", w, r, err)
	}
}

// TestReconcileCleansLegacyOrphanMarker 旧版单 root 产物（标记第二行为空 = 无投影方）在升级后
// 会被当作平台残留清掉：它是平台自己写的（有标记），却没有任何投影方认领，留着只会永久占位。
func TestReconcileCleansLegacyOrphanMarker(t *testing.T) {
	root := t.TempDir()
	orphan := filepath.Join(root, "legacy-only")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, forgeMarker), []byte("legacy-only\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "SKILL.md"), []byte("---\nname: x\n---\nold"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, r, err := ReconcileSkills(ocSpec(root), nil); err != nil || r != 1 {
		t.Fatalf("无主标记目录应被清理，got r=%d err=%v", r, err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("无主标记目录应已删除")
	}
}

// TestMaterializeWritesMarker 物化写归属标记（删除联动依据）。
func TestMaterializeWritesMarker(t *testing.T) {
	root := t.TempDir()
	sk := Skill{ID: "m1", Body: "# m1"}
	if _, err := MaterializeSkill(ocSpec(root), sk); err != nil {
		t.Fatal(err)
	}
	id, owners, ok := readMarker(filepath.Join(root, "m1", forgeMarker))
	if !ok || id != "m1" || !markerHasOwner(owners, "opencode") {
		t.Fatalf("归属标记不符: id=%q owners=%v ok=%v", id, owners, ok)
	}
}

// TestMaterializeSkillDshFrontmatter dsh 约定（2026-09-09 实证：dsh-skill-filesystem 丢弃
// 无 YAML frontmatter 的 SKILL.md）：format=SKILL.md+frontmatter 时自动合成 name/description。
func TestMaterializeSkillDshFrontmatter(t *testing.T) {
	root := t.TempDir()
	spec := dshSpec(root)
	sk := Skill{ID: "plan", Name: "规划", Description: "先规划再动手", Body: "# plan\n正文"}
	p, err := MaterializeSkill(spec, sk)
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
	sk2 := Skill{ID: "s2", Description: "描述", Body: "---\nname: \"custom\"\nwhenToUse: x\n---\nbody"}
	if _, err := MaterializeSkill(spec, sk2); err != nil {
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
	sk3 := Skill{ID: "s3", Name: "N3", Body: "x"}
	if _, err := MaterializeSkill(spec, sk3); err != nil {
		t.Fatal(err)
	}
	b3, _ := os.ReadFile(filepath.Join(root, "s3", "SKILL.md"))
	if !strings.Contains(string(b3), `description: "N3"`) {
		t.Fatalf("空 description 应退化为 name：\n%s", b3)
	}

	// 重校幂等：内容一致时不再写（独立 root，避免上面的 s2/s3 触发删除联动）。
	idemRoot := t.TempDir()
	idem := dshSpec(idemRoot)
	current := []Skill{{ID: "plan", Name: "规划", Description: "先规划再动手", Body: "# plan\n正文"}}
	if w, _, err := ReconcileSkills(idem, current); err != nil || w != 1 {
		t.Fatalf("首次重校应写 1，got w=%d err=%v", w, err)
	}
	if w, r, err := ReconcileSkills(idem, current); err != nil || w != 0 || r != 0 {
		t.Fatalf("dsh 格式幂等重校应 0/0，got w=%d r=%d err=%v", w, r, err)
	}
	// body 变更 → 重校写出。
	current[0].Body = "# plan v2"
	if w, _, err := ReconcileSkills(idem, current); err != nil || w != 1 {
		t.Fatalf("body 变更应重写，got w=%d err=%v", w, err)
	}
	if b5, _ := os.ReadFile(filepath.Join(idemRoot, "plan", "SKILL.md")); !strings.HasSuffix(string(b5), "# plan v2") {
		t.Fatalf("重校内容不符：\n%s", b5)
	}
}

// TestReconcileRespectsSkillRuntimes 投放判据是 `Skill.Runtimes`（空 = 全部）∩ 本投影方：
// 声明了别的底座就不该出现在这个 root 里；**空 = 全部**是升级安全网（存量定义没有该字段，
// 若空表示"谁都不用"，升级那一刻所有技能会集体被撤回）。
func TestReconcileRespectsSkillRuntimes(t *testing.T) {
	ocRoot, dshRoot := t.TempDir(), t.TempDir()
	both := Skill{ID: "both", Name: "双", Description: "d", Body: "# both"}
	ocOnly := Skill{ID: "oc-only", Name: "oc", Description: "d", Body: "# oc", Runtimes: []string{"opencode"}}
	dshOnly := Skill{ID: "dsh-only", Name: "dsh", Description: "d", Body: "# dsh", Runtimes: []string{"deepseekharness"}}
	skills := []Skill{both, ocOnly, dshOnly}

	if w, r, err := ReconcileSkills(ocSpec(ocRoot), skills); err != nil || w != 2 || r != 0 {
		t.Fatalf("oc 首轮期望 2/0，got w=%d r=%d err=%v", w, r, err)
	}
	if w, r, err := ReconcileSkills(dshSpec(dshRoot), skills); err != nil || w != 2 || r != 0 {
		t.Fatalf("dsh 首轮期望 2/0，got w=%d r=%d err=%v", w, r, err)
	}
	assertDirs := func(root string, want ...string) {
		t.Helper()
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(entries))
		for _, e := range entries {
			got = append(got, e.Name())
		}
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s 目录不符: got=%v want=%v", root, got, want)
		}
	}
	assertDirs(ocRoot, "both", "oc-only")
	assertDirs(dshRoot, "both", "dsh-only")

	// 幂等：再跑 0/0。
	for _, spec := range []MaterializeSpec{ocSpec(ocRoot), dshSpec(dshRoot)} {
		if w, r, err := ReconcileSkills(spec, skills); err != nil || w != 0 || r != 0 {
			t.Fatalf("%s 幂等期望 0/0，got w=%d r=%d err=%v", spec.Owner, w, r, err)
		}
	}

	// 移除 dsh-only 的声明 → dsh root 删除联动，oc root 不受影响。
	reduced := []Skill{both, ocOnly}
	if w, r, err := ReconcileSkills(dshSpec(dshRoot), reduced); err != nil || w != 0 || r != 1 {
		t.Fatalf("dsh 删除联动期望 0/1，got w=%d r=%d err=%v", w, r, err)
	}
	assertDirs(dshRoot, "both")
	assertDirs(ocRoot, "both", "oc-only")

	// 共享 root：两个投影方指向同一目录时，各自重校不删对方目录。
	shared := t.TempDir()
	if _, _, err := ReconcileSkills(ocSpec(shared), skills); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReconcileSkills(dshSpec(shared), skills); err != nil {
		t.Fatal(err)
	}
	assertDirs(shared, "both", "dsh-only", "oc-only")
	if w, r, err := ReconcileSkills(ocSpec(shared), reduced); err != nil || w != 0 || r != 0 {
		t.Fatalf("共享 root 下 oc 重校不应删 dsh 目录: w=%d r=%d err=%v", w, r, err)
	}
	if _, err := os.Stat(filepath.Join(shared, "dsh-only")); err != nil {
		t.Fatal("共享 root 下 dsh-only 不应被 oc 重校删除")
	}
}

// TestReconcileSharedRootResidualCleanup C6：共享 root 下归属集合登记/摘除——
// 两个投影方先后投影同一目录后，各自退出投影只摘除自己的登记，集合空了才删除目录
// （修复 C4 遗留的「先投影方退出后目录永久残留」）。
func TestReconcileSharedRootResidualCleanup(t *testing.T) {
	shared := t.TempDir()
	both := Skill{ID: "both", Name: "双", Description: "d", Body: "# both"}
	onlyOC := Skill{ID: "oc", Name: "oc", Description: "d", Body: "# oc"}
	skills := []Skill{both, onlyOC}

	// 两个投影方各自投影到共享 root：标记应累计两个归属方。
	if _, _, err := ReconcileSkills(ocSpec(shared), skills); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReconcileSkills(dshSpec(shared), skills); err != nil {
		t.Fatal(err)
	}
	_, owners, _ := readMarker(filepath.Join(shared, "both", forgeMarker))
	if len(owners) != 2 || !markerHasOwner(owners, "opencode") || !markerHasOwner(owners, "deepseekharness") {
		t.Fatalf("共享 root 归属集合应含两个投影方，got %v", owners)
	}
	// 幂等：两边再跑都 0/0（登记已完成，不再互相改写标记）。
	for _, spec := range []MaterializeSpec{ocSpec(shared), dshSpec(shared)} {
		if w, r, err := ReconcileSkills(spec, skills); err != nil || w != 0 || r != 0 {
			t.Fatalf("%s 幂等期望 0/0，got w=%d r=%d err=%v", spec.Owner, w, r, err)
		}
	}

	// opencode 退出 both 的投影：只摘除自己的登记，目录保留（dsh 仍投影）。
	ocOnlySet := []Skill{onlyOC}
	if w, r, err := ReconcileSkills(ocSpec(shared), ocOnlySet); err != nil || r != 0 {
		t.Fatalf("oc 摘除登记不应删除目录: w=%d r=%d err=%v", w, r, err)
	}
	_, owners, _ = readMarker(filepath.Join(shared, "both", forgeMarker))
	if len(owners) != 1 || owners[0] != "deepseekharness" {
		t.Fatalf("摘除后应只剩 dsh，got %v", owners)
	}
	if _, err := os.Stat(filepath.Join(shared, "both")); err != nil {
		t.Fatal("他方仍投影时目录不应被删")
	}

	// dsh 也退出：集合空 → 目录删除（无残留）。
	if w, r, err := ReconcileSkills(dshSpec(shared), nil); err != nil || r != 1 {
		t.Fatalf("dsh 退出后应删除残留目录: w=%d r=%d err=%v", w, r, err)
	}
	if _, err := os.Stat(filepath.Join(shared, "both")); !os.IsNotExist(err) {
		t.Fatal("两方都退出后目录应被删除")
	}
}

// TestReadMarkerLegacy 兼容旧标记格式（第二行单个投影方）与空归属（旧版单 root 产物）。
func TestReadMarkerLegacy(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, forgeMarker), []byte("legacy\nopencode\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, owners, ok := readMarker(filepath.Join(legacy, forgeMarker))
	if !ok || id != "legacy" || len(owners) != 1 || owners[0] != "opencode" {
		t.Fatalf("旧格式解析不符: id=%q owners=%v ok=%v", id, owners, ok)
	}
	global := filepath.Join(dir, "global")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, forgeMarker), []byte("global\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if id, owners, ok := readMarker(filepath.Join(global, forgeMarker)); !ok || id != "global" || len(owners) != 0 {
		t.Fatalf("空归属解析不符: id=%q owners=%v ok=%v", id, owners, ok)
	}
}
