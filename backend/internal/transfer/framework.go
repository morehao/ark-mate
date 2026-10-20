package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/morehao/ark-mate/internal/capability"
	"github.com/morehao/ark-mate/internal/store"
)

// ——— 存量定义的导入/导出：**域无关**的那一半（M6-4 能力面、M6-6 agent 面） ———
//
// 各域自己回答"目录长什么样、什么算坏定义"（`scanCapabilityDir` / `scanAgentDir`），这里收口
// 三件**每多一个域就会多抄一遍**的事：
//
//  1. 幂等：目录指纹进 `meta.<markerKey>`，Auto 模式下指纹没变就整体跳过（一个字节都不写）
//     ——否则用户会看到"我删掉的技能又回来了"；
//  2. 绝不覆盖：按行 `CreateCAS`（在 store 里），这里只把"已存在"如实计数，**不**替用户做决定；
//  3. 导出：把行正文写回文件（同目录 staging + 原子 rename），单条失败逐条可见、不整体失败。

// DefinitionImportOptions 导入的行为开关（各域共用）。
type DefinitionImportOptions struct {
	// Auto 装配期自动导入：幂等靠目录指纹（见 DefinitionImportStats.Marker），目录没变就整体跳过。
	// 显式 `mate … import` 用 false（每次按行判重）。
	Auto bool
	// DryRun 只扫描与校验、不写库（与 `capability migrate --dry-run` 同口径）。
	DryRun bool
}

// DefinitionImportStats 一次导入的**域无关**计数（人话输出与启动日志共用同一份事实）。
type DefinitionImportStats struct {
	// Dir 实际读取的目录（空 = 没配置，什么都没做）。
	Dir string
	// Marker 本次的目录指纹（Auto 模式写入 meta，作为下次的幂等依据）。
	Marker string
	// Invalid 读不了或过不了闸门的文件数（逐条 WARN，**不整体失败**）。
	Invalid int
	// Tools `<dir>/tools/*.json` 的条数（P-1：这张表不迁）。**计数不打折**：静默丢弃静态工具
	// 文件会表现为"我明明放了 /tools 里那个文件，它怎么没了"。
	Tools int
	// Inserted / Existing 真的插进库的行数 / 库里已有而按"绝不覆盖"保留的行数。
	Inserted int
	Existing int
	// Already true = 目录指纹没变，整体跳过（一个字节都没写）。
	Already bool
}

// DefinitionExportStats 一次导出的计数（各域共用）。
type DefinitionExportStats struct {
	Dir     string
	Written int // 写出的文件数（DryRun 下是"将要写出"的）
	Failed  int // 解码/写盘失败的文件数（逐条已报出）
	DryRun  bool
}

// ImportDefinitionRows 执行一次导入（标记判据 + 计数），域无关。
//
// `label` 只出现在人话输出里（`capability import:` / `agent import:`）。
func ImportDefinitionRows(ctx context.Context, st *store.Store, items []store.DefinitionImportItem,
	markerKey, marker string, stats *DefinitionImportStats, label string, out io.Writer) error {
	res, err := st.ImportDefinitionRows(ctx, items, markerKey, marker)
	if errors.Is(err, store.ErrAlreadyImported) {
		stats.Already = true
		stats.Inserted, stats.Existing = 0, 0
		return nil
	}
	if err != nil {
		return err
	}
	stats.Inserted, stats.Existing = res.Inserted, res.Existing
	if out != nil && res.Inserted > 0 {
		fmt.Fprintf(out, "%s import: 导入 %d 条（%d 条已存在，按「导入绝不覆盖」保留）\n",
			label, res.Inserted, res.Existing)
	}
	return nil
}

// CountDryRun 统计"如果真导入会插入几条/已存在几条"（DryRun 不写库）。
func CountDryRun(ctx context.Context, st *store.Store, items []store.DefinitionImportItem, stats *DefinitionImportStats) error {
	for _, it := range items {
		if _, found, err := getDefinitionRow(ctx, st, it); err != nil {
			return err
		} else if !found {
			stats.Inserted++
		} else {
			stats.Existing++
		}
	}
	return nil
}

// FingerprintPaths 目录指纹（`子目录/文件名:大小`，稳定序）——导入幂等标记的唯一算法。
//
// 只取文件名 + 大小而不是内容哈希：与 `fingerprintTranscripts` 同构，代价是"只改内容不改大小的
// 编辑可能漏判"，但漏判的后果只是"这次没自动导入"（显式 `mate … import` 随时可补），
// 而把"目录没变"判成"变了"会让**每次启动**都重跑一遍导入（噪音 + 无意义的事务）。
func FingerprintPaths(paths []string) string {
	sorted := append([]string{}, paths...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, p := range sorted {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		sub := filepath.Base(filepath.Dir(p))
		_, _ = fmt.Fprintf(h, "%s/%s:%d\n", sub, filepath.Base(p), fi.Size())
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// ExportRow 一条待写出的定义（正文已由各域归一为"写出物"的字节）。
type ExportRow struct {
	// Label 人话输出的相对路径（如 `skills/alpha.json`）。
	Label string
	// Path 写盘目标（绝对路径；由调用方用导出目录拼出）。
	Path string
	// Body 归一后的正文（各域保证"导出两次逐字节相同"）。
	Body []byte
}

// WriteExportRows 写出一批定义文件（DryRun 只打印不写盘）。
//
// 写盘走 `capability.WriteDefinitionFile`（同目录 staging + 原子 rename + 已有文件权限沿用）——
// 它是"定义文件"这一类的通用写出器，能力面与 agent 面共用同一份落盘纪律。
func WriteExportRows(rows []ExportRow, dryRun bool, stats *DefinitionExportStats, label string, out io.Writer) {
	for _, r := range rows {
		if dryRun {
			stats.Written++
			if out != nil {
				fmt.Fprintf(out, "  ── %s（--dry-run，未写盘）\n%s", r.Label, IndentJSONBlock(string(r.Body)))
			}
			continue
		}
		if err := capability.WriteDefinitionFile(r.Path, r.Body); err != nil {
			// 写盘失败一律进 stderr（导出是运维动作，坏消息不能被"没人看 out"吞掉）。
			stats.Failed++
			fmt.Fprintf(os.Stderr, "%s export: 写出 %s 失败: %v\n", label, r.Label, err)
			continue
		}
		stats.Written++
		if out != nil {
			fmt.Fprintf(out, "  ✓ %s\n", r.Label)
		}
	}
}
