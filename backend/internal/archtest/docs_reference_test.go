package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件钉住 **docs/ 的引用不悬空**（2026-10-16 文档重建时加）。
//
// 为什么需要它：重建之前，README 指着 `docs/dsh-acp-adaptation.md`，而磁盘上的文件叫
// `deepseekharness-acp-adaptation.md`——那条链接断了很久，**没有任何机制会红**。代码有编译器和
// 守卫，文档没有：一次改名、一次搬迁，指向它的每一条引用都会静默失效，而失效的表现是
// "下一个人照着一条不存在的路径去找事实源"。同一次重建清出 11 处这样的悬空指针
// （README / AGENTS.md / 8 个 Go 文件的注释），所以把它变成机器判据。
//
// 判据（fail-closed：扫描面缺成员、或一条引用都扫不到，都判失败）：
//
//	① 扫描面 = `README.md` + `AGENTS.md` + `docs/**/*.md` + `backend/**/*.go` + 前端界面源码
//	   （`uiSrcRoots()`；后端跳过 `internal/archtest` 自身——与 vocabulary / knowledge_boundary /
//	   logging_boundary 同一手法，本文件的注释里就写着那些路径形态）；
//	② 扫描面里每一条 `docs/<…>.md` 引用必须在磁盘上存在；
//	③ `docs/**` 内的 markdown 相对链接（`](./x.md)`）必须相对该文件存在；
//	④ **刻意不扫 `.dsh/docs/`**：那是内部工作笔记（gitignore、不进仓库），它的生命周期与本仓文档不同。
//
// 历史引用（指向 2026-10-16 重建时删除的那批旧设计文档）**必须逐条登记**在 historicalDocs 里并写明
// 理由——白名单是"这次确实该留着"的登记簿，不是"懒得改"的垃圾桶。
//
// 反证（实跑）：把 `README.md` 里的 `docs/roadmap.md` 改成任何一个不存在的名字 → 红；
// 从 historicalDocs 里删掉 `design-and-roadmap` → 红（`decisions.md` 的沿革段还在引用它）；
// 把扫描面的 README.md 删掉 → fatal。
var docsRefRe = regexp.MustCompile(`docs/[A-Za-z0-9._/-]+\.md`)

// mdRelLinkRe 抓 markdown 链接目标里的 `.md`（`](./x.md)`、`](assets/x.md)`）。
var mdRelLinkRe = regexp.MustCompile(`\]\(([^)\s]+\.md)\)`)

// historicalDocs 是**允许**指向已不存在文件的引用（按 basename 匹配）。
//
// 它们都指向 2026-10-16 文档重建时删除的旧设计文档——历史整体留在 git 里，而新文档的「沿革」
// 段落必须能说清"这一节是从哪来的"，那正是这些引用的作用。**每一条都要有理由**。
var historicalDocs = []struct{ name, reason string }{
	{"design-and-roadmap", "旧的总设计文档（含 M0–M3 路线图与 v2–v27 决策块）。决策块已原样迁进 docs/decisions.md，正文与旧路线图整体删除；沿革段引用它说明来源"},
	{"agent-runtime-refactor-design-20260908", "旧的 P0–P3 重构设计与逐切片证据。§20「未实现能力与降级清单」已抽出成 docs/unimplemented.md（并逐条按代码复核），其余删除"},
	{"desktop-architecture-design-20260928", "旧的桌面端架构设计。§7.3「桌面与浏览器的差异清单」已迁进 docs/acceptance-matrix.md，其余删除"},
	{"agent-interactions-acp-v1-design-20260907", "旧的 ACP elicitation/权限交互设计。仍开放的 elicitation 会话归属缺口登记在 docs/unimplemented.md D1"},
	{"p0-hardening-design-20260911", "旧的加固设计（脱敏闸门等）。闸门本身仍在 internal/platform/redact，设计过程不再单独成文"},
	{"capability-ecosystem-design-20260922", "旧的能力生态设计（三张表、投影、密钥、写面）"},
	{"capability-change-apply-design-20261008", "旧的能力变更生效设计（指纹与代际）"},
	{"model-selection-design-20260909", "旧的模型选择设计（模型面已由决策 v24–v27 三次收口取代）"},
	{"zacp-alignment-refactor-design-20260908", "旧的对齐重构设计（持久化 / 管理面已落地）"},
	{"architect-design", "旧的架构基线文档（当前架构事实见 docs/architecture.md）"},
	{"deepseekharness-acp-adaptation", "旧文件名的底座适配核实记录（v0.1.x 快照）。同名内容已重写为 docs/dsh-acp-adaptation.md——注意 README 与 internal/infra/dsh 此前指向的就是那个新名字，本次让磁盘追上引用"},
}

