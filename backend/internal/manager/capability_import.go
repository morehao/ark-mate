package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
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

// CapabilityImportOptions 导入的行为开关。
type CapabilityImportOptions struct {
	// Auto 装配期自动导入：幂等靠目录指纹（见 CapabilityImportStats.Marker），
	// 目录没变就整体跳过。显式 `forge capability import` 用 false（每次按行判重）。
	Auto bool
	// DryRun 只扫描与校验、不写库（与 `capability migrate --dry-run` 同口径）。
	DryRun bool
}

// CapabilityImportStats 一次导入的完整计数（人话输出与启动日志共用同一份事实）。
type CapabilityImportStats struct {
	// Dir 实际读取的能力目录（空 = 没配置，什么都没做）。
	Dir string
	// Marker 本次的目录指纹（Auto 模式写入 meta，作为下次的幂等依据）。
	Marker string
	// Skills / Mcps 目录里**过了闸门**的条数。
	Skills int
	Mcps   int
	// Invalid 读不了或过不了闸门的文件数（逐条 WARN，**不整体失败**）。
	Invalid int
	// Tools `<dir>/tools/*.json` 的条数（P-1：这张表不迁）。**计数不打折**：静默丢弃
	// 静态工具文件会表现为"我明明放了 /tools 里那个文件，它怎么没了"。
	Tools int
	// Inserted / Existing 真的插进库的行数 / 库里已有而按"绝不覆盖"保留的行数。
	Inserted int
	Existing int
	// Already true = 目录指纹没变，整体跳过（一个字节都没写）。
	Already bool
}

// CapabilityExportStats 一次导出的计数。
type CapabilityExportStats struct {
	Dir     string
	Written int // 写出的文件数（DryRun 下是"将要写出"的）
	Failed  int // 解码/写盘失败的文件数（逐条已报出）
	DryRun  bool
}

// ImportCapabilityDir 从目录导入存量能力定义到持久层（sqlite 模式专用）。
//
// 逐条过 `capability.ValidateSkill`/`ValidateMcpServer` 闸门（与装载、写面同一道门）：
// 一手坏定义不该让整次导入失败（其余照常进来），但必须**逐条可见**——统计 + `out` 里的行。
func ImportCapabilityDir(ctx context.Context, st *store.Store, dir string, opts CapabilityImportOptions, out io.Writer) (CapabilityImportStats, error) {
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
		for _, it := range items {
			if _, found, err := getDefinitionRow(ctx, st, it); err != nil {
				return stats, err
			} else if !found {
				stats.Inserted++
			} else {
				stats.Existing++
			}
		}
		return stats, nil
	}
	marker := ""
	if opts.Auto {
		marker = stats.Marker
	}
	res, err := st.ImportCapabilityDefinitions(ctx, items, marker)
	if errors.Is(err, store.ErrAlreadyImported) {
		stats.Already = true
		stats.Inserted, stats.Existing = 0, 0
		return stats, nil
	}
	if err != nil {
		return stats, err
	}
	stats.Inserted, stats.Existing = res.Inserted, res.Existing
	if out != nil && res.Inserted > 0 {
		fmt.Fprintf(out, "capability import: 导入 %d 条（%d 条已存在，按「导入绝不覆盖」保留）\n",
			res.Inserted, res.Existing)
	}
	return stats, nil
}

