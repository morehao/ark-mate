package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ——— G7：注释里的仓库路径引用必须存在（2026-10-22）———
//
// 挡的是**搬完家没改注释**这一类静默失效：代码能编译、测试全绿、文档链接也没断，
// 只有"顺着注释去找那个文件"的人会撞空。2026-10-22 那一轮实测：`pkg/runtime` 时代 81 处、
// `pkg/*` 13+107 处、`app/command` 10 处陈旧路径，**全靠人工扫**才发现
// （`TestDocsReferencesResolve` 只管文档与特定形态，扫不到 Go 注释里的 `internal/...`）。
//
// 判据形状：
//  1. 只抽**注释**里的引用（行注释与块注释；代码里的字符串字面量不算——那是运行时取用的
//     真实路径，另有守卫管）；
//  2. 形态 `internal/<pkg>/<sub>`、`internal/<pkg>/<file>.go`、`internal/<pkg>/<pkg>.<Symbol>`
//     （带 `.Symbol` 的只判**包目录**是否存在——符号名不在这里判，它有编译器管）；
//  3. **通配前缀跳过**：后随 `*` 或 `<`（`internal/service/svc*`、`internal/delivery/controller/ctr<领域>`）、
//     或末段以 `_` 结尾（`internal/persistence/store/migrate_v*.go` 这类族名）；
//  4. 目录不存在且未登记的引用 → 红；登记了却**现在存在** → 红（白名单该删）；
//     登记了却在注释里**不再出现** → 红（白名单腐化，它已经是纯噪声）。
//
// 为什么必须留具名白名单：本仓有一批**刻意的历史叙述**——"本文件是 v11 生成器的冻结副本，
// 抄自 `git show <rev>:backend/…`"、领域包里"P4a 第 N 刀从 <旧目录> 搬入"、判据文案里
// "旧目录不得复活"。把这些路径批量"修正"成现存路径，等于把历史读歪，还抹掉了冻结副本的来源。
// 所以白名单是**具名 + 逐条写原因**（不是按文件、按行豁免），并双向检查，不让它长成杂物间。

// historicalPathRefs 是**已在仓库中不存在的路径**的登记表（值 = 为什么保留这个引用）。
//
// 双向：登记了但路径其实存在 → 红（该删）；登记了但注释里不再出现 → 红（该删）。
var historicalPathRefs = map[string]string{
	"internal/manager": "v3.x 之前的单体实现包，按 P2b–P4a 逐刀拆进 domain/*、sessionruntime、drivers/*；领域包与 archtest 里的『搬自/原先在 internal/manager』是历史叙述，不是待改的陈旧引用",
	"internal/manager/capability_selection_validate.go": "P3c 搬进 `domain/session`，注释记的是搬家来源",
	"internal/manager/model_list.go":                    "P4a 第五十九刀搬进 `domain/provider`，注释记的是搬家来源",
	"internal/manager/providers.go":                     "`infra/dsh` 的 v11 冻结副本来源（`git show 358ab27:…`），冻结副本刻意逐字保留",
	"internal/manager/runtime_config.go":                "同上：v11 生成器冻结副本的来源记录",
	"internal/manager/tool_calls.go":                    "P4a 第六十一刀搬进 `domain/session`，注释记的是搬家来源",
	"internal/manager/workspace_settings.go":            "P2b 搬进 `domain/workspace`，注释记的是搬家来源",
	"internal/session/prompt_chunk_coalescer.go":        "同上，`domain/session/coalescer.go` 的搬家来源",
	"internal/sessionruntime/credentials.go":            "v20 去 vault 化时整个删除（77 行），注释记的是被删掉的东西",
	"internal/sessionruntime/transcript_load.go":        "transcript 装载搬进会话域，注释记的是『文件会移包，这行调用不会』",
	"internal/server/handler":                           "同上：handler 的 DTO 归属讨论引用了旧位置",
	"internal/server/router":                            "同上：`delivery/router` 的层数注释在解释『从旧位置往上提』",
	"internal/capabilityio":                             "已拆成 `drivers/transfer` + `registry`；判据文案里『旧目录不得复活』就是在说它",
	"internal/vault":                                    "v20 去 vault 化时整个删除，`vault:<ns>/<name>` 词法退役为 `store:<ns>/<name>`",
	"internal/skills/provenance.go":                     "**外部参照实现**（compozy）的路径，不是本仓路径：`core/capability` 的算法与它逐条对照",
	"internal/skills/verify.go":                         "同上：外部参照实现的模式表来源",
}

// commentPathRefRe 匹配注释里的仓库路径。末段可带 `.go`（文件）或 `.Symbol`（只判包）。
var commentPathRefRe = regexp.MustCompile(`internal/(?:[A-Za-z0-9_\-]+/)*[A-Za-z0-9_\-]+(?:\.(?:go|[A-Za-z_][A-Za-z0-9_]*))?`)

