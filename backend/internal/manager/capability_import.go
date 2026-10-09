package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-forge/internal/model"
	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/ark-forge/pkg/capability"
)

// ——— 存量能力定义的一次性导入与导出（M6-4） ———
//
// 事实源换到 DB 之后，"定义还在文件里的那份存量"必须有一次明确的搬家动作，而且**只能有一次**
// ——否则用户会遇到"我删掉的技能又回来了"。据此：
//
//	装配期（sqlite 模式）自动导入一次：目录指纹进 `meta.imported_capabilities`，目录没变就跳过；
//	`forge capability import`：显式重跑，每次按行判重（新丢进目录一个文件，跑一次就该进来）；
//	`forge capability export`：把 DB 里的定义写回目录（文件从此是**导出物**，不再是事实源）。
//
// 三条都属于**运维动作**，刻意不加 REST 路由（C1 的双向比对开销与新的对外面都不值当）。

// CapabilityImportStats 一次导入的完整计数 = 域无关计数（DefinitionImportStats）
// + 能力面自己的分域计数（Skills / Mcps）。
type CapabilityImportStats struct {
	DefinitionImportStats
	// Skills / Mcps 目录里**过了闸门**的条数。
	Skills int
	Mcps   int
}

// ImportCapabilityDir 从目录导入存量能力定义到持久层（sqlite 模式专用）。
//
// 逐条过 `capability.ValidateSkill`/`ValidateMcpServer` 闸门（与装载、写面同一道门）：
// 一手坏定义不该让整次导入失败（其余照常进来），但必须**逐条可见**——统计 + `out` 里的行。
func ImportCapabilityDir(ctx context.Context, st *store.Store, dir string, opts DefinitionImportOptions, out io.Writer) (CapabilityImportStats, error) {
	var stats CapabilityImportStats
	if st == nil {
		return stats, errors.New("manager: 导入能力定义需要 sqlite 持久层（FORGE_STORE=sqlite）")
	}
	dir = strings.TrimSpace(dir)
	stats.Dir = dir
	if dir == "" {
		// 没配目录 = 没有存量文件要搬。不是错误（sqlite 模式的写面本来就不需要目录）。
		return stats, nil
	}
	items, scanStats := scanCapabilityDir(dir, out)
	stats = scanStats
	if opts.DryRun {
		return stats, countDryRun(ctx, st, items, &stats.DefinitionImportStats)
	}
	marker := ""
	if opts.Auto {
		marker = stats.Marker
	}
	err := importDefinitionRows(ctx, st, items, model.MetaKeyImportedCapabilities, marker,
		&stats.DefinitionImportStats, "capability", out)
	return stats, err
}

// ExportCapabilityDir 把持久层里的能力定义写回目录（文件从此是导出物）。
//
// 正文**逐字节取自 DB**（`Marshal` 语义复用：键排序 + 2 空格缩进 + 末尾换行），未知键因此原样
// 落在文件里；写盘走 `capability.WriteDefinitionFile`（同目录 staging + 原子 rename + 权限沿用）。
func ExportCapabilityDir(ctx context.Context, st *store.Store, dir string, dryRun bool, out io.Writer) (DefinitionExportStats, error) {
	stats := DefinitionExportStats{Dir: strings.TrimSpace(dir), DryRun: dryRun}
	if st == nil {
		return stats, errors.New("manager: 导出能力定义需要 sqlite 持久层（FORGE_STORE=sqlite）")
	}
	if stats.Dir == "" {
		return stats, errors.New("manager: 导出能力定义需要能力目录（--dir 或 FORGE_CAPABILITIES）")
	}
	skills, err := st.ListSkillDefinitions(ctx)
	if err != nil {
		return stats, err
	}
	mcps, err := st.ListMcpServerDefinitions(ctx)
	if err != nil {
		return stats, err
	}
	type row struct {
		sub  string
		id   string
		body string
	}
	rows := make([]row, 0, len(skills)+len(mcps))
	for i := range skills {
		rows = append(rows, row{sub: capSubSkills, id: skills[i].ID, body: skills[i].Body})
	}
	for i := range mcps {
		rows = append(rows, row{sub: capSubMcps, id: mcps[i].ID, body: mcps[i].Body})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].sub != rows[j].sub {
			return rows[i].sub < rows[j].sub
		}
		return rows[i].id < rows[j].id
	})

	// 逐条经 DefinitionFile 归一一次（DB 里的 body 可能是手工写进去的：少缩进/带 BOM），
	// 导出物必须与写面产出的文件**逐字节同形**（幂等：导出两次结果相同）。
	outRows := make([]exportRow, 0, len(rows))
	for _, r := range rows {
		rel := r.sub + "/" + r.id + ".json"
		path := filepath.Join(stats.Dir, r.sub, r.id+".json")
		doc, err := capability.DecodeDefinitionDoc(path, []byte(r.body))
		if err != nil {
			stats.Failed++
			if out != nil {
				fmt.Fprintf(out, "  ✗ %s: %v\n", rel, err)
			}
			continue
		}
		body, err := doc.Marshal()
		if err != nil {
			stats.Failed++
			if out != nil {
				fmt.Fprintf(out, "  ✗ %s: %v\n", rel, err)
			}
			continue
		}
		outRows = append(outRows, exportRow{Label: rel, Path: path, Body: body})
	}
	writeExportRows(outRows, dryRun, &stats, "capability", out)
	return stats, nil
}

