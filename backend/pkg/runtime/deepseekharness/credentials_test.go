package deepseekharness

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/pkg/resource"
)

// docWithUserContent 一份**用户也在用**的凭据文档：顶层注释、平台条目的注解、用户自己的 refs、
// 以及 `records:` 分节（官方口径：`records` 按 `<owner>/<id>` 存放各插件自己的凭据）。
const docWithUserContent = `# 用户自己的注释（顶层）
version: 1

refs:
  # 平台写过的条目（旧值）
  MATE_OLD_AAAA1111_API_KEY: sk-old
  # 用户自己的密钥
  USER_KEY: sk-user

records:
  llm-pi-ai/openai-codex:
    kind: grant
    payload:
      type: oauth
`

// TestPatchRefsSectionPreservesUserContent 契约③的粒度例外：只动平台命名空间里的条目，
// 其余分节、注释、用户自己的 refs **逐字**保留。
func TestPatchRefsSectionPreservesUserContent(t *testing.T) {
	got := patchRefsSection(docWithUserContent,
		map[string]string{"MATE_NEW_BBBB2222_API_KEY": "sk-new"}, managedRefMatcher(DefaultRefPattern))

	want := `# 用户自己的注释（顶层）
version: 1

refs:
  # 用户自己的密钥
  USER_KEY: sk-user
  MATE_NEW_BBBB2222_API_KEY: sk-new

records:
  llm-pi-ai/openai-codex:
    kind: grant
    payload:
      type: oauth
`
	if got != want {
		t.Fatalf("读-改-写的结果不符：\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	// 平台条目的注解随条目一起删（官方口径：直接位于某条目上方的注释属于该条目）；
	// 用户条目上方的注释必须留下。
	if strings.Contains(got, "平台写过的条目") {
		t.Error("平台条目的注解应随该条目一起移除")
	}
	if !strings.Contains(got, "用户自己的密钥") {
		t.Error("用户条目上方的注释不得被移除")
	}
}

// TestPatchRefsSectionEmptySetRemovesOnlyPlatformRefs 空集只移除平台自己写过的条目。
func TestPatchRefsSectionEmptySetRemovesOnlyPlatformRefs(t *testing.T) {
	got := patchRefsSection(docWithUserContent, nil, managedRefMatcher(DefaultRefPattern))
	if strings.Contains(got, "MATE_") {
		t.Fatalf("空集应移除平台命名空间里的全部条目：\n%s", got)
	}
	for _, keep := range []string{"# 用户自己的注释（顶层）", "  USER_KEY: sk-user", "records:", "kind: grant", "type: oauth"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("空集撤回了不该动的内容（缺少 %q）：\n%s", keep, got)
		}
	}
}

// TestPatchRefsSectionEmitsEmptyMapping 平台条目被撤光时**不能**留下裸 `refs:`（那是 null，
// 属于官方拒收的"类型错误"）。
func TestPatchRefsSectionEmitsEmptyMapping(t *testing.T) {
	got := patchRefsSection("version: 1\n\nrefs:\n  MATE_X_11111111_API_KEY: sk-x\n", nil,
		managedRefMatcher(DefaultRefPattern))
	if want := "version: 1\n\nrefs: {}\n"; got != want {
		t.Fatalf("撤光平台条目后应写成空映射：\ngot=%q\nwant=%q", got, want)
	}
	// 反向：本来就没有平台条目时，裸 `refs:` 原样保留（不改字节）。
	src := "version: 1\n\nrefs:\n"
	if got := patchRefsSection(src, nil, managedRefMatcher(DefaultRefPattern)); got != src {
		t.Fatalf("没有平台条目要撤时不该改字节：got=%q want=%q", got, src)
	}
	// `refs: {}` 这种内联空值也要能被识别（否则会追加出重复键，dsh 直接拒启）。
	got = patchRefsSection("version: 1\n\nrefs: {}\n", map[string]string{"MATE_A_11111111_API_KEY": "sk-a"},
		managedRefMatcher(DefaultRefPattern))
	if strings.Count(got, "refs:") != 1 || !strings.Contains(got, "MATE_A_11111111_API_KEY: sk-a") {
		t.Fatalf("`refs: {}` 应被就地改写而不是追加第二份：\n%s", got)
	}
}

// TestPatchRefsSectionAppendsWhenAbsent 文档里没有 `refs:` 时追加一节（不重写已有分节）。
func TestPatchRefsSectionAppendsWhenAbsent(t *testing.T) {
	src := "version: 1\n\nrecords:\n  x/y:\n    kind: api-key\n"
	got := patchRefsSection(src, map[string]string{"MATE_A_11111111_API_KEY": "sk-a"}, managedRefMatcher(DefaultRefPattern))
	if !strings.HasPrefix(got, src) {
		t.Fatalf("已有分节必须逐字保留：\n%s", got)
	}
	if !strings.Contains(got, "\nrefs:\n  MATE_A_11111111_API_KEY: sk-a\n") {
		t.Fatalf("应追加一节：\n%s", got)
	}
	// 没有条目要写、也没有分节时，一个字节都不动（绝不造空文档）。
	if got := patchRefsSection(src, nil, managedRefMatcher(DefaultRefPattern)); got != src {
		t.Fatalf("没有要写的条目时不该改字节：%q", got)
	}
}

// TestPatchRefsSectionIdempotent 同一份输入重复调用收敛：文本逐字节不变。
func TestPatchRefsSectionIdempotent(t *testing.T) {
	src := "version: 1\n\nrefs:\n  USER_KEY: sk-user\n"
	want := map[string]string{"MATE_A_11111111_API_KEY": "sk-a"}
	once := patchRefsSection(src, want, managedRefMatcher(DefaultRefPattern))
	twice := patchRefsSection(once, want, managedRefMatcher(DefaultRefPattern))
	if once != twice {
		t.Fatalf("重复调用应收敛：\nonce =%q\ntwice=%q", once, twice)
	}
}

func TestManagedRefMatcher(t *testing.T) {
	cases := []struct {
		pattern string
		key     string
		want    bool
	}{
		{DefaultRefPattern, "MATE_FOO_1234ABCD_API_KEY", true},
		{DefaultRefPattern, "USER_KEY", false},
		{"", "MATE_FOO_1234ABCD_API_KEY", true}, // 空 = 默认模板
		{"FOO_%s_BAR", "FOO_X_BAR", true},
		{"FOO_%s_BAR", "MATE_X_API_KEY", false},
		{"FIXED_NAME", "FIXED_NAME", true},
		{"FIXED_NAME", "FIXED_NAME_2", false}, // 无 %s = 精确匹配，不做前缀放行
		{"%s_SUFFIX", "ANYTHING", false},      // 前缀为空：认不出来就一个都不删（删错不可恢复）
	}
	for _, tc := range cases {
		if got := managedRefMatcher(tc.pattern)(tc.key); got != tc.want {
			t.Errorf("managedRefMatcher(%q)(%q) = %v，期望 %v", tc.pattern, tc.key, got, tc.want)
		}
	}
}

func TestWriteCredentialRefsCreatesDoc(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".credentials.yaml")
	changed, err := writeCredentialRefs(path, map[string]string{"MATE_A_11111111_API_KEY": "sk-a"}, managedRefMatcher(DefaultRefPattern))
	if err != nil || !changed {
		t.Fatalf("首次写应成功且 changed=true：changed=%v err=%v", changed, err)
	}
	want := "version: 1\n\nrefs:\n  MATE_A_11111111_API_KEY: sk-a\n"
	if got := readFile(t, path); got != want {
		t.Fatalf("新建文档的形状不符：\ngot =%q\nwant=%q", got, want)
	}
	assertPerm(t, path, 0o600)
}

func TestWriteCredentialRefsTightensPermissionsOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.yaml")
	if err := os.WriteFile(path, []byte(docWithUserContent), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := writeCredentialRefs(path, map[string]string{"MATE_NEW_BBBB2222_API_KEY": "sk-new"}, managedRefMatcher(DefaultRefPattern))
	if err != nil || !changed {
		t.Fatalf("内容需变化时应成功：changed=%v err=%v", changed, err)
	}
	assertPerm(t, path, 0o600)
}

// TestWriteCredentialRefsFailsLoudOnWidePermissions 权限宽于 0600 是**拒启**，所以这条必须 fail-loud；
// 且错误文案里**绝不能**出现密钥值（键名可以打印）。
func TestWriteCredentialRefsFailsLoudOnWidePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 没有可检查的 mode（dsh 自己也是跳过而不是伪造）")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.yaml")
	// 内容已经正确（于是不会重写），但权限是 0644 → 读回校验必须报错。
	content := "version: 1\n\nrefs:\n  MATE_A_11111111_API_KEY: sk-super-secret\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := writeCredentialRefs(path, map[string]string{"MATE_A_11111111_API_KEY": "sk-super-secret"}, managedRefMatcher(DefaultRefPattern))
	if err == nil {
		t.Fatal("权限 0644 必须中止报错（dsh 会拒启）")
	}
	if changed {
		t.Fatal("校验失败时不得声称改过字节")
	}
	if !strings.Contains(err.Error(), "0600") {
		t.Fatalf("错误文案应指出 0600，实际：%v", err)
	}
	if strings.Contains(err.Error(), "sk-super-secret") {
		t.Fatalf("错误文案里出现了密钥值（契约⑤禁止）：%v", err)
	}
}

