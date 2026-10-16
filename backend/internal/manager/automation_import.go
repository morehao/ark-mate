package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	automationdomain "github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/mechanism/definition"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/automation"
	"github.com/morehao/ark-mate/pkg/capability"
)

// ——— 存量定时任务定义的一次性导入与导出（M6-7 下半） ———
//
// 与能力面（M6-4）/ agent 面（M6-6）同构的三条路径，差别只在**一个文件装了两样东西**：
//
//	装配期（sqlite 模式）自动导入一次：目录指纹进 `meta.imported_automations`，目录没变就跳过；
//	`mate automation import`：显式重跑，每次按行判重；
//	`mate automation export`：把库里的定义写回目录（文件从此是**导出物**）。
//
// 同一个 `.json` 里既有用户写的定义，也有平台写回的运行态（`last_fired_at` / `recent_runs` / …）。
// 进库之后后者是**列**（Plan §7 P-10），所以导入要按 `automation.ReservedKeys` 把文件切成两半，
// 并且**同一个事务**写进去——否则会出现"定义进来了、运行史没进来"的半截状态。导出反向合成。
//
// 目录里没有保留文件（不像 `<AgentDir>` 有 `providers.json`/`default.json`），所以每个 `.json`
// 都是一条定义；点开头的临时文件跳过（C9 的 staging 文件就在同目录）。

// AutomationImportStats 一次 automation 导入的计数 = 域无关计数 + 本域的 Automations 条数。
type AutomationImportStats struct {
	definition.DefinitionImportStats
	// Automations 目录里**过了闸门**的条数。
	Automations int
}

// ImportAutomationDir 从目录导入存量定时任务定义到持久层（sqlite 模式专用）。
//
// 逐条过闸门（JSON 对象 + id 合法 + 运行态键类型正确）：一条坏定义不该让整次导入失败
// （其余照常进来），但必须**逐条可见**（统计 + `out` 里的行）。
//
// **schedule/target 的可执行性不在导入期判定**：那是装载期的事（`buildAutomationRec` 会把
// `loadErr` 留在列表里让用户看见），而"现在不可执行"不等于"这条定义该丢"——这与文件模式逐字一致。
func ImportAutomationDir(ctx context.Context, st *store.Store, dir string, opts definition.DefinitionImportOptions, out io.Writer) (AutomationImportStats, error) {
	var stats AutomationImportStats
	if st == nil {
		return stats, errors.New("manager: 导入 automation 定义需要 sqlite 持久层（MATE_STORE=sqlite）")
	}
	dir = strings.TrimSpace(dir)
	stats.Dir = dir
	if dir == "" {
		// 没配目录 = 没有存量文件要搬。不是错误（sqlite 模式的写面本来就不需要目录）。
		return stats, nil
	}
	items, scanStats := scanAutomationDir(dir, out)
	stats = scanStats
	if opts.DryRun {
		return stats, definition.CountDryRun(ctx, st, items, &stats.DefinitionImportStats)
	}
	marker := ""
	if opts.Auto {
		marker = stats.Marker
	}
	if err := definition.ImportDefinitionRows(ctx, st, items, model.MetaKeyImportedAutomations, marker,
		&stats.DefinitionImportStats, "automation", out); err != nil {
		return stats, err
	}
	return stats, nil
}

// ExportAutomationDir 把持久层里的定时任务写回目录（文件从此是导出物）。
//
// 每条行都经 `automationFileBody` 归一（定义键 + 运行态键合成一个文件），所以"导出两次逐字节
// 相同"与"导出 → 空库导入 → 再导出逐字节相同"成立；写盘走 `capability.WriteDefinitionFile`
// （同目录 staging + 原子 rename + 沿用已有文件权限）。
func ExportAutomationDir(ctx context.Context, st *store.Store, dir string, dryRun bool, out io.Writer) (definition.DefinitionExportStats, error) {
	stats := definition.DefinitionExportStats{Dir: strings.TrimSpace(dir), DryRun: dryRun}
	if st == nil {
		return stats, errors.New("manager: 导出 automation 定义需要 sqlite 持久层（MATE_STORE=sqlite）")
	}
	if stats.Dir == "" {
		return stats, errors.New("manager: 导出 automation 定义需要 AutomationDir（--dir 或 MATE_AUTOMATIONS）")
	}
	rows, err := st.ListAutomationDefinitions(ctx)
	if err != nil {
		return stats, err
	}
	outRows := make([]definition.ExportRow, 0, len(rows))
	for i := range rows {
		rel := rows[i].ID + ".json"
		if !definition.ValidDefinitionID(rows[i].ID) {
			// 行 id 非法（手工塞进去的）：**不写**——否则会造出一个下次装载就报错的文件名。
			stats.Failed++
			if out != nil {
				fmt.Fprintf(out, "  ✗ %s: automation id 非法，未导出\n", rel)
			}
			continue
		}
		body, err := automationFileBody(&rows[i])
		if err != nil {
			stats.Failed++
			if out != nil {
				fmt.Fprintf(out, "  ✗ %s: %v\n", rel, err)
			}
			continue
		}
		outRows = append(outRows, definition.ExportRow{Label: rel, Path: filepath.Join(stats.Dir, rel), Body: []byte(body)})
	}
	definition.WriteExportRows(outRows, dryRun, &stats, "automation", out)
	return stats, nil
}

