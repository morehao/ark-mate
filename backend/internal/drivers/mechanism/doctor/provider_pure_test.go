package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/credential"
)

// mismatchMarkerLiteral 导入对账标记的**字面量**（刻意不引用供应商域的常量）。
//
// 两个理由：① 本包不得 import 领域（`TestMechanismMustNotImportUpward` 连测试 import 一起看）；
// ② 用字面量反而**更强**——它是写入侧真正落进 `meta` 的那个串，常量改名而这里没改，本用例会
// 当场红（用常量拼的夹具抓不到这种漂移，见计划 §3.1.36 的 NC-R）。
const mismatchMarkerLiteral = "DB 里有、文件里没有"

// 本文件是 `mate doctor` 的 provider 段里**不需要会话/装配侧数据面**的那几条判据。
//
// 它们随实现一起搬进本域（`interaction` 那一批之后的做法：测试跟着所有者走）：判的是本域的
// 纯函数与只读探测（对账分档、遗留锚点、镜像形状），与"装配起来的那份 Service"无关。
// 需要逃生门（真 Service 造只读读数面）的用例留在 `internal/manager`：
// 它们验的是**装配面的接线**，而且本包若 import manager 会形成测试期循环。

// TestProviderMismatchBenignOnly 对账分档：只有"库里有、文件里没有"才算快照滞后。
//
// 这条分档的判据就是写入侧拼串里的那个标记（`mismatchMarkerLiteral`），所以这里
// 直接用它造串——**两侧共用同一个常量**，改文案不会让分档悄悄失效。
func TestProviderMismatchBenignOnly(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		want    bool
	}{
		{"空", nil, false},
		{"只有快照滞后", []string{"a: " + mismatchMarkerLiteral + "（写面新建？）"}, true},
		{"文件里有库里没有", []string{"a: 文件里有、DB 里没有"}, false},
		{"type 不一致", []string{"a: type 不一致（文件 \"x\" / DB \"y\"）"}, false},
		{"混合", []string{
			"a: " + mismatchMarkerLiteral + "（写面新建？）",
			"b: 文件里有、DB 里没有",
		}, false},
	}
	for _, tc := range cases {
		if got := providerMismatchBenignOnly(tc.entries, mismatchMarkerLiteral); got != tc.want {
			t.Errorf("%s：got %t want %t", tc.name, got, tc.want)
		}
	}
}

// TestLegacySecretAnchor 旧机制遗留物的锚点：**ProviderDir 的父目录**优先（与搬运扫描同源）。
//
// 两个文件名不在这里断言（它们由 `internal/persistence/store` 的历史面拥有，见 v20 词规），这里只钉锚点。
func TestLegacySecretAnchor(t *testing.T) {
	if got := LegacySecretAnchor("/h", "/h/providers"); got != "/h" {
		t.Fatalf("应取 ProviderDir 的父目录：%q", got)
	}
	if got := LegacySecretAnchor("/h", "/data/providers/"); got != filepath.Clean("/data") {
		t.Fatalf("尾斜杠应被 Clean 掉：%q", got)
	}
	if got := LegacySecretAnchor("/h", ""); got != "/h" {
		t.Fatalf("ProviderDir 为空时回落到 mate home：%q", got)
	}
	if dir, key := LegacySecretPaths(""); dir != "" || key != "" {
		t.Fatalf("锚点为空 → 两个路径都为空（不给出会指向当前目录的假路径）：%q %q", dir, key)
	}
}

// TestReadCredentialsMirror 托管文档的**只读**探测：不存在 / 权限合规 / 权限过宽 / 形状坏掉，
// 四条路各自给出结论，且**不建文件、不改权限**（医生没有落笔权）。
func TestReadCredentialsMirror(t *testing.T) {
	home := t.TempDir()

	// ① 还没有：可重建，不是故障。
	got := readCredentialsMirror(home)
	if got.Exists || got.PermOK || !strings.Contains(got.Note, "尚未生成") {
		t.Fatalf("还没有文档时应如实报「尚未生成」：%+v", got)
	}
	if _, err := os.Lstat(credential.CredentialsPath(home)); !os.IsNotExist(err) {
		t.Fatalf("探测不许创建文档（err=%v）", err)
	}

	// ② 合规：0600 + 合法映射。
	writeMirrorFile(t, home, 0o600, map[string]string{"providers/p1": "sk-1"})
	got = readCredentialsMirror(home)
	if !got.Exists || !got.PermOK || got.Perm != "0600" || len(got.Raw) != 1 {
		t.Fatalf("合规文档应读得出来：%+v", got)
	}

	// ③ 权限过宽：**硬失败**那一档，且探测不许顺手 chmod。
	if err := os.Chmod(credential.CredentialsPath(home), 0o644); err != nil {
		t.Fatal(err)
	}
	got = readCredentialsMirror(home)
	if got.PermOK || !strings.Contains(got.Note, "chmod 600") {
		t.Fatalf("权限过宽应报出来并给出路：%+v", got)
	}
	if fi, err := os.Stat(credential.CredentialsPath(home)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("探测不许改权限（mode=%v err=%v）", fi.Mode().Perm(), err)
	}
	_ = os.Chmod(credential.CredentialsPath(home), 0o600)

	// ④ 形状坏掉（不是 `引用 → 值` 对象）：可重建，但要说清。
	if err := os.WriteFile(credential.CredentialsPath(home), []byte("[1,2,3]"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = readCredentialsMirror(home)
	if got.Exists && len(got.Raw) != 0 {
		t.Fatalf("坏形状不该被解析成值表：%+v", got)
	}
	if !strings.Contains(got.Note, "JSON 对象") {
		t.Fatalf("坏形状应说清是什么问题：%+v", got)
	}

	// ⑤ home 解析不出来：给出结论而不是 panic。
	if got := readCredentialsMirror(""); got.Path != "" || got.Note == "" {
		t.Fatalf("空 home 应老实报出来：%+v", got)
	}
}

// writeMirrorFile 直接写托管文档（不走 Service：这里要造的是**任意形状**的现场）。
func writeMirrorFile(t *testing.T, home string, perm os.FileMode, values map[string]string) {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credential.CredentialsPath(home), raw, perm); err != nil {
		t.Fatal(err)
	}
}