// commentTextRe 抽出注释正文：块注释整体 + 行注释（去掉行内代码部分）。
var commentTextRe = regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`)

func TestCommentPathsResolve(t *testing.T) {
	root := backendRoot(t)
	srcs := goSourcesUnder(t, root)

	scanned := 0
	refs := map[string][]string{} // 引用 → 出现位置（file:line）
	wildcards := 0
	for _, f := range srcs {
		src := readFileOrFatal(t, f)
		relFile := filepath.ToSlash(rel(root, f))
		for _, cm := range commentTextRe.FindAllString(src, -1) {
			for _, m := range commentPathRefRe.FindAllStringIndex(cm, -1) {
				scanned++
				p := strings.TrimRight(cm[m[0]:m[1]], ".,;:)]}】`」'\"")
				// 通配前缀形态：`svc*` / `ctr<领域>` / `migrate_v` 这类族名不算一条具体引用。
				next := ""
				if m[1] < len(cm) {
					next = cm[m[1] : m[1]+1]
				}
				if next == "*" || next == "<" || strings.HasSuffix(p, "_") {
					wildcards++
					continue
				}
				refs[normalizePkgRef(p)] = append(refs[normalizePkgRef(p)], relFile)
			}
		}
	}
	if scanned == 0 || len(refs) == 0 {
		t.Fatalf("注释里扫到 0 条 internal 路径引用：判据失锚（扫描面为空或文件遍历坏了）")
	}
	if wildcards == 0 {
		t.Fatalf("一条通配前缀都没扫到：说明路径形态变了，判据的跳过规则该跟着改（否则它会静默地只查一半）")
	}

	var keys []string
	for p := range refs {
		keys = append(keys, p)
	}
	sort.Strings(keys)

	reported := 0
	for _, p := range keys {
		exists := pathRefExists(root, p)
		_, registered := historicalPathRefs[p]
		switch {
		case registered && exists:
			t.Errorf("%s 已在 `historicalPathRefs` 里登记为「已删除」，但它**现在存在**：白名单该删条目（登记表腐化会让判据对这条路径永久失明）。出现于 %s", p, strings.Join(uniq(refs[p]), ", "))
			reported++
		case !registered && !exists:
			t.Errorf("%s 不存在，也不是登记过的历史路径（出现于 %s）。\n"+
				"\t两种改法：① 改成真实位置（搬完家没改注释是这类失效的常态）；\n"+
				"\t② 确实是历史叙述/外部参照实现，就把它登记进 `historicalPathRefs` 并写清原因。", p, strings.Join(uniq(refs[p]), ", "))
			reported++
		}
	}

	// 白名单防腐化：登记了但注释里已经不再出现的条目 → 红（它已经是纯噪声，且掩盖了别的东西）。
	for p := range historicalPathRefs {
		if len(refs[p]) == 0 {
			t.Errorf("`historicalPathRefs` 里的 %s 在注释里已不再出现：请从登记表删掉这一条（白名单只登记**正在被引用**的历史路径）", p)
			reported++
		}
	}
	if reported == 0 && len(historicalPathRefs) == 0 {
		t.Fatalf("历史路径登记表为空：本仓有一批刻意的历史叙述，空表通常意味着登记被误删")
	}
}

// normalizePkgRef 把 `internal/<pkg>/<sub>.<Symbol>` 归一成包路径 `internal/<pkg>/<sub>`：带符号名的引用
// 只判包是否存在（符号名有编译器管），`.go` 文件名保持原样。
//
// 归一化必须**先于**白名单查找：否则 `internal/vault.Encrypt` 与登记项 `internal/vault`
// 会是两个键，历史叙述每多一个符号名就要多登记一条。
func normalizePkgRef(p string) string {
	if strings.HasSuffix(p, ".go") {
		return p
	}
	for {
		i := strings.LastIndex(p, ".")
		if i < 0 || i < strings.LastIndex(p, "/") {
			return p
		}
		p = p[:i]
	}
}

// pathRefExists 判断注释里的引用是否真的落在一个现存目录/文件上。
func pathRefExists(root, p string) bool {
	target := p
	full := filepath.Join(root, filepath.FromSlash(target))
	if _, err := os.Stat(full); err == nil {
		return true
	}
	if !strings.HasSuffix(target, ".go") {
		if _, err := os.Stat(full + ".go"); err == nil {
			return true
		}
	}
	return false
}

// goSourcesUnder 列出 root 下 internal/ 与 pkg/ 里的全部 .go 文件（含 _test.go：
// 判据文案里的陈旧路径同样会误导人，2026-10-22 那 10 处里就有两处在 archtest 自己的错误消息里）。
func goSourcesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, top := range []string{"internal", "pkg"} {
		base := filepath.Join(root, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			out = append(out, p)
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s 失败: %v", top, err)
		}
	}
	if len(out) == 0 {
		t.Fatalf("扫到 0 个 Go 文件：判据失锚")
	}
	sort.Strings(out)
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
