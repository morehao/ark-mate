package archtest

// 本文件钉住 `docs/unimplemented.md` 与出货文档的**口径自洽**（2026-10-17 加）。
//
// 背景：2026-10-16 文档重建把 `docs/agent-runtime-refactor-design-20260908.md` 删了，
// 而 `unimplemented.md` 里 30 处 `§19.x` 还指着它；同时有 5 条内容已被后续提交反掉
// （O3 说「无连接池」、O14 说「每部署一个模式」、O17 说「不能改路径」……），三条
// 静默漂移都不会让任何既有测试变红。文档没有编译器，所以这里给它补上判据。
//
// 四条判据（都 fail-closed：解析不到表 / 扫不到文件 / git 不可用，一律失败）：
//
//	① 编号有规则：§1 是 D、§2 是 O、§3 是 C；全文件不重号（编号不重用）；
//	② 记录列可复核：每行最后一列必须含一个**存在的仓库路径**或**存在的提交号**，
//	   或者该行用 §19.x 时文件头必须有归档文档指针（路径 + `git show` + 可解析提交号）；
//	③ 出货文档不再出现 `ADR-\d+`：ADR 编号是各 design spec 私有的，跨 spec 撞号
//	   （ADR-14 至少有权限记忆键 / 凭据交付 / 图片连接期事实三种含义），仓库内没有定义表；
//	④ 不再出现已失效的表述：每个禁词都写过一次真实事故，回潮即红。
//
// 反证（实跑）：把一条 C 编号改回 O → ① 红；把某行记录列改成「见当年那份文档」→ ② 红；
// 抽掉头部归档指针 → ② 红；在 roadmap 加一处 `（ADR-10）` → ③ 红；把
// `workspace_path_immutable` 写回 unimplemented → ④ 红。

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// unimplRow 是 unimplemented.md 表格里的一行（三个 section 的列数不同，最后两列语义不同）。
type unimplRow struct {
	id     string
	record string // §1/§2 的「记录」，§3 的「关闭依据」
	raw    string
	line   int
}

var (
	unimplSectionRe = regexp.MustCompile(`^## ([123])\.`)
	unimplIDRe      = regexp.MustCompile(`^[A-Z]\d+$`)
	unimplHashRe    = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	repoPathRe      = regexp.MustCompile(`[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+\.(?:tsx|mjs|json|html|go|ts|md)`)
	adrRefRe        = regexp.MustCompile(`ADR-\d+`)
)

// unimplSections 解析 unimplemented.md 的三张表（按 `## 1.` / `## 2.` / `## 3.` 分节）。
//
// 解析失败一律 Fatal：目录/标题改名之后，最坏的表现是「守卫照样绿、只是不再检查任何行」。
func unimplSections(t *testing.T) (map[int][]unimplRow, string) {
	t.Helper()
	root := filepath.Dir(backendRoot(t))
	raw, err := os.ReadFile(filepath.Join(root, "docs", "unimplemented.md"))
	if err != nil {
		t.Fatalf("读 docs/unimplemented.md 失败: %v", err)
	}
	text := string(raw)

	out := map[int][]unimplRow{}
	section := 0
	for i, line := range strings.Split(text, "\n") {
		if m := unimplSectionRe.FindStringSubmatch(line); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("解析 §%s 失败: %v", m[1], err)
			}
			section = n
			continue
		}
		if section == 0 {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		// 表头与分隔行：`| # | … |` / `|---|…`
		if strings.Contains(trimmed, "---") {
			continue
		}
		cells := splitTableRow(trimmed)
		if len(cells) < 2 || !unimplIDRe.MatchString(cells[0]) {
			// 表头（`#`）或不是数据行
			continue
		}
		wantCols := 4
		if section == 3 {
			wantCols = 3
		}
		if len(cells) != wantCols {
			t.Fatalf("docs/unimplemented.md:%d 列数 = %d，期望 %d（§%d 的表格形态变了，判据会读错列）",
				i+1, len(cells), wantCols, section)
		}
		out[section] = append(out[section], unimplRow{
			id:     cells[0],
			record: cells[len(cells)-1],
			raw:    trimmed,
			line:   i + 1,
		})
	}
	if len(out[1]) == 0 || len(out[2]) == 0 || len(out[3]) == 0 {
		t.Fatalf("docs/unimplemented.md 的三张表没解析到（§1=%d §2=%d §3=%d）——判据失去对象",
			len(out[1]), len(out[2]), len(out[3]))
	}
	return out, text
}

