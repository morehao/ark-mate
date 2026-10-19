package capabilityio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	domprovider "github.com/morehao/ark-mate/internal/domain/provider"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/credential"
	"github.com/morehao/ark-mate/internal/mechanism/definition"

	"github.com/morehao/ark-mate/internal/capability"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/resource"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/golib/glog"
)

// domprovider.ProvidersFileName provider 档案文件名（**目录级配置**：ProviderDir 下的保留文件）。
//
// 它留在文件侧：档案是"这台机器上有哪些上游凭据"，与 agent 定义的生命周期不同。

// ——— 存量 provider 档案的一次性导入与导出（v11 下半；v13 改为列载荷；v20 换密钥面） ———
//
// 与能力面（M6-4）**同构**的三条路径，但目录形状不同：provider 档案是
// `<ProviderDir>` 下的**一个文件**（`providers.json`，`{id: {type, model?, key_ref?, key?}}`），
// 而不是"每定义一个文件"。于是：
//
//	装配期（sqlite 模式）自动导入一次：**文件内容指纹**进 `meta.imported_providers`，内容没变就跳过；
//	`mate provider import`：显式重跑，每次按行判重（导入绝不覆盖）；
//	`mate provider export`：把 DB 里的行写回 `<ProviderDir>/providers.json`（回退窗口的唯一回灌路径）。
//
// 三条都与其余三个域共用同一套纪律：**备份一次、只搬不删、不回写、对账不一致只告警不阻断**。
//
// v13 起导入的落库物是**列载荷**（`store.ProviderColumns`）：键集合封闭，未知键不再有寄存处（B2）。
//
// **v20 起这里只剩"搬运"，不再有"跳过"**：旧明文 `key` 直接写进 `providers.key` 一列
// （旧机制不可用时才跳过的那个分支随机制一起删除），而 `key_ref` 里的**旧前缀**在这条
// 导入路径上被显式翻译（`capability.TranslateLegacySecretRef`——档案文件不在库里，迁移改不到它们）。
// 导出**永远不写明文**：`key` 只作为输入存在。

// ProviderImportStats 一次导入的完整计数 = 域无关计数 + provider 面自己的计数。
type ProviderImportStats struct {
	definition.DefinitionImportStats
	// Providers 文件里**过了闸门**的档案条数。
	Providers int
	// Backup 本次写下的备份文件路径（空 = 没有写，因为已存在或 DryRun）。
	Backup string
	// KeyProblems 本条档案里的密钥**没被搬进库**的条目（`<id>（原因）`，升序）。
	//
	// 它不是失败，但必须被看见：档案的其余列照常导入，只是这把密钥没进来——用户要照着
	// 这份清单重设。v20 之前它叫 `SkippedKeys`（那时唯一的原因是"旧机制不可用"），
	// 换库之后原因只剩两种：档案里的那份与库里已有的值冲突（不覆盖），或写库失败。
	KeyProblems []string
	// Mismatch 导入后对账不一致的条目（人话描述；空 = 一致）。
	Mismatch []string
}

