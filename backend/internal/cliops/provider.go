package cliops

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/morehao/ark-mate/internal/mechanism/credential"
	"github.com/morehao/ark-mate/internal/mechanism/definition"

	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/config"
)

// providerCmd 分发 `mate provider <动作>`（v11：供应商档案的存量搬家）。
//
// 目录形状：`<ProviderDir>/providers.json` **一个文件**装全部档案；导出也只有这一个文件。
//
// 回退窗口（v11 迁移后关闭）的唯一回灌路径就是 `mate provider export`——所以它必须存在，
// 且必须确定化输出（导出两次逐字节相同）。
func Provider(args []string) {
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
  mate provider import [--dir <provider 目录>] [--dry-run]
      把 <ProviderDir>/providers.json 导入 SQLite（v11：sqlite 模式下 DB 才是档案事实源）
      幂等：按行判重，**已有行绝不覆盖**；旧明文密钥直接写进库（providers.key 一列）
      密钥引用里的旧前缀在这里被显式翻译成 store:（档案文件不在库里，迁移改不到它们）
      迁移前备份 providers.json.bak-v11（已存在则不覆盖）；文件**不删不回写**
      导入后会把库按镜像对账（<MATE_HOME>/credentials.json）
      对账不一致只告警（落 meta.providers_import_mismatch，mate doctor 可查），不阻断
  mate provider export [--dir <provider 目录>] [--dry-run]
      把 SQLite 里的档案写回 providers.json（此后文件是**导出物**，不是事实源）；
      这是 v11 迁移后回退旧二进制的唯一回灌路径
`

// providerImport 显式导入存量档案（`mate provider import`）。
//
// 与装配期自动导入的分工：自动导入看文件内容指纹（内容没变就整体跳过），显式导入**不看指纹**、
// 每次按行判重。
func providerImport(args []string) {
	fs := flag.NewFlagSet("provider import", flag.ExitOnError)
	dir := fs.String("dir", "", "provider 目录（默认 MATE_PROVIDERS 或 ~/.ark-mate/providers）")
	dryRun := fs.Bool("dry-run", false, "只扫描与校验，不写库也不写托管凭据文档")
	_ = fs.Parse(args)

	cfg, _ := mustConfig()
	root := providerRoot(cfg, *dir)
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "provider import: 打开持久层 %s 失败: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	stats, err := provider.ImportProviderFile(context.Background(), st, root,
		definition.DefinitionImportOptions{DryRun: *dryRun}, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "provider import: %v\n", err)
		os.Exit(1)
	}
	// 导入把密钥写进了库 → 顺手把托管镜像按库对账（幂等合并；失败只告警，不改变导入结论：
	// 库已经写成功了，镜像是可重建副本）。
	if !*dryRun {
		if changed, sErr := credential.SyncCredentialMirrorFromStore(context.Background(), st, cfg.HomeDir, "mate provider import"); sErr != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ 托管凭据文档未按库对账（库是权威、键不受影响）：%v\n", sErr)
		} else if changed > 0 {
			fmt.Printf("  托管凭据文档已按库对账：%d 条（%s）\n", changed, credential.CredentialsPath(cfg.HomeDir))
		}
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
	if len(stats.KeyProblems) > 0 {
		fmt.Printf("  ⚠ %d 条密钥没搬进库：\n", len(stats.KeyProblems))
		for _, k := range stats.KeyProblems {
			fmt.Printf("      %s\n", k)
		}
		fmt.Printf("      库里已有的值不会被档案里的明文覆盖；要用档案里那份就在面板里重填一次\n")
	}
	if len(stats.Mismatch) > 0 {
		fmt.Printf("  ⚠ 导入对账不一致 %d 条（不阻断，`mate doctor --json` 可查）：\n", len(stats.Mismatch))
		for _, m := range stats.Mismatch {
			fmt.Printf("      %s\n", m)
		}
	}
}

// providerExport 把 DB 里的档案写回 `<ProviderDir>/providers.json`。
func providerExport(args []string) {
	fs := flag.NewFlagSet("provider export", flag.ExitOnError)
	dir := fs.String("dir", "", "provider 目录（默认 MATE_PROVIDERS 或 ~/.ark-mate/providers）")
	dryRun := fs.Bool("dry-run", false, "只打印将要写出的内容，不改任何文件")
	_ = fs.Parse(args)

	cfg, _ := mustConfig()
	root := providerRoot(cfg, *dir)
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

	stats, err := provider.ExportProviderFile(context.Background(), st, root, *dryRun, os.Stdout)
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

// providerRoot 解析 provider 目录：--dir 优先，否则取配置（MATE_PROVIDERS / ~/.ark-mate/providers）。
func providerRoot(cfg config.Config, flagDir string) string {
	if root := strings.TrimSpace(flagDir); root != "" {
		return root
	}
	return cfg.ProviderDir
}
