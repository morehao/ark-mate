package dsh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/pkg/resource"
)

// fakeDelivery 记录 adapter 请求平台改的启动面（真实实现是注册表的 runtimeDelivery）。
type fakeDelivery struct {
	env  map[string]string
	args map[string]string
}

func newFakeDelivery() *fakeDelivery {
	return &fakeDelivery{env: map[string]string{}, args: map[string]string{}}
}

func (f *fakeDelivery) SetEnv(key, value string) error  { f.env[key] = value; return nil }
func (f *fakeDelivery) SetArg(flag, value string) error { f.args[flag] = value; return nil }

func testSet(docPath string) resource.Set {
	return resource.Set{
		Runtime: ID,
		Providers: []resource.Provider{
			{ID: "gw", Name: "网关", Protocol: resource.ProtocolOpenAI, BaseURL: "https://gw.example.com/v1",
				APIKey: "sk-fixture", Ref: resource.RefFor(DefaultRefPattern, "gw")},
		},
		Models: []resource.Model{
			{ID: "m1", ProviderID: "gw", ModelID: "gpt-4o-mini", Name: "小模型"},
			{ID: "m2", ProviderID: "gw", ModelID: "gpt-4o", Name: "大模型"},
		},
		DefaultModel:      "m2",
		CredentialDocPath: docPath,
	}
}

