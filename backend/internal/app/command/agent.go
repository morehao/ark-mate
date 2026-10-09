package command

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/morehao/ark-mate/internal/manager"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/config"
)

// agentCmd 分发 `forge agent <动作>`（M6-6：agent 定义的存量搬家，与 `forge capability` 同形）。
//
// 只有 import / export 两个动作：agent 定义没有 `capability migrate` 那种历史键改写
// （它今天就是 typed JSON，没有过 `sources[]` 那种需要保持语义的迁移）。
func agentCmd(args []string) {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, agentUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "import":
		agentImport(args[1:])
	case "export":
		agentExport(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知的 agent 动作 %q\n\n%s", args[0], agentUsage)
		os.Exit(2)
	}
}

const agentUsage = `agent 用法：
  forge agent import [--dir <Agent 目录>] [--dry-run]
      把目录里的 <id>.json 导入 SQLite（M6：sqlite 模式下 DB 才是定义事实源）
      幂等：按行判重，**已有行绝不覆盖**；坏定义逐条报出、其余照常导入
      providers.json / default.json 是目录级配置，不参与导入（default.json 的显式选择进 meta）
  forge agent export [--dir <Agent 目录>] [--dry-run]
      把 SQLite 里的定义写回目录（此后文件是**导出物**，不是事实源）；
      meta 里的「当前 Agent」一并写成 default.json
`

// agentImport 显式导入存量 agent 定义（`forge agent import`）。
//
// 与装配期自动导入的分工：自动导入看目录指纹（目录没变就整体跳过），显式导入**不看指纹**、
// 每次按行判重——新丢进目录一个文件，跑一次就该进来。
func agentImport(args []string) {
	fs := flag.NewFlagSet("agent import", flag.ExitOnError)
	dir := fs.String("dir", "", "Agent 定义目录（默认 FORGE_AGENTS 或 ~/.forge/agents）")
	dryRun := fs.Bool("dry-run", false, "只扫描与校验，不写库")
	_ = fs.Parse(args)

	cfg, _ := mustConfig()
	root := agentRoot(cfg, *dir)
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent import: 打开持久层 %s 失败: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	stats, err := manager.ImportAgentDir(context.Background(), st, root,
		manager.DefinitionImportOptions{DryRun: *dryRun}, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent import: %v（未写入任何内容）\n", err)
		os.Exit(1)
	}
	verb := "新增"
	if *dryRun {
		verb = "将新增"
	}
	if stats.Already {
		fmt.Printf("agent import: %s 的目录指纹未变（装配期已导入过）；要重跑显式导入就用本次这条命令\n", root)
	}
	fmt.Printf("agent import: 扫描定义 %d 条 → %s；库中已有 %d 条（不覆盖），%s %d 条\n",
		stats.Agents, cfg.DBPath, stats.Existing, verb, stats.Inserted)
	if stats.DefaultAgent != "" {
		fmt.Printf("  已采纳 default.json 的显式选择：%s\n", stats.DefaultAgent)
	}
	if stats.Invalid > 0 {
		fmt.Printf("  跳过坏定义 %d 条（上面逐条列了原因）\n", stats.Invalid)
	}
	if stats.Tools > 0 {
		fmt.Printf("  ⚠ %s/tools 下有 %d 个文件：工具目录**不参与持久化**，本次未导入\n", root, stats.Tools)
	}
}

// agentExport 把 DB 里的定义写回目录（`forge agent export`）。
func agentExport(args []string) {
	fs := flag.NewFlagSet("agent export", flag.ExitOnError)
	dir := fs.String("dir", "", "Agent 定义目录（默认 FORGE_AGENTS 或 ~/.forge/agents）")
	dryRun := fs.Bool("dry-run", false, "只打印将要写出的内容，不改任何文件")
	_ = fs.Parse(args)

	cfg, _ := mustConfig()
	root := agentRoot(cfg, *dir)
	if root == "" {
		fmt.Fprintln(os.Stderr, "agent export: Agent 目录为空（用 --dir 指定，或设置 FORGE_AGENTS）")
		os.Exit(2)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent export: 打开持久层 %s 失败: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	stats, err := manager.ExportAgentDir(context.Background(), st, root, *dryRun, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent export: %v\n", err)
		os.Exit(1)
	}
	mode := "已写出"
	if *dryRun {
		mode = "待写出（--dry-run，未写盘）"
	}
	fmt.Printf("agent export: %s %d 个文件 → %s，失败 %d 个\n", mode, stats.Written, root, stats.Failed)
	if stats.Failed > 0 {
		os.Exit(1)
	}
}

// agentRoot 解析 Agent 定义目录：--dir 优先，否则取配置（FORGE_AGENTS / ~/.forge/agents）。
func agentRoot(cfg config.Config, flagDir string) string {
	if root := strings.TrimSpace(flagDir); root != "" {
		return root
	}
	return cfg.AgentDir
}
