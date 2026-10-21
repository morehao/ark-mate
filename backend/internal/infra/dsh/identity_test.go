package dsh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkgidentity "github.com/morehao/ark-mate/internal/platform/identity"
	"github.com/morehao/ark-mate/internal/platform/resource"
)

// ——— 身份 overlay 的生成 / 校验 / 幂等 / 撤回（P1 的确定性判据） ———
//
// 这个文件守的是"身份交付不会把底座弄坏"：写错一个字节的 patch 会让 dsh **拒启**，而启动期
// 刻意不跑 `--dump-config-schema`（用户已拍板）。所以正确性必须落在**静态判据**上：
//
//	① 产物形态逐字节钉住（golden）；
//	② 非法输入一个字节都不写（校验在落盘之前）；
//	③ 同一份身份重复交付收敛（幂等），撤回是"删文件 + 摘参数"而不是写空文件。

// identityFixture 一份固定身份（不含任何真实底座名：这个包的测试也不该依赖产品文案）。
func identityFixture() pkgidentity.Spec {
	return pkgidentity.Spec{
		Version:      "v1",
		Name:         "ark-mate",
		DisplayName:  "ark-mate",
		Tagline:      "个人 Agent 助手",
		SystemPrompt: "你是 ark-mate。\n\n第二段。",
		UserContext:  "<product_identity>身份</product_identity>",
	}
}

// identityGoldenPath golden 文件：生成的 patch 必须与它逐字节相同。
//
// 为什么用文件而不是内联常量：这份产物要在**真实底座**上跑一次性 probe（
// `.dsh/docs/notes/probes/dsh-identity-probe.mjs` 读的就是它）。文件与生成器
// 之间由本测试钉住（`-update` 重生成，评审时人工过一遍 diff），于是 probe 验的正是
// 生成器今天会写出的字节，而不是一份手抄副本。
const identityGoldenPath = "testdata/mate-identity.patch.yml"

func TestBuildIdentityPatchGolden(t *testing.T) {
	data, err := buildIdentityPatch(identityFixture())
	if err != nil {
		t.Fatalf("生成身份 patch: %v", err)
	}
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(identityGoldenPath), 0o755); err != nil {
			t.Fatalf("建 testdata 目录: %v", err)
		}
		if err := os.WriteFile(identityGoldenPath, data, 0o644); err != nil {
			t.Fatalf("重写 golden: %v", err)
		}
		t.Logf("已重写 %s（评审时人工过 diff）", identityGoldenPath)
	}
	want, err := os.ReadFile(identityGoldenPath)
	if err != nil {
		t.Fatalf("读 golden 失败（%s）：%v（首次生成用 `go test ./pkg/runtime/dsh -run TestBuildIdentityPatchGolden -update`）",
			identityGoldenPath, err)
	}
	if string(data) != string(want) {
		t.Fatalf("身份 patch 与 golden 不一致：\n--- got ---\n%s\n--- want ---\n%s", data, want)
	}
}

func TestBuildIdentityPatchRejectsDangerousCopy(t *testing.T) {
	cases := map[string]string{
		"模板花括号":  "你是 {{name}}。",
		"模板标签":   "你是 {% if x %}ark-mate{% endif %}。",
		"NUL":    "你是 ark-mate\x00。",
		"YAML标签": "你是 !!js ark-mate。",
		"空正文":    "   \n\t",
		"超长":     strings.Repeat("很长的一句身份。", MaxIdentityPatchBytes),
	}
	for name, copy := range cases {
		spec := identityFixture()
		spec.SystemPrompt = copy
		if _, err := buildIdentityPatch(spec); err == nil {
			t.Fatalf("%s：非法正文应被拒绝", name)
		}
	}
}

// TestDeliverWritesAndIsIdempotent 交付写文件 + 装参数；同一份重复交付必须 `unchanged`
// （不重写字节、不重装参数）。
func TestDeliverWritesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	delivery := newFakeDelivery()
	patches := NewPatchArgs(delivery)
	a := NewIdentity(IdentityOptions{RootAbs: root}, patches)

	out, err := a.DeliverIdentity(context.Background(), identityFixture())
	if err != nil {
		t.Fatalf("首次交付: %v", err)
	}
	if out.Status != resource.StatusApplied {
		t.Fatalf("首次交付应为 applied，得到 %q（%v）", out.Status, out.Problems)
	}
	path := filepath.Join(root, IdentityPatchFilename)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("身份 patch 未落盘: %v", err)
	}
	golden, err := os.ReadFile(identityGoldenPath)
	if err != nil {
		t.Fatalf("读 golden 失败（%s）：%v", identityGoldenPath, err)
	}
	if string(first) != string(golden) {
		t.Fatalf("落盘内容与 golden 不一致:\n%s", first)
	}
	if got := delivery.multi[PatchFlag]; len(got) != 1 || got[0] != path {
		t.Fatalf("`--patch` 应只装身份这一层，得到 %v", got)
	}

	out2, err := a.DeliverIdentity(context.Background(), identityFixture())
	if err != nil {
		t.Fatalf("重复交付: %v", err)
	}
	if out2.Status != resource.StatusUnchanged {
		t.Fatalf("同一份身份重复交付应为 unchanged，得到 %q", out2.Status)
	}
	second, err := os.ReadFile(path)
	if err != nil || string(second) != string(first) {
		t.Fatalf("幂等重放不应重写字节: %v", err)
	}
}

