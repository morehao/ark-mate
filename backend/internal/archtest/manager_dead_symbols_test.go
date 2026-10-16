package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ——— 「manager 里不留死符号」守卫（P4a 正文第四十七刀） ———
//
// **为什么值得一条机器判据**：`go build` / `go vet` **都不报**未使用的函数与方法（与未使用的
// 变量、import 不同），所以「实现搬走了，它在本包留下的私有帮手还挂着」这一类残留**没有任何
// 编译期信号**。它的危害不是报错，而是让「这段实现到底还在不在 manager 里」重新不可见——
// P4a 这一批每一刀都要回答那个问题，而残留会让答案偏悲观：看起来还有一大堆没搬。
//
// 第四十七刀实测：manager 的生产文件里躺着 **21 个**这样的符号（`advertisedModels` /
// `materializeMessages` / `sessionResp` / `terminalRunStatus` / `derefList` / `mirrorValue` /
// `matchEnvKey` / `writeSkillEnabled` …），合计 148 行，全部只有一处出现（它自己的声明），
// 而它们**全都是**前几十刀搬实现时留下的私有转发与转发帮手。
//
// 判据（fail-closed）：扫 `internal/manager/*.go`（跳过 `_test.go`）里 `^func (recv) name(` 的
// **未导出**符号，在**剥掉行注释**的 `internal/**` + `cmd/**` 文本里数它的出现次数，恰好 1 次
// （= 只有声明那一处）即判红。
//
// **为什么要剥注释**：本仓注释里大量提到符号名（"见 `session.DerefList`"、"与
// `rejectRuntimeHintInPatch` 同源"），不剥的话死符号会被注释喂绿——这是本判据最容易假绿的地方，
// 也是它的**第一版**（不剥注释）实测就会放过 11 个真死符号的原因。
//
// **扫描面解析不到即失败**：一个私有符号都扫不到（目录改名 / 文件搬走）时判失败＝判据失锚，
// 与 `TestFrontendUiScanRootsCoverAllUi` 同形。
func TestManagerHasNoDeadPrivateSymbols(t *testing.T) {
	root := backendRoot(t)

	managerDir := filepath.Join(root, "internal", "manager")
	var prod []string
	for _, p := range goFilesUnder(t, managerDir) {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		prod = append(prod, p)
	}
	if len(prod) == 0 {
		t.Fatal("`internal/manager` 下扫不到任何生产文件——判据失锚（目录改名？文件搬走了？）")
	}

	// 参照面：全仓 `internal/**` + `cmd/**`，**跳过 archtest 自身**（守卫的消息里会写符号名）。
	var refs []string
	for _, dir := range []string{filepath.Join(root, "internal"), filepath.Join(root, "cmd")} {
		for _, p := range goFilesUnder(t, dir) {
			if strings.HasPrefix(filepath.ToSlash(p), filepath.ToSlash(filepath.Join(root, "internal", "archtest"))) {
				continue
			}
			refs = append(refs, stripLineComments(readFileOrFatal(t, p)))
		}
	}
	if len(refs) < 50 {
		t.Fatalf("参照面只拿到 %d 个文件——路径写错了？（fail-closed：解析不到即失败）", len(refs))
	}

	declRE := regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s*)?([a-z][A-Za-z0-9_]*)\s*\(`)
	declared := 0
	var dead []string
	for _, p := range prod {
		src := stripLineComments(readFileOrFatal(t, p))
		for _, m := range declRE.FindAllStringSubmatch(src, -1) {
			name := m[1]
			declared++
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
			hits := 0
			for _, body := range refs {
				hits += len(re.FindAllString(body, -1))
			}
			if hits <= 1 {
				rel, _ := filepath.Rel(root, p)
				dead = append(dead, filepath.ToSlash(rel)+" → "+name)
			}
		}
	}
	if declared < 50 {
		t.Fatalf("只解析到 %d 个私有符号声明——判据失锚（正则或扫描面被改坏了）", declared)
	}
	if len(dead) > 0 {
		t.Errorf("`internal/manager` 里有 %d 个**死私有符号**（只有声明、没有任何引用）：\n  %s\n"+
			"`go build`/`go vet` 都不会报它们，但它们让「这段实现还在不在 manager 里」变成看不清的——"+
			"P4a 这批每一刀都要回答那个问题。\n"+
			"处置：删掉它（实现已经搬走时才可能长成这样），或者在守卫里登记白名单并写明它为什么留着。",
			len(dead), strings.Join(dead, "\n  "))
	}
}
