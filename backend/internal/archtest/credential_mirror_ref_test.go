package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestProviderMirrorRefFormatIsPinned 托管镜像键的**格式**必须有唯一实现，且钉在凭据机制里。
//
// 为什么值得一条守卫：`providers/<id>` 不是一个内部常量，是**持久化格式**——它写进
// `<MATE_HOME>/credentials.json` 的键，也回写进 `providers.json` 的 `key_ref`。把它改掉
// （或换个地方再拼一次）的后果是**静默**的：已有引用全部指向不存在的键，取值落空，
// 表现为"密钥没配"；而 `pkg/credentials` 的分层顺序、provider 域的档案校验**都不会红**。
//
// 判据三段（都 fail-closed：找不到即失败，避免改名/搬迁后守卫静默空转——这正是 P0 里
// 反复吃过的那类假绿）：
//  1. `ProviderMirrorRefPrefix` 全树**恰好声明一次**；
//  2. 那处声明在 `internal/mechanism/credential/`，取值**逐字**是 `providers/`；
//  3. 全树只有**一个**拼接点（`ProviderMirrorRefPrefix +`），即没有人在别处手拼这个键。
//
// 刻意**不**断言 `"providers/"` 这个字面量全仓唯一：`pkg/config` 的旧目录告警里有同名串，
// 那是指目录不是指键，判进来只会逼着后续加白名单。
func TestProviderMirrorRefFormatIsPinned(t *testing.T) {
	const decl = "ProviderMirrorRefPrefix"
	root := backendRoot(t)
	roots := []string{"internal", "pkg", "cmd"}

	var declFiles []string
	concatSites := 0
	for _, root2 := range roots {
		if _, err := os.Stat(filepath.Join(root, root2)); err != nil {
			continue // 该根不存在（例如没有 cmd/）不算失败
		}
		for _, f := range goFilesUnder(t, filepath.Join(root, root2)) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("读 %s: %v", f, err)
			}
			code := stripLineComments(string(src))
			if regexp.MustCompile(`(?m)^\s*(?:const\s+)?` + decl + `\s*=`).MatchString(code) {
				declFiles = append(declFiles, f)
			}
			concatSites += strings.Count(code, decl+" +")
		}
	}
	if len(declFiles) != 1 {
		t.Fatalf("%s 必须全树恰好声明一次（实际 %d 处：%v）——它是持久化格式，两份声明迟早在某一侧漂移",
			decl, len(declFiles), declFiles)
	}
	got := declFiles[0]
	if !strings.Contains(filepath.ToSlash(got), "internal/mechanism/credential/") {
		t.Fatalf("镜像键格式必须由凭据机制拥有，实际声明在 %s（provider 域拥有它会让「改镜像键」看起来像改 provider）", got)
	}
	src, _ := os.ReadFile(got)
	if !strings.Contains(stripLineComments(string(src)), decl+` = "providers/"`) {
		t.Fatalf("%s 的取值必须逐字是 %q —— 改它等于让所有已有引用指向不存在的键，失效是静默的", decl, "providers/")
	}
	if concatSites != 1 {
		t.Fatalf("镜像键只允许一个拼接点（%s + id），实际 %d 处——第二处就是第二份实现", decl, concatSites)
	}
}
