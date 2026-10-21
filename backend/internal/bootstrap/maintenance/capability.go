package maintenance

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/drivers/transfer"
	"github.com/morehao/ark-mate/internal/persistence/store"
)

// capabilityCmd 分发 `mate capability <动作>`。
func Capability(args []string) {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, capabilityUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "migrate":
		capabilityMigrate(args[1:])
	case "import":
		capabilityImport(args[1:])
	case "export":
		capabilityExport(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知的 capability 动作 %q\n\n%s", args[0], capabilityUsage)
		os.Exit(2)
	}
}

const capabilityUsage = `capability 用法：
  mate capability migrate [--dir <能力目录>] [--dry-run]
      把 skill 定义里的旧 sources[] 改写为 runtimes[]（文件仍是事实源时的迁移）
  mate capability import  [--dir <能力目录>] [--dry-run]
      把目录里的 {skills,mcp}/*.json 导入 SQLite（M6：sqlite 模式下 DB 才是事实源）
      幂等：按行判重，**已有行绝不覆盖**；坏定义逐条报出、其余照常导入
  mate capability export  [--dir <能力目录>] [--dry-run]
      把 SQLite 里的定义写回目录（此后文件是**导出物**，不是事实源）
`

// capabilityImport 显式导入存量定义（`mate capability import`）。
//
// 与装配期自动导入的分工：自动导入看目录指纹（目录没变就整体跳过，保证"我删掉的技能不会自己
// 回来"），显式导入**不看指纹**、每次按行判重——新丢进目录一个文件，跑一次就该进来。
func capabilityImport(args []string) {
	fs := flag.NewFlagSet("capability import", flag.ExitOnError)
	dir := fs.String("dir", "", "能力目录（默认 MATE_CAPABILITIES 或 ~/.ark-mate/capabilities）")
	dryRun := fs.Bool("dry-run", false, "只扫描与校验，不写库")
	_ = fs.Parse(args)

	cfg, _ := config.MustLoad()
	root := capabilityRoot(cfg, *dir)
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "capability import: 打开持久层 %s 失败: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	stats, err := transfer.ImportCapabilityDir(context.Background(), st, root,
		transfer.DefinitionImportOptions{DryRun: *dryRun}, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "capability import: %v（未写入任何内容）\n", err)
		os.Exit(1)
	}
	verb := "新增"
	if *dryRun {
		verb = "将新增"
	}
	if stats.Already {
		fmt.Printf("capability import: %s 的目录指纹未变（装配期已导入过）；要重跑显式导入就用本次这条命令\n", root)
	}
	fmt.Printf("capability import: 扫描 skill %d 条 / mcp %d 条 → %s；库中已有 %d 条（不覆盖），%s %d 条\n",
		stats.Skills, stats.Mcps, cfg.DBPath, stats.Existing, verb, stats.Inserted)
	if stats.Invalid > 0 {
		fmt.Printf("  跳过坏定义 %d 条（上面逐条列了原因）\n", stats.Invalid)
	}
	if stats.Tools > 0 {
		fmt.Printf("  ⚠ %s/tools 下有 %d 个文件：工具目录**不参与持久化**（探测产物每轮重建），本次未导入\n",
			root, stats.Tools)
	}
}

// capabilityExport 把 DB 里的定义写回目录（`mate capability export`）。
func capabilityExport(args []string) {
	fs := flag.NewFlagSet("capability export", flag.ExitOnError)
	dir := fs.String("dir", "", "能力目录（默认 MATE_CAPABILITIES 或 ~/.ark-mate/capabilities）")
	dryRun := fs.Bool("dry-run", false, "只打印将要写出的内容，不改任何文件")
	_ = fs.Parse(args)

	cfg, _ := config.MustLoad()
	root := capabilityRoot(cfg, *dir)
	if root == "" {
		fmt.Fprintln(os.Stderr, "capability export: 能力目录为空（用 --dir 指定，或设置 MATE_CAPABILITIES）")
		os.Exit(2)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "capability export: 打开持久层 %s 失败: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	stats, err := transfer.ExportCapabilityDir(context.Background(), st, root, *dryRun, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "capability export: %v\n", err)
		os.Exit(1)
	}
	mode := "已写出"
	if *dryRun {
		mode = "待写出（--dry-run，未写盘）"
	}
	fmt.Printf("capability export: %s %d 个文件 → %s，失败 %d 个\n", mode, stats.Written, root, stats.Failed)
	if stats.Failed > 0 {
		os.Exit(1)
	}
}