// automationFileBody 把一行（定义正文 + 运行态列）合成**文件形态**的字节（导出与导入归一化共用）。
//
// 运行态键按 `pkg/automation.Automation` 的 JSON 形态写回，值用**强类型**（`[]automation.RunRecord`、
// 字符串时间）——`DefinitionFile.Set` 会把它 marshal 成紧凑 JSON 再统一缩进，所以导出物与老文件
// 载体的写回形态一致，且两次导出逐字节相同。
func automationFileBody(row *model.AutomationEntity) (string, error) {
	df, err := capability.DecodeDefinitionDoc(automationdomain.Locator(row.ID), []byte(row.Body))
	if err != nil {
		return "", err
	}
	rt, err := automationRuntimeStateOf(row)
	if err != nil {
		return "", err
	}
	if err := applyAutomationRuntime(df, rt); err != nil {
		return "", err
	}
	body, err := df.Marshal()
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// automationRuntimeStateOf 从一行取运行态（recent_runs 的 JSON 坏掉 → 报错，不静默丢运行史）。
func automationRuntimeStateOf(row *model.AutomationEntity) (store.AutomationRuntimeState, error) {
	rt := store.AutomationRuntimeState{
		LastFiredAt:         row.LastFiredAt,
		LastStatus:          row.LastStatus,
		LastReason:          row.LastReason,
		LastSessionID:       row.LastSessionID,
		ConsecutiveFailures: row.ConsecutiveFailures,
		RecentRuns:          row.RecentRuns,
	}
	if row.RecentRuns != "" && row.RecentRuns != "[]" {
		var runs []automation.RunRecord
		if err := json.Unmarshal([]byte(row.RecentRuns), &runs); err != nil {
			return rt, fmt.Errorf("recent_runs 不是合法运行史 JSON：%v", err)
		}
	}
	return rt, nil
}

// applyAutomationRuntime 把运行态写进文件文档（空值不写键：与"未触发过就什么都没有"一致）。
func applyAutomationRuntime(df *capability.DefinitionFile, rt store.AutomationRuntimeState) error {
	if rt.LastFiredAt != "" {
		if err := df.Set("last_fired_at", rt.LastFiredAt); err != nil {
			return err
		}
	}
	if rt.LastStatus != "" {
		if err := df.Set("last_status", rt.LastStatus); err != nil {
			return err
		}
	}
	if rt.LastReason != "" {
		if err := df.Set("last_reason", rt.LastReason); err != nil {
			return err
		}
	}
	if rt.LastSessionID != "" {
		if err := df.Set("last_session_id", rt.LastSessionID); err != nil {
			return err
		}
	}
	if rt.ConsecutiveFailures != 0 {
		if err := df.Set("consecutive_failures", rt.ConsecutiveFailures); err != nil {
			return err
		}
	}
	if rt.RecentRuns != "" && rt.RecentRuns != "[]" {
		var runs []automation.RunRecord
		if err := json.Unmarshal([]byte(rt.RecentRuns), &runs); err != nil {
			return fmt.Errorf("recent_runs 不是合法运行史 JSON：%v", err)
		}
		if err := df.Set("recent_runs", runs); err != nil {
			return err
		}
	}
	return nil
}

// scanAutomationDir 扫描 `<AutomationDir>/*.json`：逐条切分定义/运行态并过闸门。
func scanAutomationDir(dir string, out io.Writer) ([]store.DefinitionImportItem, AutomationImportStats) {
	stats := AutomationImportStats{DefinitionImportStats: definition.DefinitionImportStats{Dir: dir}}
	files := definition.GlobSorted(dir)
	items := make([]store.DefinitionImportItem, 0, len(files))
	kept := make([]string, 0, len(files))
	for _, path := range files {
		if strings.HasPrefix(filepath.Base(path), ".") {
			continue // staging/临时文件不是定义
		}
		kept = append(kept, path)
		body, id, rt, err := readAutomationDefinitionFile(path)
		if err != nil {
			stats.Invalid++
			definition.WarnFile(out, "automation", path, err)
			continue
		}
		items = append(items, store.DefinitionImportItem{
			Kind: model.DefinitionKindAutomation, ID: id, Body: body, AutomationRuntime: rt,
		})
		stats.Automations++
	}
	stats.Marker = definition.FingerprintPaths(kept)
	return items, stats
}

// readAutomationDefinitionFile 读一个定时任务定义文件并**切分**（定义正文 / 运行态 / id）。
//
// 文件里的保留键（`automation.ReservedKeys`）一律搬进运行态：不搬的话定义正文里就会留着
// `last_fired_at` 这种"写了也不生效"的键，正是 P-10 要结构性排除的东西。
func readAutomationDefinitionFile(path string) (string, string, *store.AutomationRuntimeState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", "", nil, fmt.Errorf("不是合法 JSON 对象：%v", err)
	}
	if doc == nil {
		return "", "", nil, errors.New("内容不是 JSON 对象")
	}
	rt, err := automationRuntimeFromDoc(doc)
	if err != nil {
		return "", "", nil, err
	}
	// id：文件里有就用文件里的（与 agent 面一致），否则用文件名。
	id := strings.TrimSuffix(filepath.Base(path), ".json")
	if raw, ok := doc["id"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", "", nil, fmt.Errorf(`字段 "id" 须为字符串：%v`, err)
		}
		if s = strings.TrimSpace(s); s != "" {
			id = s
		}
	}
	if !definition.ValidDefinitionID(id) {
		return "", "", nil, fmt.Errorf("automation id %q 不能作为定义键（文件名或 id 字段非法）", id)
	}
	// 定义正文 = 原文去掉保留键，再把 id 钉成载体键（写面也是这么做的：`patch["id"] = id`）。
	for _, k := range automation.ReservedKeys {
		delete(doc, k)
	}
	rest, err := json.Marshal(doc)
	if err != nil {
		return "", "", nil, err
	}
	df, err := capability.DecodeDefinitionDoc(automationdomain.Locator(id), rest)
	if err != nil {
		return "", "", nil, err
	}
	if err := df.Set("id", id); err != nil {
		return "", "", nil, err
	}
	body, err := df.Marshal()
	if err != nil {
		return "", "", nil, err
	}
	return string(body), id, rt, nil
}