// historicalDocsAllow 按 basename 查历史白名单。
func historicalDocsAllow(t *testing.T, base string) (string, bool) {
	t.Helper()
	for _, h := range historicalDocs {
		if h.name == base {
			if strings.TrimSpace(h.reason) == "" {
				t.Fatalf("历史白名单 %q 没有写理由——空理由的登记等于垃圾桶", base)
			}
			return h.reason, true
		}
	}
	return "", false
}

// TestDocsReferencesResolve 见文件头。
func TestDocsReferencesResolve(t *testing.T) {
	repoRoot := filepath.Dir(backendRoot(t))

	readme := filepath.Join(repoRoot, "README.md")
	agents := filepath.Join(repoRoot, "AGENTS.md")
	docsDir := filepath.Join(repoRoot, "docs")
	for _, must := range []string{readme, agents, docsDir} {
		if _, err := os.Stat(must); err != nil {
			t.Fatalf("扫描面成员不存在：%s（%v）——守卫失去对象", must, err)
		}
	}

	var files []string
	files = append(files, readme, agents)

	// docs/**/*.md
	err := filepath.WalkDir(docsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 docs/ 失败: %v", err)
	}

	// backend/**/*.go，跳过 internal/archtest 自身。
	for _, p := range goFilesUnder(t, filepath.Join(repoRoot, "backend")) {
		rel, _ := filepath.Rel(repoRoot, p)
		if strings.HasPrefix(filepath.ToSlash(rel), "backend/internal/archtest/") {
			continue
		}
		files = append(files, p)
	}

	// 前端界面源码（`uiSrcRoots()` 是"界面在哪"的单点声明）。加这一面是有实测依据的：
	// 2026-10-16 重建时，设置页有一处**用户可见**的 `hint="见 docs/design-and-roadmap.md §5.3"`
	// 与一处注释同样悬空——只扫后端会漏掉"界面把一个不存在的路径展示给用户"这种形态。
	for _, root := range uiSrcRoots(t) {
		files = append(files, listFrontendSources(t, root)...)
	}

	refs := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", f, err)
		}
		s := string(raw)
		rel, _ := filepath.Rel(repoRoot, f)

		// ② `docs/<…>.md` 形态（绝对到仓库根）
		for _, idx := range docsRefRe.FindAllStringIndex(s, -1) {
			ref := s[idx[0]:idx[1]]
			// 刻意不扫 .dsh/docs/（内部笔记，不进仓库）
			if strings.HasSuffix(s[:idx[0]], ".dsh/") {
				continue
			}
			refs++
			if _, err := os.Stat(filepath.Join(repoRoot, ref)); err == nil {
				continue
			}
			if reason, ok := historicalDocsAllow(t, baseNoExt(ref)); ok {
				_ = reason
				continue
			}
			t.Errorf("%s 指向不存在的文档 `%s`——改名/搬迁时请同步这条引用（本次重建清出 11 处这类悬空指针）", rel, ref)
		}

		// ③ markdown 相对链接（只对 docs/ 下的文件有意义）
		if !strings.HasPrefix(filepath.ToSlash(rel), "docs/") {
			continue
		}
		for _, m := range mdRelLinkRe.FindAllStringSubmatch(s, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "/") {
				continue
			}
			refs++
			if _, err := os.Stat(filepath.Join(filepath.Dir(f), target)); err == nil {
				continue
			}
			if _, ok := historicalDocsAllow(t, baseNoExt(target)); ok {
				continue
			}
			t.Errorf("%s 的相对链接 `%s` 指向不存在的文件", rel, target)
		}
	}

	// ④ 判据本身失效（改名/搬迁会让它静默空转）
	if refs == 0 {
		t.Fatalf("一条 docs 引用都没扫到（扫了 %d 个文件）——判据失效，检查扫描面是否还在", len(files))
	}
}

// baseNoExt 取路径的文件名去掉扩展名（历史白名单按 basename 匹配）。
func baseNoExt(p string) string {
	return strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
}
