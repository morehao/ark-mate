package cliops

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/morehao/ark-mate/internal/mechanism/definition"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/store"
)

// automationCmd 分发 `mate automation <动作>`（M6-7：定时任务定义的存量搬家，与 `mate agent` 同形）。
//
// 只有 import / export 两个动作：automation 定义没有 `capability migrate` 那种历史键改写。
// 注意一个文件里同时住着定义与运行态：导入时运行态键会按 `automation.ReservedKeys` 切进
// **运行态列**（不是"丢弃"），导出时再合成同一个文件——所以这条命令搬的是完整的一条定时任务。
func Automation(args []string) {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, automationUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "import":
		automationImport(args[1:])
	case "export":
		automationExport(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知的 automation 动作 %q\n\n%s", args[0], automationUsage)
		os.Exit(2)
	}
}

const automationUsage = `automation 用法：
  mate automation import [--dir <Automation 目录>] [--dry-run]
      把目录里的 <id>.json 导入 SQLite（M6-7：sqlite 模式下 DB 才是定时任务事实源）
      文件里的运行态键（last_fired_at / last_status / recent_runs / …）一并进运行态列
      幂等：按行判重，**已有行绝不覆盖**；坏定义逐条报出、其余照常导入
  mate automation export [--dir <Automation 目录>] [--dry-run]
      把 SQLite 里的定时任务写回目录（此后文件是**导出物**，不是事实源）
      定义键与运行态键合成同一个 <id>.json（与老文件载体的形态一致）
`

// automationImport 显式导入存量定时任务定义（`mate automation import`）。
//
// 与装配期自动导入的分工：自动导入看目录指纹（目录没变就整体跳过），显式导入**不看指纹**、
// 每次按行判重——新丢进目录一个文件，跑一次就该进来。
func automationImport(args []string) {
	fs := flag.NewFlagSet("automation import", flag.ExitOnError)
	dir := fs.String("dir", "", "Automation 定义目录（默认 MATE_AUTOMATIONS 或 ~/.ark-mate/automations）")
	dryRun := fs.Bool("dry-run", false, "只扫描与校验，不写库")
	_ = fs.Parse(args)

	cfg, _ := mustConfig()
	root := automationRoot(cfg, *dir)
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "automation import: 打开持久层 %s 失败: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	stats, err := automation.ImportAutomationDir(context.Background(), st, root,
		definition.DefinitionImportOptions{DryRun: *dryRun}, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "automation import: %v（未写入任何内容）\n", err)
		os.Exit(1)
	}
	verb := "新增"
	if *dryRun {
		verb = "将新增"
	}
	if stats.Already {
		fmt.Printf("automation import: %s 的目录指纹未变（装配期已导入过）；要重跑显式导入就用本次这条命令\n", root)
	}
	fmt.Printf("automation import: 扫描定义 %d 条 → %s；库中已有 %d 条（不覆盖），%s %d 条\n",
		stats.Automations, cfg.DBPath, stats.Existing, verb, stats.Inserted)
	if stats.Invalid > 0 {
		fmt.Printf("  跳过坏定义 %d 条（上面逐条列了原因）\n", stats.Invalid)
	}
}

// automationExport 把 DB 里的定时任务写回目录（`mate automation export`）。
func automationExport(args []string) {
	fs := flag.NewFlagSet("automation export", flag.ExitOnError)
	dir := fs.String("dir", "", "Automation 定义目录（默认 MATE_AUTOMATIONS 或 ~/.ark-mate/automations）")
	dryRun := fs.Bool("dry-run", false, "只打印将要写出的内容，不改任何文件")
	_ = fs.Parse(args)

	cfg, _ := mustConfig()
	root := automationRoot(cfg, *dir)
	if root == "" {
		fmt.Fprintln(os.Stderr, "automation export: Automation 目录为空（用 --dir 指定，或设置 MATE_AUTOMATIONS）")
		os.Exit(2)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "automation export: 打开持久层 %s 失败: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	stats, err := automation.ExportAutomationDir(context.Background(), st, root, *dryRun, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "automation export: %v\n", err)
		os.Exit(1)
	}
	mode := "已写出"
	if *dryRun {
		mode = "待写出（--dry-run，未写盘）"
	}
	fmt.Printf("automation export: %s %d 个文件 → %s，失败 %d 个\n", mode, stats.Written, root, stats.Failed)
	if stats.Failed > 0 {
		os.Exit(1)
	}
}

// automationRoot 解析定时任务定义目录：--dir 优先，否则取配置（MATE_AUTOMATIONS / ~/.ark-mate/automations）。
func automationRoot(cfg config.Config, flagDir string) string {
	if root := strings.TrimSpace(flagDir); root != "" {
		return root
	}
	return cfg.AutomationDir
}