// splitTableRow 按 `|` 切表格行（去掉首尾竖线），每格去空白。
func splitTableRow(line string) []string {
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// TestUnimplementedIdsFollowSectionPrefix 见文件头 ①。
func TestUnimplementedIdsFollowSectionPrefix(t *testing.T) {
	rows, _ := unimplSections(t)
	wantPrefix := map[int]string{1: "D", 2: "O", 3: "C"}
	seen := map[string]int{}
	total := 0
	for section := 1; section <= 3; section++ {
		for _, r := range rows[section] {
			total++
			if !strings.HasPrefix(r.id, wantPrefix[section]) {
				t.Errorf("docs/unimplemented.md:%d 的编号 %s 在 §%d——该节只许 %s 前缀（编号不重用：移入 §3 的一律重编为 C）",
					r.line, r.id, section, wantPrefix[section])
			}
			if prev, ok := seen[r.id]; ok {
				t.Errorf("编号 %s 重复（第 %d 行与第 %d 行）——同一个 ID 不能同时表示两件事", r.id, prev, r.line)
			}
			seen[r.id] = r.line
		}
	}
	if total < 20 {
		t.Fatalf("只解析到 %d 行（< 20）——表格形态可能已经变了，判据正在空转", total)
	}
}

// TestUnimplementedRecordsAreResolvable 见文件头 ②。
func TestUnimplementedRecordsAreResolvable(t *testing.T) {
	root := filepath.Dir(backendRoot(t))
	rows, text := unimplSections(t)

	archiveOK := archivedDocPointerResolves(t, root, text)
	resolved := 0
	for section := 1; section <= 3; section++ {
		for _, r := range rows[section] {
			if strings.TrimSpace(r.record) == "" {
				t.Errorf("docs/unimplemented.md:%d（%s）的记录列为空——它要回答「下一个人去哪复核」", r.line, r.id)
				continue
			}
			if unimplRecordResolves(t, root, r.record) {
				resolved++
				continue
			}
			if strings.Contains(r.raw, "§") && archiveOK {
				resolved++
				continue
			}
			t.Errorf("docs/unimplemented.md:%d（%s）的记录列无法复核：%q——必须含一个存在的仓库路径、或一个存在的提交号；用 §19.x 时文件头必须给出归档文档指针", r.line, r.id, r.record)
		}
	}
	if resolved == 0 {
		t.Fatalf("一条可复核记录都没解析到——判据失效")
	}
}

// archivedDocPointerResolves 校验文件头的归档文档指针（§19.x 的复核入口）。
func archivedDocPointerResolves(t *testing.T, root, text string) bool {
	t.Helper()
	if !strings.Contains(text, "归档文档指针") ||
		!strings.Contains(text, "docs/agent-runtime-refactor-design-20260908.md") ||
		!strings.Contains(text, "git show ") {
		return false
	}
	hashes := unimplHashRe.FindAllString(text, -1)
	for _, h := range hashes {
		if gitCommitExists(t, root, h) {
			return true
		}
	}
	return false
}

// unimplRecordResolves 判断一条记录列是否至少有一个可复核锚。
func unimplRecordResolves(t *testing.T, root, record string) bool {
	t.Helper()
	for _, cand := range repoPathRe.FindAllString(record, -1) {
		if repoPathExists(root, cand) {
			return true
		}
	}
	for _, h := range unimplHashRe.FindAllString(record, -1) {
		if gitCommitExists(t, root, h) {
			return true
		}
	}
	return false
}

// repoPathExists 按仓库根 / backend/ / frontend/ 三种基址解析路径。
func repoPathExists(root, cand string) bool {
	for _, base := range []string{"", "backend/", "frontend/"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(base+cand))); err == nil {
			return true
		}
	}
	return false
}

