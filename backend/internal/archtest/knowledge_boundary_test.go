package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是「底座知识归属」的两条静态守卫（v12 T1.1 / T1.4）。
//
// 它们守的是同一件事的两个方向：
//
//	TestInternalHasNoRuntimeConfigLiterals —— 底座配置**格式**的知识不得回流到平台内核；
//	TestAdapterHasNoStoreLiterals          —— adapter 不得反向依赖平台的持久层。
//
// 为什么必须是静态守卫而不是靠约定：v11 的失败正是"靠约定"——生成器、格式串、校验门
// 全都住在 `internal/manager`，接一个新底座就要改内核（T2 不成立），而没有任何机器判据
// 会在知识回流的那一刻报红。判据一旦只用注释表达，下一个人"顺手在这里加一个分支"就再也回不去了。

// runtimeConfigLiterals 底座的配置格式与机制字面量（T1：`internal/` 下零命中）。
//
// 每一个都是**底座私有知识**：它属于 `pkg/runtime/<adapter>`，不属于平台内核。
//   - `opencode.json` / `OPENCODE_CONFIG_CONTENT`：opencode 的配置文件与内联通道；
//   - `mate-models.patch.yml`：平台给 dsh 的 overlay 文件名；
//   - `.credentials.yaml`：dsh 的原生凭据文档（凭据文档的格式与路径同属适配器知识）；
//   - `--dump-config`：dsh 的配置转储子命令（两级校验门的一半）；
//   - `llm-pi-ai`：dsh 的 provider 路由条目 id；
//   - `opencode_json` / `dsh_patch_yaml`：v11 的格式枚举值（格式由 adapter 自己认，
//     内核不再按字符串 switch）。
var runtimeConfigLiterals = []string{
	"opencode.json",
	"OPENCODE_CONFIG_CONTENT",
	"mate-models.patch.yml",
	".credentials.yaml",
	"--dump-config",
	"llm-pi-ai",
	"opencode_json",
	"dsh_patch_yaml",
}

// TestInternalHasNoRuntimeConfigLiterals A9 / T1：`internal/**` 剥注释后不得出现底座配置格式字面量。
//
// 三条纪律：
//   - **剥注释**（`stripTSComments` 同样能处理 Go 的行/块注释与引号）：注释里提到某个底座格式
//     是**解释**，不是知识回流；
//   - **逐行报告**（文件:行号 + 命中词）：命中清单就是迁移工作量的度量（M1 结束时它非空是有意的，
//     M2/M3 把生成器搬进 adapter 之后才归零）；
//   - **不扫本包**：这条守卫自己的禁词表就写着这些字面量，扫自己必然自证其罪。跳过
//     `internal/archtest` 是唯一豁免，且**只豁免这一个目录**。
func TestInternalHasNoRuntimeConfigLiterals(t *testing.T) {
	root := backendRoot(t)
	internalDir := filepath.Join(root, "internal")
	if _, err := os.Stat(internalDir); err != nil {
		t.Fatalf("internal/ 不存在：%v", err)
	}
	hits := 0
	for _, path := range goFilesUnder(t, internalDir) {
		if strings.HasPrefix(filepath.ToSlash(path), filepath.ToSlash(filepath.Join(internalDir, "archtest"))+"/") {
			continue
		}
		hits += scanForLiterals(t, path, runtimeConfigLiterals, "internal/")
	}
	// 不在这里 fail 于"命中数必须为 0"之外的东西：命中清单本身就是本守卫的产物。
	// M1 结束时刻意允许非空（剩余命中点在计划里逐条登记给 M2/M3）。
	_ = hits
}

// storeLiterals adapter 不得出现的持久层标记（T1.4）。
//
// 刻意**不** ban `providers` / `models`：那两个字正是 adapter 要生成的配置键，
// ban 了它自己就写不出配置。ban 的是"SQL/DAO 的形状"与"反向 import 平台的持久层"。
var storeLiterals = []string{
	"SELECT ",
	"INSERT INTO",
	"UPDATE ",
	"DELETE FROM",
	"gorm",
	"dao.",
	modulePrefix + "internal/store",
	modulePrefix + "internal/dao",
	modulePrefix + "internal/model",
}

// TestAdapterHasNoStoreLiterals A9：`pkg/runtime/<adapter>/**` 剥注释后不得出现持久层代码。
//
// 判据是"结构上够不到"：adapter 只认端口传进来的 `Set`，它没有、也不该有读库的能力
// （H5 的机器判据）。一旦有，等价性 golden 就再也证明不了什么——同样的输入可能有不同的输出。
//
// fail-closed：`pkg/runtime/<adapter>` 一个都没有 → 失败（目录改名/搬迁也会让它静默空转）。
func TestAdapterHasNoStoreLiterals(t *testing.T) {
	root := backendRoot(t)
	runtimeDir := filepath.Join(root, "pkg", "runtime")
	adapters := 0
	for _, dir := range subdirs(t, runtimeDir) {
		if !hasGoFiles(dir) {
			continue
		}
		adapters++
		for _, path := range goFilesUnder(t, dir) {
			scanForLiterals(t, path, storeLiterals, "pkg/runtime/<adapter>/")
		}
	}
	if adapters == 0 {
		t.Fatalf("%s 下没有带 Go 文件的 adapter 子包：这条守卫失去了检查对象（改名/搬迁了？）", runtimeDir)
	}
}

// goFilesUnder 返回 dir 下全部 .go 文件（递归；跳过 testdata）。
//
// 目录为空/不存在时返回空切片而不是错误：调用方自己决定"空"是不是失败（见上面两条守卫）。
func goFilesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "node_modules" || (strings.HasPrefix(d.Name(), ".") && path != dir) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败：%v", dir, err)
	}
	return out
}

// scanForLiterals 扫一个文件（剥注释后）里的禁词，逐条报告并返回命中数。
//
// 大小写敏感：这些是**字面量**（文件名、子命令、枚举值），大小写变了就不是同一个东西，
// 大小写无关只会造出误报。
func scanForLiterals(t *testing.T, path string, literals []string, where string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s：%v", path, err)
	}
	rel, rerr := filepath.Rel(backendRoot(t), path)
	if rerr != nil {
		rel = path
	}
	src := stripTSComments(string(raw))
	hits := 0
	for i, line := range strings.Split(src, "\n") {
		for _, lit := range literals {
			if strings.Contains(line, lit) {
				hits++
				t.Errorf("%s 里 %s:%d 出现底座/持久层字面量 %q：\n\t%s\n"+
					"底座配置格式的知识属于 pkg/runtime/<adapter>，不属于平台内核（T1/T2）；"+
					"adapter 也不得依赖 internal/{store,dao,model}（H5）",
					where, rel, i+1, lit, strings.TrimSpace(line))
			}
		}
	}
	return hits
}