// ImportProviderFile 从 `<ProviderDir>/providers.json` 导入存量档案到持久层（sqlite 模式专用）。
//
// 顺序是刻意的（与设计「数据迁移与校验」的流程图一致）：
//
//	① 幂等判据（`meta.imported_providers` == 文件内容指纹）→ 命中即整体跳过，一个字节都不写；
//	② 备份 `providers.json.bak-v11`（已存在则不覆盖）；
//	③ 逐条：旧明文 `key` 收进待写清单、旧前缀 `key_ref` 显式翻译；v11 → v12 的字段映射保留；
//	④ 单事务插入（绝不覆盖）+ 一条 `action=import` 审计；
//	⑤ 密钥逐条写进 `providers.key`（**库里已有值的不覆盖**——档案是历史，面板是现在）；
//	⑥ 对账：DB 行集合 == 文件档案集合（比 id / protocol / base_url / "有没有密钥"）→
//	   不一致只 WARN 并落 `meta.providers_import_mismatch`，**不阻断启动**；
//	⑦ **不删、不回写**文件（sqlite 模式下它是导入/导出载体）。
func ImportProviderFile(ctx context.Context, st *store.Store, dir string, opts definition.DefinitionImportOptions, out io.Writer) (ProviderImportStats, error) {
	var stats ProviderImportStats
	if st == nil {
		return stats, errors.New("manager: 导入 provider 档案需要 sqlite 持久层（MATE_STORE=sqlite）")
	}
	dir = strings.TrimSpace(dir)
	stats.Dir = dir
	if dir == "" {
		return stats, nil
	}
	path := filepath.Join(dir, domprovider.ProvidersFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		// 文件不存在 = 没有存量档案要搬（全新机器）。不是错误。
		return stats, nil
	}
	stats.Marker = contentFingerprint(raw)

	// ① 幂等：内容指纹一致 → 整体跳过（含备份与对账，一个字节都不写）。
	if opts.Auto {
		if cur, ok, mErr := st.GetMeta(ctx, model.MetaKeyImportedProviders); mErr != nil {
			return stats, mErr
		} else if ok && cur == stats.Marker {
			stats.Already = true
			return stats, nil
		}
	}

	items, expected, keys, keyProblems, err := scanProviderFile(ctx, raw, path, dir, opts, out)
	if err != nil {
		return stats, err
	}
	stats.Providers = len(items)
	stats.KeyProblems = keyProblems
	if opts.DryRun {
		return stats, definition.CountDryRun(ctx, st, items, &stats.DefinitionImportStats)
	}

	// ② 备份（只写一次）。
	if backup, bErr := backupProviderFile(path, dir); bErr != nil {
		// 备份失败**必须阻断**：回退窗口的关闭是不可逆动作，没有备份就没有回退路径。
		return stats, fmt.Errorf("manager: 备份 %s 失败（拒绝在无备份的情况下关闭回退窗口）: %w", domprovider.ProvidersFileName, bErr)
	} else {
		stats.Backup = backup
	}

	// ③④ 单事务导入（绝不覆盖 + 逐条 import 审计 + marker）。
	marker := ""
	if opts.Auto {
		marker = stats.Marker
	}
	if err := definition.ImportDefinitionRows(ctx, st, items, model.MetaKeyImportedProviders, marker,
		&stats.DefinitionImportStats, "provider", out); err != nil {
		return stats, err
	}
	if stats.Already {
		return stats, nil
	}

	// ⑤ 密钥：写进同一行的 `key` 列。**库里已经有值的不动**——档案是历史快照，而面板是现在；
	// 用一份旧明文覆盖用户刚在面板里轮换过的密钥，是最不该由"导入"做出来的事。
	stats.KeyProblems = append(stats.KeyProblems, importProviderKeys(ctx, st, keys)...)
	sort.Strings(stats.KeyProblems)

	// ⑥ 对账：不一致只告警（档案数量是几十条，任何不一致都该被人看见，而不是被静默放过或直接拒绝启动）。
	stats.Mismatch = reconcileProviderRows(ctx, st, expected)
	if len(stats.Mismatch) > 0 {
		glog.Warnw(ctx, "providers: 导入对账不一致（档案行与 providers.json 不等价；跑 `mate doctor --json` 查看）",
			"dir", dir, "count", len(stats.Mismatch))
		if err := st.SetMeta(ctx, model.MetaKeyProvidersImportMismatch, strings.Join(stats.Mismatch, "\n")); err != nil {
			glog.Warnw(ctx, "providers: 写对账不一致标记失败", "err", err)
		}
	} else if err := st.SetMeta(ctx, model.MetaKeyProvidersImportMismatch, ""); err != nil {
		glog.Warnw(ctx, "providers: 清除对账不一致标记失败", "err", err)
	}

	// ⑦ 不删、不回写。没搬进来的密钥必须逐条说出来（它们是"少了一支密钥"的事实，不是失败）。
	if len(stats.KeyProblems) > 0 {
		glog.Warnw(ctx, "providers: 有密钥未搬进库（清单见报告；档案里的明文**不会再被读**）",
			"count", len(stats.KeyProblems), "ids", strings.Join(stats.KeyProblems, "、"))
	}
	return stats, nil
}

// providerKeyImport 一条待写进库的明文密钥（旧档案 `key` 字段的搬运物）。
//
// 它**只在内存里活到写完为止**：不落任何中间文件、不进日志、不进程之外的地方。
type providerKeyImport struct {
	ID  string
	Key string
}

