package dsh

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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
			{ID: "m1", ProviderID: "gw", Model: "gpt-4o-mini", Name: "小模型"},
			{ID: "m2", ProviderID: "gw", Model: "gpt-4o", Name: "大模型"},
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
	if out.Credential.Channel != resource.ChannelNativeDoc || out.Credential.Effect != resource.EffectNextRun ||
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

// TestBuildPatchDeliversCapacityOnlyWhenDeclared v25：容量是**平台声明**——非空才写进底座声明，
// 空就一个字段都不写（让底座自己的兜底生效，而不是由平台塞一个假值）。
//
// 三条判据，各自对应一种静默退化：
//
//	① 声明过的模型下**恰恰**出现 `contextWindow:` / `maxTokens:` 各一行，且值就是平台那个数；
//	② 没声明的模型下**一行都没有**（写成 `contextWindow: 0` 会被底座的 `min(1)` 校验门拒掉，
//	   而"写了 0"与"没写"在界面上长得一模一样）；
//	③ 字段名用**底座的词**且只出现在这一处（内核那两列叫 `max_input_tokens`/`max_output_tokens`）
//	   ——反向断言：产物里不得出现平台的列名。
func TestBuildPatchDeliversCapacityOnlyWhenDeclared(t *testing.T) {
	in, out := int64(131072), int64(8192)
	b, err := buildPatch(resource.Set{
		Runtime: ID,
		Providers: []resource.Provider{
			{ID: "gw", Name: "网关", Protocol: resource.ProtocolOpenAI, BaseURL: "https://gw.example.com/v1"},
		},
		Models: []resource.Model{
			{ID: "m1", ProviderID: "gw", Model: "with-cap", Name: "有容量",
				InputModalities: []string{resource.ModalityText},
				MaxInputTokens:  &in, MaxOutputTokens: &out},
			{ID: "m2", ProviderID: "gw", Model: "no-cap", Name: "没容量",
				InputModalities: []string{resource.ModalityText}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)

	// ① 有容量的那一条：值就是平台那个数，且写在 `input` **之前**（与 QwenWork 口径一致：先能力后上限）。
	wantBlock := "          - id: \"with-cap\"\n" +
		"            name: \"有容量\"\n" +
		"            contextWindow: 131072\n" +
		"            maxTokens: 8192\n" +
		"            input: [text]\n"
	if !strings.Contains(got, wantBlock) {
		t.Fatalf("声明过的容量没有按底座字段名交付：\nwant:\n%s\ngot:\n%s", wantBlock, got)
	}

	// ② 没容量的那一条：一个字段都不写。
	noCapBlock := "          - id: \"no-cap\"\n" +
		"            name: \"没容量\"\n" +
		"            input: [text]\n"
	if !strings.Contains(got, noCapBlock) {
		t.Fatalf("未声明的容量必须**不下发该字段**：\nwant:\n%s\ngot:\n%s", noCapBlock, got)
	}
	for _, banned := range []string{"contextWindow: 0", "maxTokens: 0"} {
		if strings.Contains(got, banned) {
			t.Fatalf("未声明的容量写成了 %q——底座的 `min(1)` 会拒掉它，而「没声明」与「声明成 0」看起来一样", banned)
		}
	}
	// 每个字段名**恰好**出现一次（只有声明过的那条模型）。
	for _, field := range []string{"contextWindow: ", "maxTokens: "} {
		if c := strings.Count(got, field); c != 1 {
			t.Fatalf("`%s` 应恰好出现 1 次（只有声明过的那条），实际 %d 次：\n%s", field, c, got)
		}
	}

	// ③ 平台的列名不得出现在产物里（底座词表与内核词表不许混）。
	for _, banned := range []string{"max_input_tokens", "max_output_tokens"} {
		if strings.Contains(got, banned) {
			t.Errorf("产物里出现了内核的列名 %q——交付必须用底座的字段名", banned)
		}
	}
}

// TestDshRouteKeyAgreesBetweenPatchAndModelValue 守卫③（**动态那半**）：配置里声明的路由键与
// ACP 模型值的第一段必须**逐字同源**。
//
// 为什么必须有这一条（静态那半不够）：前者在 `config.go`、后者在 `model_value.go`——**只改一处**
// 是完全编译得过的，而后果是 `set_config_option` 找不到 provider，**所有选了模型的会话立刻起不来**
// （计划 R1 就是这个现场）。静态判据只能钉"调用点有几个"，钉不住"两边编出来的串真的相等"，而
// 后者才是运行时真正消费的东西。
//
// 反证（实跑）：把 `model_value.go` 的 `routeKey(providerID)` 改回 `providerID` → 本用例红。
func TestDshRouteKeyAgreesBetweenPatchAndModelValue(t *testing.T) {
	set := goldenSet()
	b, err := buildPatch(set)
	if err != nil {
		t.Fatal(err)
	}
	keys := patchProviderKeys(t, string(b))
	if len(keys) == 0 {
		t.Fatal("patch 里一个 `providers.<k>` 键都没解析到（fail-closed：判据自身失效）")
	}
	if len(keys) != len(set.Providers) {
		t.Fatalf("patch 里的路由键数（%d）与声明的供应商数（%d）不等", len(keys), len(set.Providers))
	}

	mv := ModelValue{}
	for _, m := range set.Models {
		v, merr := mv.Value(m.ProviderID, m.Model)
		if merr != nil {
			t.Fatalf("编模型值失败：%v", merr)
		}
		var seg []string
		if uerr := json.Unmarshal([]byte(v), &seg); uerr != nil || len(seg) != 2 {
			t.Fatalf("dsh 的模型值形态应是长度为 2 的 JSON 数组，实际 %q（err=%v）", v, uerr)
		}
		if !keys[seg[0]] {
			t.Errorf("模型 %s/%s 的 ACP 值第一段是 %q，而 patch 里声明的路由键是 %v——"+
				"两边分叉 ⇒ `set_config_option` 找不到 provider（所有选了模型的会话起不来）",
				m.ProviderID, m.Model, seg[0], sortedKeyList(keys))
		}
	}

	// 默认模型那一行（`- id: acp` 的 `provider:`）也必须是同一个键集里的一个——它是"新进程起来时
	// 当前选中"的那一行，与上面两条同等重要。
	for _, ln := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(ln, "    provider: ") {
			continue
		}
		v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(ln, "    provider: ")), `"`)
		if !keys[v] {
			t.Errorf("默认模型的 provider 值 %q 不在 patch 声明的路由键里：%v", v, sortedKeyList(keys))
		}
	}
}

// patchProviderKeys 抽出 patch 里 `providers.<k>:` 的键集（缩进 6 + 带引号 = 路由键）。
func patchProviderKeys(t *testing.T, patch string) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	inProviders := false
	for _, ln := range strings.Split(patch, "\n") {
		if strings.HasPrefix(ln, "    providers:") {
			inProviders = true
			continue
		}
		if !inProviders {
			continue
		}
		if strings.HasPrefix(ln, "      \"") && strings.HasSuffix(ln, "\":") {
			keys[strings.TrimSuffix(strings.TrimPrefix(ln, "      \""), "\":")] = true
			continue
		}
		if strings.TrimSpace(ln) != "" && !strings.HasPrefix(ln, "      ") {
			inProviders = false
		}
	}
	return keys
}

// sortedKeyList 把键集排序后返回（报错信息要确定）。
func sortedKeyList(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