// capabilityRoot 解析能力目录：--dir 优先，否则取配置（MATE_CAPABILITIES / ~/.ark-mate/capabilities）。
func capabilityRoot(cfg config.Config, flagDir string) string {
	if root := strings.TrimSpace(flagDir); root != "" {
		return root
	}
	return cfg.CapabilityDir
}

// legacySource 旧 `sources[]` 条目里平台关心的两段（其余字段忽略）。
type legacySource struct {
	Runtime string `json:"runtime"`
	Path    string `json:"path"`
	Format  string `json:"format"`
}

// skillMigration 一个 skill 定义文件的迁移计划。
//
// 干跑与实跑**共用同一份计划对象**（含将要写入的确切字节 `Next`）：这样「--dry-run 的输出 ==
// 实跑的结果」不是靠两处代码写得一样来保证，而是结构上不可能不一致。
type skillMigration struct {
	// Path 定义文件绝对路径。
	Path string
	// Skip 无 `sources` 键 → 无需改动（幂等的落点：已迁移过的文件连 mtime 都不动）。
	Skip bool
	// Sources 将被丢弃的旧条目（逐条打印：底座目录/格式现在由 runtime 的投影声明回答，
	// 用户需要知道到底丢了什么，才不会以为平台悄悄改了他的意图）。
	Sources []legacySource
	// Runtimes 迁移后的 runtimes（去重升序）。为空 = 不写该键（语义仍是"全部"）。
	Runtimes []string
	// Next 将要写入的确切字节（由 capability.DefinitionFile.Marshal 产出，与 Save 同源）。
	Next []byte

	file *capability.DefinitionFile
}

// apply 落盘（C9 纪律由 capability.DefinitionFile 承担）。
func (m *skillMigration) apply() error { return m.file.Save() }

// capabilityMigrate 把 skill 定义里的旧 `sources[]` 迁移成 `runtimes[]`。
//
// 为什么必须手跑一次：M1 起条目侧只声明「给谁用」（`runtimes`），而 `sources` 是个**未知键**
// ——`encoding/json` 会静默丢掉它。丢掉的后果不是报错，而是**投放面被放宽**：原本「只给某一个
// 底座」的技能变成「给所有支持 skill 投影的 runtime」。所以这不是清理性迁移，而是
// **保持语义不变**的一次改写；不迁移也能跑，但行为与迁移前不同（装载期会为这类文件挂一条
// `legacy_sources_key` 告警，把人引到这里）。
func capabilityMigrate(args []string) {
	fs := flag.NewFlagSet("capability migrate", flag.ExitOnError)
	dir := fs.String("dir", "", "能力目录（默认 MATE_CAPABILITIES 或 ~/.ark-mate/capabilities）")
	dryRun := fs.Bool("dry-run", false, "只打印将要写入的内容，不改任何文件")
	_ = fs.Parse(args)

	cfg, _ := config.MustLoad()
	root := strings.TrimSpace(*dir)
	if root == "" {
		root = cfg.CapabilityDir
	}
	if root == "" {
		fmt.Fprintln(os.Stderr, "capability migrate: 能力目录为空（用 --dir 指定，或设置 MATE_CAPABILITIES）")
		os.Exit(2)
	}
	res := migrateSkillsDir(root, *dryRun, os.Stdout)
	if res.Failed > 0 {
		os.Exit(1)
	}
}

// migrateStats 一次迁移的计数（摘要行用）。
type migrateStats struct {
	Migrated int
	Skipped  int
	Failed   int
}