// importProviderKeys 把待写清单逐条写进 `providers.key`，返回没写成的条目（人话，含 id）。
//
// 只搬不覆盖：库里那一行已经有密钥时**跳过并说明**（见 ImportProviderFile ⑤ 的理由）。
func importProviderKeys(ctx context.Context, st *store.Store, keys []providerKeyImport) []string {
	if len(keys) == 0 {
		return nil
	}
	existing := make(map[string]bool, len(keys))
	if rows, err := st.ListProviderDefinitions(ctx); err == nil {
		for i := range rows {
			if strings.TrimSpace(rows[i].Key) != "" {
				existing[rows[i].ID] = true
			}
		}
	}
	var problems []string
	for _, k := range keys {
		if existing[k.ID] {
			problems = append(problems, k.ID+"（密钥未搬：库里已有值，不覆盖）")
			continue
		}
		written, err := st.SetProviderKey(ctx, k.ID, k.Key)
		switch {
		case err != nil:
			problems = append(problems, k.ID+"（密钥未搬："+err.Error()+"）")
		case !written:
			problems = append(problems, k.ID+"（密钥未搬：库里没有这一行——它可能在本次导入里被判重跳过了）")
		}
	}
	return problems
}

// providerExpected 一条"文件里应该有"的档案（对账字段）。
//
// v12 把 `type` 换成了 `protocol` + `base_url`；v13 之后 DB 行里**没有 `key_ref` 列**，
// 所以对账比的是**资源身份**（id / protocol / base_url）加上 v20 新加的一条：**"有没有密钥"**。
// `WantKey` 只作输入，用来回答"文件里有明文密钥，但库里没有"（那把密钥没搬进来）。
type providerExpected struct {
	ID       string
	Protocol string
	BaseURL  string
	// WantKey 文件里出现了**明文 `key`**（可搬进库的那一份）。
	//
	// 刻意**不含 `key_ref`**：v20 起它是派生的输出量，背后没有值；把它算进来只会让
	// "文件里有个引用、库里没有值"永远对不上账（而那个引用本来就不携带任何东西）。
	WantKey bool
}

// scanProviderFile 解析 `providers.json`：逐条过闸门、翻译旧前缀、收出待写的明文密钥。
//
// 返回 (列载荷, 对账期望, 待写密钥, 密钥面问题, error)。
//
// **明文不再有任何中间落点**：`key` 直接进待写清单，由 `importProviderKeys` 写进库的同一行。
// v20 之前那个"旧机制不可用就跳过"的分支随机制一起删除——没有"写不进去"的机制了。
func scanProviderFile(
	ctx context.Context, raw []byte, path, dir string, opts definition.DefinitionImportOptions, out io.Writer,
) ([]store.DefinitionImportItem, []providerExpected, []providerKeyImport, []string, error) {
	var file map[string]map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("manager: %s 解析失败: %w", path, err)
	}
	ids := make([]string, 0, len(file))
	for id := range file {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	items := make([]store.DefinitionImportItem, 0, len(ids))
	expected := make([]providerExpected, 0, len(ids))
	keys := make([]providerKeyImport, 0, len(ids))
	var problems []string
	for _, id := range ids {
		entry := file[id]
		if id == "" || entry == nil {
			continue
		}
		// id 会进托管文档的键 → 必须过同一道校验；坏 id 逐条跳过并可见。
		if err := domprovider.ValidateProviderID(id); err != nil {
			fmt.Fprintf(os.Stderr, "provider import: 跳过 id %q（非法：%v）\n", id, err)
			continue
		}
		// **v11 → v12 字段映射**（迁移逃生门）：文件里可能还是旧 `type`（自由串）+ `projections`。
		// 导入保留对它的识别并映射成 `protocol` + `base_url`——映射完就不要再提旧键：
		// 留着它们等于让"底座维度"从导入路径重新进库。
		protocol := domprovider.BodyString(entry, "protocol")
		if protocol == "" {
			protocol = string(resource.ProtocolOpenAI)
		}
		baseURL := domprovider.BodyString(entry, "base_url")
		if baseURL == "" {
			baseURL = domprovider.V11FileBaseURL(&domprovider.ProviderFile{
				Type:        domprovider.BodyString(entry, "type"),
				Projections: anyToProjections(entry["projections"]),
			})
		}
		// 旧前缀的**唯一翻译点**：档案文件不在库里，v20 迁移改不到它们，只能在导入时显式翻。
		// 翻出来的值只用于**认一下这条引用**（旧前缀落到日志/报告里；`key_ref` 本身是派生量，
		// 见下），不写回文件、也不进库。
		keyRef := domprovider.BodyString(entry, "key_ref")
		if keyRef != "" {
			if translated, ok := capability.TranslateLegacySecretRef(keyRef); ok {
				capability.WarnLegacySecretRef(ctx, keyRef, path+" 的 key_ref")
				keyRef = translated
			}
		}
		// `WantKey` 只认**明文 `key`**：v20 起 `key_ref` 是**派生的输出量**
		// （`mate provider export` 在库里那一列非空时才写它），它背后没有任何可搬的值。
		// 把手写的 `key_ref` 也算成"文件里有密钥"，会让每次导入都报一条永远修不好的假不一致。
		wantKey := false
		if k := domprovider.BodyString(entry, "key"); k != "" {
			wantKey = true
			if opts.DryRun {
				// DryRun 只读：不写库，也不声称搬过。
				problems = append(problems, id+"（明文密钥未搬：--dry-run）")
			} else {
				keys = append(keys, providerKeyImport{ID: id, Key: k})
			}
		}
		items = append(items, store.DefinitionImportItem{
			Kind: model.DefinitionKindProvider, ID: id,
			Provider: &store.ProviderColumns{
				ID: id, Name: domprovider.BodyString(entry, "name"), Protocol: protocol,
				BaseURL: baseURL, Note: domprovider.BodyString(entry, "note"),
			},
		})
		expected = append(expected, providerExpected{ID: id, Protocol: protocol, BaseURL: baseURL, WantKey: wantKey})
	}
	if out != nil {
		fmt.Fprintf(out, "provider import: 扫描 %s，%d 条档案\n", path, len(items))
	}
	return items, expected, keys, problems, nil
}

