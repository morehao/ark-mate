package command

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/morehao/ark-mate/internal/manager"
	"github.com/morehao/ark-mate/internal/store"
)

// providerCmd 分发 `mate provider <动作>`（v11：供应商档案的存量搬家，与 `mate agent` 同形）。
//
// 目录形状不同是这两个命令唯一值得对比的地方：agent 是"每定义一个文件"，provider 是
// `<AgentDir>/providers.json` **一个文件**装全部档案；导出也只有这一个文件。
//
// 回退窗口（v11 迁移后关闭）的唯一回灌路径就是 `mate provider export`——所以它必须存在，
// 且必须确定化输出（导出两次逐字节相同）。
func providerCmd(args []string) {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, providerUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "import":
		providerImport(args[1:])
	case "export":
		providerExport(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知的 provider 动作 %q\n\n%s", args[0], providerUsage)
		os.Exit(2)
	}
}

const providerUsage = `provider 用法：
  mate provider import [--dir <Agent 目录>] [--dry-run]
      把 <Agent 目录>/providers.json 导入 SQLite（v11：sqlite 模式下 DB 才是档案事实源）
      幂等：按行判重，**已有行绝不覆盖**；旧明文密钥走 vault 加密（只留 key_ref）
      迁移前备份 providers.json.bak-v11（已存在则不覆盖）；文件**不删不回写**
      对账不一致只告警（落 meta.providers_import_mismatch，mate doctor 可查），不阻断
  mate provider export [--dir <Agent 目录>] [--dry-run]
      把 SQLite 里的档案写回 providers.json（此后文件是**导出物**，不是事实源）；
      这是 v11 迁移后回退旧二进制的唯一回灌路径
`

// providerImport 显式导入存量档案（`mate provider import`）。
//
// 与装配期自动导入的分工：自动导入看文件内容指纹（内容没变就整体跳过），显式导入**不看指纹**、
// 每次按行判重。
func providerImport(args []string) {
	fs := flag.NewFlagSet("provider import", flag.ExitOnError)
	dir := fs.String("dir", "", "Agent 目录（默认 MATE_AGENTS 或 ~/.ark-mate/agents）")
	dryRun := fs.Bool("dry-run", false, "只扫描与校验，不写库也不写 vault")
	_ = fs.Parse(args)

	cfg, _ := mustConfig()
	root := agentRoot(cfg, *dir)
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "provider import: 打开持久层 %s 失败: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	stats, err := manager.ImportProviderFile(context.Background(), st, root,
		manager.DefinitionImportOptions{DryRun: *dryRun}, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "provider import: %v\n", err)
		os.Exit(1)
	}
	verb := "新增"
	if *dryRun {
		verb = "将新增"
	}
	if stats.Already {
		fmt.Printf("provider import: %s 的内容指纹未变（装配期已导入过）；要重跑显式导入就用本次这条命令\n", root)
	}
	fmt.Printf("provider import: 扫描档案 %d 条 → %s；库中已有 %d 条（不覆盖），%s %d 条\n",
		stats.Providers, cfg.DBPath, stats.Existing, verb, stats.Inserted)
	if stats.Backup != "" {
		fmt.Printf("  已备份迁移前的文件：%s\n", stats.Backup)
	}
	if len(stats.SkippedKeys) > 0 {
		fmt.Printf("  ⚠ %d 条密钥未导入（vault 不可用，明文**不再有任何落点**）：\n", len(stats.SkippedKeys))
		for _, k := range stats.SkippedKeys {
			fmt.Printf("      %s\n", k)
		}
		fmt.Printf("      修好 vault（MATE_VAULT_KEY 或 <mate home>/vault.key）后重跑本命令\n")
	}
	if len(stats.Mismatch) > 0 {
		fmt.Printf("  ⚠ 导入对账不一致 %d 条（不阻断，`mate doctor --json` 可查）：\n", len(stats.Mismatch))
		for _, m := range stats.Mismatch {
			fmt.Printf("      %s\n", m)
		}
	}
}

// providerExport 把 DB 里的档案写回 `<AgentDir>/providers.json`。
func providerExport(args []string) {
	fs := flag.NewFlagSet("provider export", flag.ExitOnError)
	dir := fs.String("dir", "", "Agent 目录（默认 MATE_AGENTS 或 ~/.ark-mate/agents）")
	dryRun := fs.Bool("dry-run", false, "只打印将要写出的内容，不改任何文件")
	_ = fs.Parse(args)

	cfg, _ := mustConfig()
	root := agentRoot(cfg, *dir)
	if root == "" {
		fmt.Fprintln(os.Stderr, "provider export: Agent 目录为空（用 --dir 指定，或设置 MATE_AGENTS）")
		os.Exit(2)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "provider export: 打开持久层 %s 失败: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	stats, err := manager.ExportProviderFile(context.Background(), st, root, *dryRun, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "provider export: %v\n", err)
		os.Exit(1)
	}
	if !*dryRun {
		fmt.Printf("provider export: 已写出 %d 条档案 → %s\n", stats.Written, root)
	}
	if stats.Failed > 0 {
		os.Exit(1)
	}
}