// migrateSkillsDir 迁移 <root>/skills 下的全部定义，人类可读输出写 out。
//
// 与 CLI 拆开是为了可测：CLI 那层只做「解析配置 → 调它 → 定退出码」，
// 而 `os.Exit` 与 stdout 的耦合不会渗进迁移逻辑本身。
func migrateSkillsDir(root string, dryRun bool, out io.Writer) migrateStats {
	skillsDir := filepath.Join(root, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(out, "capability migrate: %s 不存在，没有可迁移的定义（无需操作）\n", skillsDir)
			return migrateStats{}
		}
		fmt.Fprintf(out, "capability migrate: 读取 %s 失败: %v\n", skillsDir, err)
		return migrateStats{Failed: 1}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // 遍历顺序稳定（输出可复现，干跑与实跑可比）

	var st migrateStats
	for _, name := range names {
		path := filepath.Join(skillsDir, name)
		m, err := planSkillMigration(path)
		if err != nil {
			fmt.Fprintf(out, "  ✗ %s: %v\n", name, err)
			st.Failed++
			continue
		}
		if m.Skip {
			st.Skipped++
			continue
		}
		for _, src := range m.Sources {
			fmt.Fprintf(out, "  · %s 丢弃 source runtime=%s path=%q format=%q\n",
				name, src.Runtime, src.Path, src.Format)
		}
		if len(m.Runtimes) == 0 {
			fmt.Fprintf(out, "  · %s 的 sources 没有任何 runtime（迁移后不写 runtimes = 全部 runtime）\n", name)
		}
		if dryRun {
			fmt.Fprintf(out, "  ── %s（--dry-run，未写盘）\n%s", m.Path, indentBlock(string(m.Next)))
			st.Migrated++
			continue
		}
		if err := m.apply(); err != nil {
			fmt.Fprintf(out, "  ✗ %s: %v\n", name, err)
			st.Failed++
			continue
		}
		fmt.Fprintf(out, "  ✓ %s → runtimes=%v\n", m.Path, m.Runtimes)
		st.Migrated++
	}
	mode := "已迁移"
	if dryRun {
		mode = "待迁移（--dry-run，未写盘）"
	}
	fmt.Fprintf(out, "capability migrate: %s %d 个，跳过（无 sources）%d 个，失败 %d 个\n",
		mode, st.Migrated, st.Skipped, st.Failed)
	return st
}

// planSkillMigration 为单个定义文件算出迁移计划（只读，不写盘）。
func planSkillMigration(path string) (*skillMigration, error) {
	f, err := capability.ReadDefinitionFile(path)
	if err != nil {
		return nil, err
	}
	m := &skillMigration{Path: path, file: f}
	if !f.Has("sources") {
		m.Skip = true
		return m, nil
	}
	if _, err := f.Decode("sources", &m.Sources); err != nil {
		return nil, err
	}
	m.Runtimes = dedupeSortedRuntimes(m.Sources)
	f.Delete("sources")
	if len(m.Runtimes) > 0 {
		if err := f.Set("runtimes", m.Runtimes); err != nil {
			return nil, err
		}
	}
	// Marshal 与 Save 同源：干跑打印的就是实跑会写进去的字节。
	m.Next, err = f.Marshal()
	if err != nil {
		return nil, err
	}
	return m, nil
}

// dedupeSortedRuntimes 把 sources 的 runtime 去重升序（空项丢弃）。
//
// 去重升序而不是保序：旧文件里同一 runtime 出现多次是常见写法（一条 source 对一个 path），
// 保序会让迁移结果依赖书写顺序，同一个技能在两台机器上迁移出不同的 `runtimes` 数组。
func dedupeSortedRuntimes(sources []legacySource) []string {
	seen := make(map[string]bool, len(sources))
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		rt := strings.TrimSpace(s.Runtime)
		if rt == "" || seen[rt] {
			continue
		}
		seen[rt] = true
		out = append(out, rt)
	}
	sort.Strings(out)
	return out
}

// indentBlock 给多行文本统一加四格缩进（dry-run 输出与上下文区分开）。
func indentBlock(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n") + "\n"
}