// reconcileProviderRows 对账：DB 行集合 == 文件档案集合。
//
// 口径：**任一字段不一致即算不一致**——档案数量是几十条，这里没有抽样一说，全量逐条比。
// 比的是 `protocol` / `base_url` 两列，外加"文件说有密钥、库里有没有"这一条（引自 `WantKey`）。
func reconcileProviderRows(ctx context.Context, st *store.Store, want []providerExpected) []string {
	rows, err := st.ListProviderDefinitions(ctx)
	if err != nil {
		return []string{"读 providers 行失败: " + err.Error()}
	}
	got := make(map[string]providerExpected, len(rows))
	for i := range rows {
		got[rows[i].ID] = providerExpected{
			ID: rows[i].ID, Protocol: rows[i].Protocol, BaseURL: rows[i].BaseURL,
			WantKey: strings.TrimSpace(rows[i].Key) != "",
		}
	}
	var mismatch []string
	for _, w := range want {
		g, ok := got[w.ID]
		if !ok {
			mismatch = append(mismatch, fmt.Sprintf("%s: 文件里有、DB 里没有", w.ID))
			continue
		}
		if g.Protocol != w.Protocol {
			mismatch = append(mismatch, fmt.Sprintf("%s: protocol 不一致（文件 %q / DB %q）", w.ID, w.Protocol, g.Protocol))
		}
		if g.BaseURL != w.BaseURL {
			mismatch = append(mismatch, fmt.Sprintf("%s: base_url 不一致（文件 %q / DB %q）", w.ID, w.BaseURL, g.BaseURL))
		}
		if w.WantKey && !g.WantKey {
			mismatch = append(mismatch, fmt.Sprintf("%s: 文件里有明文密钥（key），但库里没有（密钥未搬进来）", w.ID))
		}
		delete(got, w.ID)
	}
	// DB 里多出来的行不算错（可能是写面新建的，文件还没导出过），但要说出来。
	extra := make([]string, 0, len(got))
	for id := range got {
		extra = append(extra, id)
	}
	sort.Strings(extra)
	for _, id := range extra {
		mismatch = append(mismatch, fmt.Sprintf("%s: %s（写面新建？跑 `mate provider export` 同步文件）",
			id, ProviderMismatchExtraMarker))
	}
	return mismatch
}