// TestSyncCredentialsEmptySetOnlyTouchesPlatformRefs 走完整入口：空集撤回后用户内容逐字保留。
func TestSyncCredentialsEmptySetOnlyTouchesPlatformRefs(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, ".credentials.yaml")
	if err := os.WriteFile(doc, []byte(docWithUserContent), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &syncer{credDocPath: doc, refPattern: DefaultRefPattern}

	out, err := s.syncCredentials(resource.Set{Runtime: ID})
	if err != nil {
		t.Fatalf("空集撤回失败：%v", err)
	}
	if out.Channel != resource.ChannelNativeDoc || out.Effect != resource.EffectNextTurn || !out.Tested {
		t.Fatalf("凭据结论不符：%+v", out)
	}
	got := readFile(t, doc)
	if strings.Contains(got, "MATE_OLD") {
		t.Fatalf("平台旧条目应被移除：\n%s", got)
	}
	for _, keep := range []string{"# 用户自己的注释（顶层）", "  USER_KEY: sk-user", "records:", "kind: grant"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("空集撤回了用户内容（缺少 %q）：\n%s", keep, got)
		}
	}
}

// TestSyncCredentialsNoDocPathDoesNotClaimChannel 没有落点时**不冒充**任何通道。
func TestSyncCredentialsNoDocPathDoesNotClaimChannel(t *testing.T) {
	s := &syncer{}
	out, err := s.syncCredentials(resource.Set{Runtime: ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.Channel != resource.ChannelNone || out.Tested {
		t.Fatalf("没有落点时应如实报 none/untested，实际 %+v", out)
	}
}

// TestSyncCredentialsPrefersSetPath 落点优先取编排层（Set）的决定，Options 只作空集兜底。
func TestSyncCredentialsPrefersSetPath(t *testing.T) {
	a, b := filepath.Join(t.TempDir(), "a.yaml"), filepath.Join(t.TempDir(), "b.yaml")
	s := &syncer{credDocPath: b, refPattern: DefaultRefPattern}
	set := resource.Set{
		Runtime:           ID,
		CredentialDocPath: a,
		Providers:         []resource.Provider{{ID: "gw", Ref: "MATE_GW_11111111_API_KEY", APIKey: "sk-a"}},
	}
	if _, err := s.syncCredentials(set); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); err != nil {
		t.Fatalf("应写到 Set 给出的落点：%v", err)
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Fatalf("Options 的落点不该被用到（Set 已给出落点）")
	}
}

func TestYAMLScalar(t *testing.T) {
	plain := []string{"sk-abc_123", "MATE_GW_255CB31F_API_KEY", "sk-proj-aBcD1234"}
	for _, s := range plain {
		if got := yamlScalar(s); got != s {
			t.Errorf("%q 应为裸标量，实际 %q", s, got)
		}
	}
	quoted := []string{
		"",                      // 空值（官方明确拒收空值，但引号形态至少可解析）
		"a: b",                  // 映射指示符
		"a # b",                 // 注释指示符
		"line1\nline2",          // 多行
		" leading", "trailing ", // 首尾空格
		"true", "null", "no", // 保留字
		"-leading-dash", "@at", // 指示符开头
		"tab\there", "\"quoted\"", // 需要转义的字符
	}
	for _, s := range quoted {
		got := yamlScalar(s)
		if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
			t.Errorf("%q 应为双引号标量，实际 %q", s, got)
		}
	}
	// 转义必须与 JSON 规则一致（YAML 双引号标量不支持裸换行/裸制表符）。
	if got := yamlScalar("a\nb"); got != `"a\nb"` {
		t.Errorf("换行应转义成 \\n，实际 %q", got)
	}
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("%s 权限应为 %04o，实际 %04o", path, want, got)
	}
}