// ExportCapabilityDir 把持久层里的能力定义写回目录（文件从此是导出物）。
//
// 正文**逐字节取自 DB**（`Marshal` 语义复用：键排序 + 2 空格缩进 + 末尾换行），未知键因此原样
// 落在文件里；写盘走 `capability.WriteDefinitionFile`（同目录 staging + 原子 rename + 权限沿用）。
func ExportCapabilityDir(ctx context.Context, st *store.Store, dir string, dryRun bool, out io.Writer) (CapabilityExportStats, error) {
	stats := CapabilityExportStats{Dir: strings.TrimSpace(dir), DryRun: dryRun}
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

	for _, r := range rows {
		// 经 DefinitionFile 归一一次：DB 里的 body 可能是手工写进去的（不留神就少缩进/带 BOM），
		// 导出物必须与写面产出的文件**逐字节同形**（幂等：导出两次结果相同）。
		doc, err := capability.DecodeDefinitionDoc(filepath.Join(stats.Dir, r.sub, r.id+".json"), []byte(r.body))
		if err != nil {
			stats.Failed++
			if out != nil {
				fmt.Fprintf(out, "  ✗ %s/%s.json: %v\n", r.sub, r.id, err)
			}
			continue
		}
		body, err := doc.Marshal()
		if err != nil {
			stats.Failed++
			if out != nil {
				fmt.Fprintf(out, "  ✗ %s/%s.json: %v\n", r.sub, r.id, err)
			}
			continue
		}
		if dryRun {
			stats.Written++
			if out != nil {
				fmt.Fprintf(out, "  ── %s/%s.json（--dry-run，未写盘）\n%s", r.sub, r.id, indentJSONBlock(string(body)))
			}
			continue
		}
		if err := capability.WriteDefinitionFile(filepath.Join(stats.Dir, r.sub, r.id+".json"), body); err != nil {
			stats.Failed++
			fmt.Fprintf(os.Stderr, "capability export: 写出 %s/%s.json 失败: %v\n", r.sub, r.id, err)
			continue
		}
		stats.Written++
		if out != nil {
			fmt.Fprintf(out, "  ✓ %s/%s.json\n", r.sub, r.id)
		}
	}
	return stats, nil
}

// scanCapabilityDir 扫描能力目录：逐条解码 + 过闸门，返回可导入的条目与统计。
//
// 与 `Registry.LoadDir` 的关系：**同一个目录、同一道闸门**，但这里的产物是"要写进 DB 的正文"，
// 而不是内存注册表条目——导入完成后由装载层从 DB 读回来，装载路径只有一条。
func scanCapabilityDir(dir string, out io.Writer) ([]store.CapabilityImportItem, CapabilityImportStats) {
	stats := CapabilityImportStats{Dir: dir}
	items := make([]store.CapabilityImportItem, 0, 8)

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
		items = append(items, store.CapabilityImportItem{
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
		items = append(items, store.CapabilityImportItem{
			Kind: model.CapabilityKindMcp, ID: m.ID, Body: string(body),
		})
		stats.Mcps++
	}

	// tools 不迁（P-1），但**必须报数**：静默丢弃用户放在这里的文件是最坏的一种"成功"。
	stats.Tools = len(globSorted(filepath.Join(dir, "tools")))

	stats.Marker = fingerprintCapabilityFiles(skillFiles, mcpFiles)
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

// fingerprintCapabilityFiles 目录指纹（相对子目录 + 文件名 + 大小，稳定序）——导入幂等标记。
//
// 与 `fingerprintTranscripts` 同构（文件名 + 大小）：只改文件内容而不改大小的编辑可能漏判，
// 但漏判的后果只是"这次没自动导入"（显式 `forge capability import` 随时可补），
// 而把"目录没变"判成"变了"会让每次启动都重跑一遍导入（噪音 + 无意义的事务）。
func fingerprintCapabilityFiles(skillFiles, mcpFiles []string) string {
	h := sha256.New()
	for _, p := range append(append([]string{}, skillFiles...), mcpFiles...) {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		sub := filepath.Base(filepath.Dir(p))
		_, _ = fmt.Fprintf(h, "%s/%s:%d\n", sub, filepath.Base(p), fi.Size())
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// warnFile 一条坏定义：WARN 日志 + （有人看输出时）一行人话。
func warnFile(out io.Writer, kind, path string, err error) {
	slog.Warn("capability: 导入时跳过一条定义", "kind", kind, "file", path, "err", err)
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
func getDefinitionRow(ctx context.Context, st *store.Store, it store.CapabilityImportItem) (string, bool, error) {
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