// gitCommitExists 用 git 校验提交号（记录列的另一半可复核锚）。
//
// fail-closed：git 命令**不可用**（不是「查不到」）时直接 Fatal——「查不到就跳过」会让
// 这条路在 CI 上静默失效，而那正是本文件要消灭的东西。
//
// 浅克隆同样 Fatal（2026-10-22，CI 基线第一次真实运行暴露）：要比对的提交号是**历史锚**
// （归档文档已删，`git show <rev>` 是它唯一的复核入口），而 `actions/checkout` 默认
// `fetch-depth: 1`——那种仓库里**任何**历史提交都 `rev-parse` 不到，于是这条判据把
// "克隆太浅"报成二十来行「记录列无法复核」，人得读完整张表才可能想到这一层。
// 现在直接说清楚原因和改法。
func gitCommitExists(t *testing.T, root, hash string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", hash+"^{commit}").CombinedOutput()
	if err != nil {
		var ee *exec.Error
		if errors.As(err, &ee) {
			t.Fatalf("git 不可用（%v）——记录列的可复核性依赖 git，不能静默放过", err)
		}
		if isShallowRepo(t, root) {
			t.Fatalf("仓库是**浅克隆**（%s 里查不到历史提交）：本判据的复核锚点全在历史里，"+
				"浅克隆会把「克隆太浅」报成「记录列无法复核」。CI 里请给 actions/checkout 配 "+
				"`fetch-depth: 0`（见 .github/workflows/ci.yml）。", root)
		}
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// isShallowRepo 报告仓库是否是浅克隆（只在 git 已确认可用后调用）。
func isShallowRepo(t *testing.T, root string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "rev-parse", "--is-shallow-repository").Output()
	if err != nil {
		return false // 老版本 git 没有这个开关：退回原行为（只报「查不到」）
	}
	return strings.TrimSpace(string(out)) == "true"
}

// TestShippedDocsHaveNoAdrReferences 见文件头 ③。
func TestShippedDocsHaveNoAdrReferences(t *testing.T) {
	root := filepath.Dir(backendRoot(t))
	var files []string
	for _, rel := range []string{"README.md", "AGENTS.md"} {
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("扫描面成员不存在：%s（%v）", p, err)
		}
		files = append(files, p)
	}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d os.DirEntry, err error) error {
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
	if len(files) < 5 {
		t.Fatalf("只扫到 %d 个文档——扫描面失效", len(files))
	}

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", f, err)
		}
		rel, _ := filepath.Rel(root, f)
		for i, line := range strings.Split(string(raw), "\n") {
			if m := adrRefRe.FindString(line); m != "" {
				t.Errorf("%s:%d 出现 %s——ADR 编号是各 design spec 私有的，跨 spec 会撞号；请改成它旁边那句结论", rel, i+1, m)
			}
		}
	}
}

// TestDocsHaveNoStaleClaims 见文件头 ④。
func TestDocsHaveNoStaleClaims(t *testing.T) {
	root := filepath.Dir(backendRoot(t))
	banned := map[string][]struct{ phrase, why string }{
		"docs/unimplemented.md": {
			{"每部署一个模式", "O14 的旧事实：v29 起模式进 ConnKey，不同模式各一个暖进程"},
			{"workspace_path_immutable", "该错误码已随 9e35fe8 删除（改路径已实现）"},
			{"无连接池", "internal/infra/protocol/mcp/pool.go 已是连接池"},
			{"前端策略编辑", "该屏不存在（当前无策略路由/DTO/编辑面）"},
		},
		"docs/roadmap.md": {
			{"保持未关闭", "L3-2 已关闭（见该节标题与 unimplemented §3）"},
			{"O16 是它的当前记录处", "O16 已关闭并改名为 C19"},
		},
	}
	for rel, phrases := range banned {
		p := filepath.Join(root, filepath.FromSlash(rel))
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", rel, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for _, b := range phrases {
				if strings.Contains(line, b.phrase) {
					t.Errorf("%s:%d 仍写着已失效的 %q——%s", rel, i+1, b.phrase, b.why)
				}
			}
		}
	}
}
