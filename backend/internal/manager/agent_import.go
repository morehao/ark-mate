package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
)

// ——— 存量 agent 定义的一次性导入与导出（M6-6 下半） ———
//
// 与能力面（M6-4）同构的三条路径，只是目录形状不同：
//
//	装配期（sqlite 模式）自动导入一次：目录指纹进 `meta.imported_agents`，目录没变就跳过；
//	`mate agent import`：显式重跑，每次按行判重（新丢进目录一个文件，跑一次就该进来）；
//	`mate agent export`：把 DB 里的定义写回目录（文件从此是**导出物**，不再是事实源）。
//
// 目录形状：`<AgentDir>/*.json`（**扁平**，与 `LoadAgentDefinitions` 的既有约定一致），
// 其中两个文件**不是 agent 定义**、必须跳过：
//
//	providers.json —— provider 档案（P-9：这张仍留在文件侧，不进库）
//	default.json   —— 「当前 Agent」的显式选择（P-8：进 meta.default_agent）
//
// 跳过而不是报错：它们是这个目录的**正常住户**，报错会让"每次启动一条 WARN"变成噪音。

// AgentImportStats 一次 agent 定义导入的计数 = 域无关计数 + 本域的 Agents 条数。
type AgentImportStats struct {
	DefinitionImportStats
	// Agents 目录里**过了闸门**的条数。
	Agents int
	// DefaultAgent 本次从 `default.json` 采纳的显式选择（空 = 没有/已设置过，不动）。
	DefaultAgent string
}

// ImportAgentDir 从目录导入存量 agent 定义到持久层（sqlite 模式专用）。
//
// 逐条过闸门（JSON 可解析 + id 合法且非保留前缀）：一手坏定义不该让整次导入失败（其余照常进来），
// 但必须**逐条可见**（统计 + `out` 里的行）。
//
// **runtime 是否存在不在导入期判定**：那是装载期的事（`applyDefinition` 校验 runtime 已注册），
// 而且"暂时没注册"不等于"这条定义该丢"——定义留着、runtime 装回来就生效，这与文件模式逐字一致。
func ImportAgentDir(ctx context.Context, st *store.Store, dir string, opts DefinitionImportOptions, out io.Writer) (AgentImportStats, error) {
	var stats AgentImportStats
	if st == nil {
		return stats, errors.New("manager: 导入 agent 定义需要 sqlite 持久层（MATE_STORE=sqlite）")
	}
	dir = strings.TrimSpace(dir)
	stats.Dir = dir
	if dir == "" {
		// 没配目录 = 没有存量文件要搬。不是错误（sqlite 模式的写面本来就不需要目录）。
		return stats, nil
	}
	items, scanStats := scanAgentDir(dir, out)
	stats = scanStats
	if opts.DryRun {
		return stats, countDryRun(ctx, st, items, &stats.DefinitionImportStats)
	}
	marker := ""
	if opts.Auto {
		marker = stats.Marker
	}
	if err := importDefinitionRows(ctx, st, items, model.MetaKeyImportedAgents, marker,
		&stats.DefinitionImportStats, "agent", out); err != nil {
		return stats, err
	}
	if stats.Already {
		return stats, nil
	}
	// `default.json` → meta：**只在 meta 还没被设置时**采纳（用户在面板上改过的显式选择优先，
	// 而目录里那份可能还是几个月前的老值）。它与定义导入同一次触发，因为它描述的就是"这批定义
	// 里当前用哪个"。
	id, err := importAgentDefaultSelection(ctx, st, dir, out)
	if err != nil {
		return stats, err
	}
	stats.DefaultAgent = id
	return stats, nil
}