// TestPatchRefsSectionKeepsAnnotationOnRotation **轮换密钥**（平台条目仍在 desired 里）时，
// 条目上方的注释必须留下、缩进必须保持——纪律⑤要的是"注释逐字保住"，不是"条目重建"。
func TestPatchRefsSectionKeepsAnnotationOnRotation(t *testing.T) {
	src := "version: 1\n\nrefs:\n  # 这条由平台写入（运维备注）\n  MATE_X_11111111_API_KEY: sk-old\n  USER_KEY: sk-user\n"
	got := patchRefsSection(src, map[string]string{"MATE_X_11111111_API_KEY": "sk-new"}, managedRefMatcher(DefaultRefPattern))
	want := "version: 1\n\nrefs:\n  # 这条由平台写入（运维备注）\n  MATE_X_11111111_API_KEY: sk-new\n  USER_KEY: sk-user\n"
	if got != want {
		t.Fatalf("轮换应保持注解与位置：\ngot =%q\nwant=%q", got, want)
	}
}

// TestSyncCredentialsRotatesInPlace 走完整入口的轮换：文档里旧密钥被换掉，用户内容一字不动。
func TestSyncCredentialsRotatesInPlace(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, ".credentials.yaml")
	src := "version: 1\n\nrefs:\n  MATE_GW_11111111_API_KEY: sk-old\n  USER_KEY: sk-user\n\nrecords:\n  llm-pi-ai/x:\n    kind: api-key\n"
	if err := os.WriteFile(doc, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &syncer{credDocPath: doc, refPattern: DefaultRefPattern}
	set := resource.Set{
		Runtime:   ID,
		Providers: []resource.Provider{{ID: "gw", Ref: "MATE_GW_11111111_API_KEY", APIKey: "sk-rotated"}},
	}
	out, err := s.syncCredentials(set)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != resource.StatusApplied {
		t.Fatalf("轮换应报 applied，实际 %q", out.Status)
	}
	got := readFile(t, doc)
	if strings.Contains(got, "sk-old") || !strings.Contains(got, "MATE_GW_11111111_API_KEY: sk-rotated") {
		t.Fatalf("密钥应就地轮换：\n%s", got)
	}
	if !strings.Contains(got, "  USER_KEY: sk-user") || !strings.Contains(got, "kind: api-key") {
		t.Fatalf("用户内容必须逐字保留：\n%s", got)
	}
	// 幂等：再同步一次不改字节。
	out2, err := s.syncCredentials(set)
	if err != nil {
		t.Fatal(err)
	}
	if out2.Status != resource.StatusUnchanged || readFile(t, doc) != got {
		t.Fatalf("重复同步应收敛（status=%q）", out2.Status)
	}
}