// scanCapabilityDir 扫描能力目录：逐条解码 + 过闸门，返回可导入的条目与统计。
//
// 与 `Registry.LoadDir` 的关系：**同一个目录、同一道闸门**，但这里的产物是"要写进 DB 的正文"，
// 而不是内存注册表条目——导入完成后由装载层从 DB 读回来，装载路径只有一条。
func scanCapabilityDir(dir string, out io.Writer) ([]store.DefinitionImportItem, CapabilityImportStats) {
	stats := CapabilityImportStats{DefinitionImportStats: DefinitionImportStats{Dir: dir}}
	items := make([]store.DefinitionImportItem, 0, 8)

	skillFiles := globSorted(filepath.Join(dir, capSubSkills))
	for _, path := range skillFiles {
		doc, err := capability.ReadDefinitionFile(path)
		if err != nil {
			stats.Invalid++
			warnFile(out, "skill", path, err)
			continue
		}
		body, err := doc.Marshal()
		if err != nil {
			stats.Invalid++
			warnFile(out, "skill", path, err)
			continue
		}
		var sk capability.Skill
		if err := json.Unmarshal(body, &sk); err != nil {
			stats.Invalid++
			warnFile(out, "skill", path, err)
			continue
		}
		if sk.ID == "" {
			sk.ID = strings.TrimSuffix(filepath.Base(path), ".json")
		}
		if err := capability.ValidateSkill(&sk); err != nil {
			stats.Invalid++
			warnFile(out, "skill", path, err)
			continue
		}
		items = append(items, store.DefinitionImportItem{
			Kind: model.CapabilityKindSkill, ID: sk.ID, Body: string(body),
		})
		stats.Skills++
	}

	mcpFiles := globSorted(filepath.Join(dir, capSubMcps))
	for _, path := range mcpFiles {
		doc, err := capability.ReadDefinitionFile(path)
		if err != nil {
			stats.Invalid++
			warnFile(out, "mcp", path, err)
			continue
		}
		body, err := doc.Marshal()
		if err != nil {
			stats.Invalid++
			warnFile(out, "mcp", path, err)
			continue
		}
		var m capability.McpServer
		if err := json.Unmarshal(body, &m); err != nil {
			stats.Invalid++
			warnFile(out, "mcp", path, err)
			continue
		}
		if m.ID == "" {
			m.ID = strings.TrimSuffix(filepath.Base(path), ".json")
		}
		if err := capability.ValidateMcpServer(&m); err != nil {
			stats.Invalid++
			warnFile(out, "mcp", path, err)
			continue
		}
		items = append(items, store.DefinitionImportItem{
			Kind: model.CapabilityKindMcp, ID: m.ID, Body: string(body),
		})
		stats.Mcps++
	}

	// tools 不迁（P-1），但**必须报数**：静默丢弃用户放在这里的文件是最坏的一种"成功"。
	stats.Tools = len(globSorted(filepath.Join(dir, "tools")))

	stats.Marker = fingerprintPaths(append(append([]string{}, skillFiles...), mcpFiles...))
	return items, stats
}

// globSorted `<dir>/*.json`（不存在 → 空；顺序稳定 → 输出与指纹可复现）。
func globSorted(dir string) []string {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		slog.Warn("capability: 扫描目录失败", "dir", dir, "err", err)
		return nil
	}
	sort.Strings(files)
	return files
}

// warnFile 一条坏定义：WARN 日志 + （有人看输出时）一行人话。
//
// `kind` 是**定义的域**（skill / mcp / agent），它同时是日志与输出的分类依据——不写死
// "capability"，否则 agent 的坏定义会以能力面的名义报出来（排查时先被误导一次）。
func warnFile(out io.Writer, kind, path string, err error) {
	slog.Warn("定义导入：跳过一条坏定义", "kind", kind, "file", path, "err", err)
	if out != nil {
		fmt.Fprintf(out, "  ✗ %s/%s: %v\n", kind, filepath.Base(path), err)
	}
}

// indentJSONBlock 给多行 JSON 统一加四格缩进（--dry-run 输出与上下文区分开）。
//
// 与 `command.indentBlock` 同一用途但**分属两层**（这里是可复用的导出路径，那边是 CLI 输出），
// 故各留一份而不是把 CLI 的输出助手提到领域层。
func indentJSONBlock(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// getDefinitionRow 读一条定义行是否存在（DryRun 统计用）。
func getDefinitionRow(ctx context.Context, st *store.Store, it store.DefinitionImportItem) (string, bool, error) {
	if it.Kind == model.CapabilityKindMcp {
		row, found, err := st.GetMcpServerDefinition(ctx, it.ID)
		if err != nil || !found {
			return "", found, err
		}
		return row.Body, true, nil
	}
	row, found, err := st.GetSkillDefinition(ctx, it.ID)
	if err != nil || !found {
		return "", found, err
	}
	return row.Body, true, nil
}