// ExportProviderFile 把持久层里的档案写回 `<ProviderDir>/providers.json`（回退窗口的唯一回灌路径）。
//
// 输出**确定性**（map 键排序 + 2 空格缩进 + 末尾换行）：导出两次逐字节相同，导出物可以 diff。
//
// **绝不写明文**（v20 起这是一条硬规则）：`key` 只作为输入存在。`key_ref` 只在库里确实有密钥
// 时才写——它指向托管文档 `<MATE_HOME>/credentials.json` 里的一条，凭一个不存在的引用会让
// 下次导入的对账报一条假不一致。
func ExportProviderFile(ctx context.Context, st *store.Store, dir string, dryRun bool, out io.Writer) (definition.DefinitionExportStats, error) {
	stats := definition.DefinitionExportStats{Dir: strings.TrimSpace(dir), DryRun: dryRun}
	if st == nil {
		return stats, errors.New("manager: 导出 provider 档案需要 sqlite 持久层（MATE_STORE=sqlite）")
	}
	if stats.Dir == "" {
		return stats, errors.New("manager: 导出 provider 档案需要 ProviderDir（--dir 或 MATE_PROVIDERS）")
	}
	rows, err := st.ListProviderDefinitions(ctx)
	if err != nil {
		return stats, err
	}
	file := make(map[string]map[string]any, len(rows))
	for i := range rows {
		entry := map[string]any{"protocol": rows[i].Protocol}
		if rows[i].Name != "" {
			entry["name"] = rows[i].Name
		}
		if rows[i].BaseURL != "" {
			entry["base_url"] = rows[i].BaseURL
		}
		if rows[i].Note != "" {
			entry["note"] = rows[i].Note
		}
		if strings.TrimSpace(rows[i].Key) != "" {
			entry["key_ref"] = credential.ProviderRef(rows[i].ID)
		}
		entry["id"] = rows[i].ID
		file[rows[i].ID] = entry
	}
	b, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return stats, err
	}
	body := append(b, '\n')
	stats.Written = len(file)
	if dryRun {
		if out != nil {
			fmt.Fprintf(out, "  ── %s（--dry-run，未写盘）\n%s", domprovider.ProvidersFileName, definition.IndentJSONBlock(string(body)))
		}
		return stats, nil
	}
	if err := writeFileAtomic(filepath.Join(stats.Dir, domprovider.ProvidersFileName), body, 0o600); err != nil {
		stats.Written = 0
		stats.Failed++
		return stats, err
	}
	if out != nil {
		fmt.Fprintf(out, "  ✓ %s（%d 条档案）\n", domprovider.ProvidersFileName, len(file))
	}
	return stats, nil
}

// backupProviderFile 把 providers.json 复制成 `providers.json.bak-v11`（**已存在则不覆盖**）。
//
// 不覆盖是刻意的：第二次启动（或用户手动重跑导入）不该把第一次那份"迁移前真相"冲掉。
func backupProviderFile(path, dir string) (string, error) {
	dst := filepath.Join(dir, ProvidersBackupName)
	if _, err := os.Stat(dst); err == nil {
		return "", nil // 已有备份：保留它
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(dst, b, 0o600); err != nil {
		return "", err
	}
	return dst, nil
}

// writeFileAtomic 同目录 staging + `os.Rename`（与 `persistProvidersFile` 同款手法：
// 读者永远看不到"写了一半"的文件）。
func writeFileAtomic(path string, body []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, body, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// contentFingerprint 文件内容指纹（导入幂等的唯一算法）。
//
// provider 面用**内容**而不是"名字+大小"：整个域只有一个文件，而它的内容就是全部事实——
// "改了内容但大小没变"在能力面是低概率的漏判，在这里却正好是最常见的一种编辑
// （把 `type` 从 deepseek 改成 openai 长度可能相同）。
func contentFingerprint(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// anyToProjections 把导入文件里 `projections` 的任意 JSON 值折成 `map[string]any`
// （只服务于 v11 → v12 的 `baseURL` 推导；形状不对就当没有）。
func anyToProjections(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

// ProvidersBackupName 迁移前备份的文件名（同目录，0600；**已存在则不覆盖**——
// 第二次启动不该把第一次的备份冲掉）。
const ProvidersBackupName = "providers.json.bak-v11"

// ProviderMismatchExtraMarker 对账不一致里**"库里有、文件里没有"**这一类的判据标记。
//
// 它是"写入侧拼串"与"读取侧分档"之间唯一的耦合点，所以提成常量：`mate doctor` 要据它
// 把"写面新建后快照滞后"（正常，只告警）与"真不一致"（判失败）分开。两侧各手抄一份
// 中文串，会在某次改文案时静默失效——那时 doctor 会把正常状态报成失败。
const ProviderMismatchExtraMarker = "DB 里有、文件里没有"