// automationRuntimeFromDoc 从文件原文里抽出运行态键（没有这些键 → nil = 未触发过）。
//
// 类型不符一律**报错**（计入 Invalid 并 WARN）：静默丢掉一个写坏的 `last_fired_at` 会让用户
// 拿到"我明明写了它为什么不生效"的困惑；而报错是可操作的（删掉或改对那个键再导入）。
func automationRuntimeFromDoc(doc map[string]json.RawMessage) (*store.AutomationRuntimeState, error) {
	rt := store.AutomationRuntimeState{}
	found := false
	for _, k := range automation.ReservedKeys {
		raw, ok := doc[k]
		if !ok {
			continue
		}
		found = true
		switch k {
		case "last_fired_at":
			s, err := automationStringKey(k, raw)
			if err != nil {
				return nil, err
			}
			if s == "" {
				continue
			}
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return nil, fmt.Errorf("last_fired_at %q 不是 RFC3339 时间：%v", s, err)
			}
			// 归一成**唯一格式**（UTC + RFC3339Nano）：它是认领 CAS 的比较值，两种写法会让
			// "同一个 occurrence"比对不上（详见 automationLastFiredText）。
			rt.LastFiredAt = t.UTC().Format(time.RFC3339Nano)
		case "last_status", "last_reason", "last_session_id":
			s, err := automationStringKey(k, raw)
			if err != nil {
				return nil, err
			}
			switch k {
			case "last_status":
				rt.LastStatus = s
			case "last_reason":
				rt.LastReason = s
			default:
				rt.LastSessionID = s
			}
		case "consecutive_failures":
			if err := json.Unmarshal(raw, &rt.ConsecutiveFailures); err != nil {
				return nil, fmt.Errorf("consecutive_failures 须为整数：%v", err)
			}
		case "recent_runs":
			var runs []automation.RunRecord
			if err := json.Unmarshal(raw, &runs); err != nil {
				return nil, fmt.Errorf("recent_runs 须为运行史数组：%v", err)
			}
			// 有界：文件可以被人手工塞进 500 条，进库就该回到 RecentRunsMax（列的契约是"有界"）。
			if len(runs) > automation.RecentRunsMax {
				runs = runs[:automation.RecentRunsMax]
			}
			rt.RecentRuns = automationdomain.RunsJSON(runs)
		}
	}
	if !found {
		return nil, nil
	}
	return &rt, nil
}

// automationStringKey 读一个字符串型运行态键（缺省空串）。
func automationStringKey(key string, raw json.RawMessage) (string, error) {
	if string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s 须为字符串：%v", key, err)
	}
	return strings.TrimSpace(s), nil
}