// importAgentDefaultSelection 把 `<dir>/default.json` 的 `default_agent` 采纳进 meta（未设置时）。
func importAgentDefaultSelection(ctx context.Context, st *store.Store, dir string, out io.Writer) (string, error) {
	path := filepath.Join(dir, defaultAgentFile)
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("agent: 读 default.json 失败", "path", path, "err", err)
		}
		return "", nil
	}
	var doc struct {
		DefaultAgent string `json:"default_agent"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		slog.Warn("agent: default.json 不是合法 JSON，已跳过", "path", path, "err", err)
		return "", nil
	}
	id := strings.TrimSpace(doc.DefaultAgent)
	if id == "" {
		return "", nil
	}
	if _, ok, err := st.GetDefaultAgentID(ctx); err != nil {
		return "", err
	} else if ok {
		// 库里的显式选择已经存在（用户在面板上改过）→ 目录里那份不再有话语权。
		return "", nil
	}
	if err := st.SetDefaultAgentID(ctx, id); err != nil {
		return "", err
	}
	if out != nil {
		fmt.Fprintf(out, "agent import: 采纳 default.json 的显式选择 %q\n", id)
	}
	return id, nil
}

// ExportAgentDir 把持久层里的 agent 定义写回目录（文件从此是导出物）。
//
// 正文以 `agentDefinitionFile` 往返归一一次（手工写进行里的 body 也能导出成写面同形的文件），
// 写盘走 `capability.WriteDefinitionFile`；`meta.default_agent` 有值时一并写成 `default.json`
// ——导出物的用途是"接管/迁移/离线审阅"，缺了当前选择就不完整。
func ExportAgentDir(ctx context.Context, st *store.Store, dir string, dryRun bool, out io.Writer) (DefinitionExportStats, error) {
	stats := DefinitionExportStats{Dir: strings.TrimSpace(dir), DryRun: dryRun}
	if st == nil {
		return stats, errors.New("manager: 导出 agent 定义需要 sqlite 持久层（MATE_STORE=sqlite）")
	}
	if stats.Dir == "" {
		return stats, errors.New("manager: 导出 agent 定义需要 AgentDir（--dir 或 MATE_AGENTS）")
	}
	rows, err := st.ListAgentDefinitions(ctx)
	if err != nil {
		return stats, err
	}
	outRows := make([]exportRow, 0, len(rows)+1)
	for i := range rows {
		rel := rows[i].ID + ".json"
		if !validDefinitionID(rows[i].ID) || agentReservedFile(rel) {
			// 行 id 非法/与目录里的保留文件同名：**不写**（否则会覆盖 providers.json 这类文件）。
			stats.Failed++
			if out != nil {
				fmt.Fprintf(out, "  ✗ %s: agent id 非法或与保留文件同名，未导出\n", rel)
			}
			continue
		}
		var d agentDefinitionFile
		if err := json.Unmarshal([]byte(rows[i].Body), &d); err != nil {
			stats.Failed++
			if out != nil {
				fmt.Fprintf(out, "  ✗ %s: %v\n", rel, err)
			}
			continue
		}
		d.ID = rows[i].ID
		body, err := agentBody(d)
		if err != nil {
			stats.Failed++
			if out != nil {
				fmt.Fprintf(out, "  ✗ %s: %v\n", rel, err)
			}
			continue
		}
		outRows = append(outRows, exportRow{Label: rel, Path: filepath.Join(stats.Dir, rel), Body: []byte(body)})
	}
	if id, ok, err := st.GetDefaultAgentID(ctx); err != nil {
		return stats, err
	} else if ok {
		body := fmt.Sprintf("{\n  \"default_agent\": %q\n}\n", id)
		outRows = append(outRows, exportRow{
			Label: defaultAgentFile, Path: filepath.Join(stats.Dir, defaultAgentFile), Body: []byte(body),
		})
	}
	writeExportRows(outRows, dryRun, &stats, "agent", out)
	return stats, nil
}

// scanAgentDir 扫描 `<AgentDir>/*.json`：逐条解码 + 过闸门，返回可导入的条目与统计。
//
// 与 `LoadAgentDefinitions` 的关系：**同一个目录、同一道闸门**（id 形状 + 保留前缀），
// 但这里的产物是"要写进 DB 的正文"，而不是内存注册表条目——导入完成后由装载层从 DB 读回来，
// 装载路径只有一条。
func scanAgentDir(dir string, out io.Writer) ([]store.DefinitionImportItem, AgentImportStats) {
	stats := AgentImportStats{DefinitionImportStats: DefinitionImportStats{Dir: dir}}
	items := make([]store.DefinitionImportItem, 0, 8)
	files := globSorted(dir)
	kept := make([]string, 0, len(files))
	for _, path := range files {
		base := filepath.Base(path)
		if agentReservedFile(base) || strings.HasPrefix(base, ".") {
			continue // providers.json / default.json / 临时文件：不是 agent 定义
		}
		kept = append(kept, path)
		body, id, err := readAgentDefinitionFile(path)
		if err != nil {
			stats.Invalid++
			warnFile(out, "agent", path, err)
			continue
		}
		items = append(items, store.DefinitionImportItem{Kind: model.CapabilityKindAgent, ID: id, Body: body})
		stats.Agents++
	}
	stats.Marker = fingerprintFiles(kept)
	return items, stats
}

// readAgentDefinitionFile 读一个 agent 定义文件并归一（返回正文与 id）。
func readAgentDefinitionFile(path string) (string, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var d agentDefinitionFile
	if err := json.Unmarshal(b, &d); err != nil {
		return "", "", err
	}
	if d.ID == "" {
		d.ID = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	// 保留前缀（`agent-`）与内置种子 id 都**不能**作为行进来：`seedAgent` 只看 id 就判定只读，
	// 一行 `agent-x` 会变成"删不掉、也改不了 runtime"的幽灵定义。`dsh`/`opencode` 同理属于种子
	// 身份——想覆盖它们就在**文件里**写同名定义（那条路径的语义是"对种子的覆盖"），不要绕过
	// 写面直接塞一行。
	if !validDefinitionID(d.ID) || strings.HasPrefix(d.ID, reservedAgentPrefix) || isBuiltinAgent(d.ID) {
		return "", "", fmt.Errorf("agent id %q 不能作为定义行（不得是保留前缀 %q 或内置种子名）",
			d.ID, reservedAgentPrefix)
	}
	// 正文归一：手工写的文件（少缩进/多空白）进来也是写面同形的字节，导出两次结果相同。
	body, err := agentBody(d)
	if err != nil {
		return "", "", err
	}
	return body, d.ID, nil
}

// agentReservedFile 目录里的**保留文件**（不是 agent 定义）。
func agentReservedFile(base string) bool {
	return base == defaultAgentFile || base == providersFileName
}

// fingerprintFiles 目录指纹（文件名 + 大小，稳定序）——导入幂等标记。
//
// 与 `fingerprintCapabilityFiles` 同一套口径（只改内容不改大小的编辑可能漏判，后果只是"这次没
// 自动导入"，显式 `mate agent import` 随时可补），但目录更扁平，所以直接收文件列表。
func fingerprintFiles(files []string) string {
	sorted := append([]string{}, files...)
	sort.Strings(sorted)
	return fingerprintPaths(sorted)
}