// fakeDsh 造一个假 dsh：按 FAKE_DSH_MODE 打印不同的 `x-cordis` 结论。
//
// **绝不调用真实 dsh**（隔离纪律：不许碰用户自己的底座）。假命令同时把 `--patch` 收到的候选
// 复制到 FAKE_DSH_SEEN（若设了），用来断言"校验跑的是候选、不是成品"。
func fakeDsh(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv("FAKE_DSH_MODE", mode)
	dir := t.TempDir()
	p := filepath.Join(dir, "dsh")
	script := `#!/bin/sh
if [ -n "$FAKE_DSH_SEEN" ]; then cp "$4" "$FAKE_DSH_SEEN"; fi
case "$FAKE_DSH_MODE" in
  ok) printf '%s' '{"x-cordis":{"complete":true,"diagnostics":[]}}'; exit 0 ;;
  incomplete) printf '%s' '{"x-cordis":{"complete":false,"diagnostics":[]}}'; exit 0 ;;
  warn) printf '%s' '{"x-cordis":{"complete":true,"diagnostics":[{"level":"warn","message":"patch: entry \"acpx\" not found"}]}}'; exit 0 ;;
  notjson) printf '%s' 'boom'; exit 0 ;;
  crash) exit 3 ;;
esac
exit 0
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSyncModelsWritesOverlayAndArg(t *testing.T) {
	dir := t.TempDir()
	patch := filepath.Join(dir, "mate-models.patch.yml")
	doc := filepath.Join(dir, ".credentials.yaml")
	d := newFakeDelivery()
	s := NewSyncer(Options{PatchPath: patch, CredentialDocPath: doc, RefPattern: DefaultRefPattern}, d)

	out, err := s.SyncModels(context.Background(), testSet(doc))
	if err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	if out.Status != resource.StatusApplied {
		t.Fatalf("首发应报 applied，实际 %q", out.Status)
	}
	if out.Effect != resource.EffectNextProcess {
		t.Fatalf("清单的生效时刻应是 next_process，实际 %q", out.Effect)
	}
	// 未提供 dsh 可执行文件 = 不跑底座级校验门，但**必须如实说**。
	if !anyContains(out.Problems, "未跑底座级校验") {
		t.Fatalf("DshBin 为空时必须如实标注未跑底座级校验，实际 problems=%v", out.Problems)
	}
	// `--patch` 必须是**绝对**路径。
	if got := d.args[PatchFlag]; got != patch || !filepath.IsAbs(got) {
		t.Fatalf("应装载 %s=%q（绝对路径），实际 %q", PatchFlag, patch, got)
	}
	b, err := os.ReadFile(patch)
	if err != nil {
		t.Fatalf("overlay 未落盘：%v", err)
	}
	if err := selfCheck(b); err != nil {
		t.Fatalf("落盘的 overlay 过不了自校验：%v", err)
	}
	if fi, _ := os.Stat(patch); fi.Mode().Perm() != 0o600 {
		t.Fatalf("overlay 权限应为 0600，实际 %04o", fi.Mode().Perm())
	}
	// 凭据面：原生文档通道 + 热生效 + 已实测。
	if out.Credential.Channel != resource.ChannelNativeDoc || out.Credential.Effect != resource.EffectNextTurn ||
		!out.Credential.Tested || out.Credential.Status != resource.StatusApplied {
		t.Fatalf("凭据结论不符：%+v", out.Credential)
	}
	if !strings.Contains(readFile(t, doc), "MATE_GW_") {
		t.Fatalf("凭据文档里应写进逐供应商引用名，实际：\n%s", readFile(t, doc))
	}
	for _, p := range testSet(doc).Providers {
		if strings.Contains(readFile(t, doc), p.APIKey) == false {
			t.Fatalf("凭据文档里应写进该供应商的密钥（只落文档这一处）")
		}
	}
}

func TestSyncModelsIdempotent(t *testing.T) {
	dir := t.TempDir()
	patch := filepath.Join(dir, "mate-models.patch.yml")
	doc := filepath.Join(dir, ".credentials.yaml")
	s := NewSyncer(Options{PatchPath: patch, CredentialDocPath: doc, RefPattern: DefaultRefPattern}, newFakeDelivery())

	if _, err := s.SyncModels(context.Background(), testSet(doc)); err != nil {
		t.Fatal(err)
	}
	fi1, _ := os.Stat(patch)
	out, err := s.SyncModels(context.Background(), testSet(doc))
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != resource.StatusUnchanged {
		t.Fatalf("同一声明重复同步应报 unchanged，实际 %q（problems=%v）", out.Status, out.Problems)
	}
	if out.Credential.Status != resource.StatusUnchanged {
		t.Fatalf("同一份密钥重复写应报 unchanged，实际 %q", out.Credential.Status)
	}
	fi2, _ := os.Stat(patch)
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("重复同步重写了 overlay（mtime 变了）")
	}
}

func TestSyncModelsEmptySetRevokes(t *testing.T) {
	dir := t.TempDir()
	patch := filepath.Join(dir, "mate-models.patch.yml")
	doc := filepath.Join(dir, ".credentials.yaml")
	d := newFakeDelivery()
	s := NewSyncer(Options{PatchPath: patch, CredentialDocPath: doc, RefPattern: DefaultRefPattern}, d)

	if _, err := s.SyncModels(context.Background(), testSet(doc)); err != nil {
		t.Fatal(err)
	}
	// 空集：Set 里没有供应商（编排层仍给出已解析的凭据文档落点，否则平台撤不掉自己写过的条目）。
	out, err := s.SyncModels(context.Background(), resource.Set{Runtime: ID, CredentialDocPath: doc})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != resource.StatusApplied {
		t.Fatalf("撤回已有交付应报 applied，实际 %q", out.Status)
	}
	if _, err := os.Stat(patch); !os.IsNotExist(err) {
		t.Fatalf("空集必须删除 overlay（空 patch 会让 dsh 拒启），实际 err=%v", err)
	}
	if v, ok := d.args[PatchFlag]; !ok || v != "" {
		t.Fatalf("空集必须摘除 %s（值为空 = 摘掉），实际 %q", PatchFlag, v)
	}
	if txt := readFile(t, doc); strings.Contains(txt, "MATE_GW_") {
		t.Fatalf("空集必须移除平台自己写过的 refs 条目，实际：\n%s", txt)
	}
}

func TestSyncModelsUnsupportedWithoutPatchPath(t *testing.T) {
	s := NewSyncer(Options{}, newFakeDelivery())
	out, err := s.SyncModels(context.Background(), resource.Set{Runtime: ID, Models: []resource.Model{{ID: "m"}}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != resource.StatusUnsupported || len(out.Problems) == 0 {
		t.Fatalf("未声明落点应报 unsupported 且带可执行出路，实际 %+v", out)
	}
}

func TestValidationGateAcceptsCompleteDump(t *testing.T) {
	dir := t.TempDir()
	patch := filepath.Join(dir, "mate-models.patch.yml")
	seen := filepath.Join(dir, "seen.patch.yml")
	t.Setenv("FAKE_DSH_SEEN", seen)
	bin := fakeDsh(t, "ok")
	s := NewSyncer(Options{PatchPath: patch, DshBin: bin, StagingHome: t.TempDir()}, newFakeDelivery())

	out, err := s.SyncModels(context.Background(), testSet(""))
	if err != nil {
		t.Fatalf("三条判据同时成立时应通过：%v", err)
	}
	if anyContains(out.Problems, "未跑底座级校验") {
		t.Fatalf("跑了校验门就不该再标注未跑，实际 %v", out.Problems)
	}
	// 校验必须跑在**候选**上：假 dsh 复制出来的那份应与落盘内容逐字节相同。
	if got, want := readFile(t, seen), readFile(t, patch); got != want {
		t.Fatalf("校验门跑的不是候选：\nseen=%q\nlanded=%q", got, want)
	}
}

func TestValidationGateRejectsWithoutWriting(t *testing.T) {
	// 三种都必须被拒：complete=false（exit 0）、诊断非空（exit 0）、输出不是 JSON。只看退出码会漏掉前三者中的前两种。
	for _, mode := range []string{"incomplete", "warn", "notjson", "crash"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			patch := filepath.Join(dir, "mate-models.patch.yml")
			bin := fakeDsh(t, mode)
			s := NewSyncer(Options{PatchPath: patch, DshBin: bin, StagingHome: t.TempDir()}, newFakeDelivery())

			if _, err := s.SyncModels(context.Background(), testSet("")); err == nil {
				t.Fatalf("校验门应当拒绝 mode=%s 的候选", mode)
			}
			if _, err := os.Stat(patch); !os.IsNotExist(err) {
				t.Fatalf("校验失败时**一个字节都不许写**（fail-loud，不半写），实际 err=%v", err)
			}
		})
	}
}

func TestValidationGateUsesStagingHomeNotRealOne(t *testing.T) {
	// staging 必须与活跃 home 分开：假 dsh 把子进程的 DSH_HOME/HOME 记下来，断言它们在 staging 下。
	dir := t.TempDir()
	staging := t.TempDir()
	seen := filepath.Join(dir, "env.txt")
	t.Setenv("FAKE_DSH_ENV", seen)
	p := filepath.Join(t.TempDir(), "dsh")
	script := "#!/bin/sh\nprintf '%s\\n%s\\n' \"$DSH_HOME\" \"$HOME\" > \"$FAKE_DSH_ENV\"\nprintf '%s' '{\"x-cordis\":{\"complete\":true,\"diagnostics\":[]}}'\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewSyncer(Options{PatchPath: filepath.Join(dir, "p.yml"), DshBin: p, StagingHome: staging}, newFakeDelivery())
	if _, err := s.SyncModels(context.Background(), testSet("")); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, seen)
	if !strings.Contains(got, staging) {
		t.Fatalf("校验门的 DSH_HOME/HOME 必须在 staging 下（绝不碰活跃 home），实际：\n%s", got)
	}
	// staging 用后即清。
	entries, _ := os.ReadDir(staging)
	if len(entries) != 0 {
		t.Fatalf("staging 目录应在校验后清空，实际残留 %d 项", len(entries))
	}
}

func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s：%v", path, err)
	}
	return string(b)
}