// TestRevokeDeletesFileAndArg 撤回 = 删文件 + 摘参数。
//
// 只"不写"是不够的：上一份 patch 还在、`--patch` 还装着，底座照旧读旧系统提示——用户把模式
// 切到 off 之后模型仍自称 ark-mate，而界面上没有任何一行解释这件事。
func TestRevokeDeletesFileAndArg(t *testing.T) {
	root := t.TempDir()
	delivery := newFakeDelivery()
	patches := NewPatchArgs(delivery)
	a := NewIdentity(IdentityOptions{RootAbs: root}, patches)

	if _, err := a.DeliverIdentity(context.Background(), identityFixture()); err != nil {
		t.Fatalf("先交付: %v", err)
	}
	path := filepath.Join(root, IdentityPatchFilename)

	// `user` 模式：SystemPrompt 被清空（首轮块仍在）。
	spec := identityFixture()
	spec.SystemPrompt = ""
	out, err := a.DeliverIdentity(context.Background(), spec)
	if err != nil {
		t.Fatalf("撤回: %v", err)
	}
	if out.Status != resource.StatusApplied {
		t.Fatalf("撤回应为 applied（真的删了什么），得到 %q", out.Status)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("撤回后身份 patch 必须不存在（空/只有注释的 patch 会让底座拒启）")
	}
	if got := delivery.multi[PatchFlag]; len(got) != 0 {
		t.Fatalf("撤回后 `--patch` 应为空，得到 %v", got)
	}

	// 再撤回一次：幂等（不再报告 changed）。
	out3, err := a.DeliverIdentity(context.Background(), spec)
	if err != nil {
		t.Fatalf("重复撤回: %v", err)
	}
	if out3.Status != resource.StatusUnchanged {
		t.Fatalf("重复撤回应为 unchanged，得到 %q", out3.Status)
	}
}

// TestIdentitySharesPatchListWithModels 模型清单与身份必须**共存**，且模型在前。
//
// 这是本方案最容易静默失效的一条：`--patch` 是可重复参数，两边各用 `SetArg` 会互相覆盖，
// 而两处都报 applied——用户看到"清单已同步、身份已交付"，底座只拿到后装的那一层。
func TestIdentitySharesPatchListWithModels(t *testing.T) {
	delivery := newFakeDelivery()
	patches := NewPatchArgs(delivery)
	modelPath := filepath.Join(t.TempDir(), "mate-models.patch.yml")
	idPath := filepath.Join(t.TempDir(), "mate-identity.patch.yml")

	// 身份先装（模拟装配顺序：身份步骤在模型同步之前）。
	if _, err := patches.SetIdentity(idPath); err != nil {
		t.Fatalf("装身份: %v", err)
	}
	if _, err := patches.SetModel(modelPath); err != nil {
		t.Fatalf("装清单: %v", err)
	}
	got := delivery.multi[PatchFlag]
	if len(got) != 2 || got[0] != modelPath || got[1] != idPath {
		t.Fatalf("`--patch` 取值应为 [模型, 身份]，得到 %v", got)
	}

	// 只撤清单：身份那一层必须留着。
	if _, err := patches.SetModel(""); err != nil {
		t.Fatalf("撤清单: %v", err)
	}
	got = delivery.multi[PatchFlag]
	if len(got) != 1 || got[0] != idPath {
		t.Fatalf("撤清单后应只剩身份，得到 %v", got)
	}

	// 只撤身份：清单那一层必须留着（反过来同样）。
	if _, err := patches.SetModel(modelPath); err != nil {
		t.Fatalf("再装清单: %v", err)
	}
	if _, err := patches.SetIdentity(""); err != nil {
		t.Fatalf("撤身份: %v", err)
	}
	got = delivery.multi[PatchFlag]
	if len(got) != 1 || got[0] != modelPath {
		t.Fatalf("撤身份后应只剩清单，得到 %v", got)
	}
}

// TestIdentityDeclaresTestedVersion 声明必须有版本、且与"已实测"自洽。
//
// 版本与载荷 pin 的**逐字比对**在 archtest（它要读仓库里的 pin 文件）；这里钉的是"声明本身
// 不能是空白"——留空的 TestedOnVersion 会让那条守卫失去对象。
func TestIdentityDeclaresTestedVersion(t *testing.T) {
	if !Identity.Tested {
		t.Skip("身份面当前声明为未实测：跳过版本判据")
	}
	if strings.TrimSpace(Identity.TestedOnVersion) == "" {
		t.Fatal("Identity.Tested=true 时 TestedOnVersion 不能为空")
	}
	// Note 是"写入权是用实测换的"那条纪律的落点：没有结论的声明等于没测过。
	if strings.TrimSpace(Identity.Note) == "" {
		t.Fatal("Identity.Note 不能为空：它记录实测结论与已知边界")
	}
	if Identity.PatchFilename != IdentityPatchFilename {
		t.Fatalf("声明里的文件名 %q 与实现常量 %q 不一致", Identity.PatchFilename, IdentityPatchFilename)
	}
}
