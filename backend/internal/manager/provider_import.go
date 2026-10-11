package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/vault"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/golib/glog"
)

// ——— 存量 provider 档案的一次性导入与导出（v11 下半；v13 改为列载荷） ———
//
// 与能力面（M6-4）/agent 面（M6-6）**同构**的三条路径，但目录形状不同：provider 档案是
// `<AgentDir>` 下的**一个文件**（`providers.json`，`{id: {type, model?, key_ref?, key?}}`），
// 而不是"每定义一个文件"。于是：
//
//	装配期（sqlite 模式）自动导入一次：**文件内容指纹**进 `meta.imported_providers`，内容没变就跳过；
//	`mate provider import`：显式重跑，每次按行判重（导入绝不覆盖）；
//	`mate provider export`：把 DB 里的行写回 `<AgentDir>/providers.json`（回退窗口的**唯一**回灌路径）。
//
// 三条都与其余三个域共用同一套纪律：**备份一次、只搬不删、不回写、对账不一致只告警不阻断**。
//
// v13 的两处变化（ADR-8）：
//   - 导入的落库物从"body JSON"变成**列载荷**（`store.ProviderColumns`）：键集合封闭，未知键
//     不再有寄存处（B2 的破坏性面，与迁移同一口径）；
//   - vault 不可用时**跳过那条密钥**并在报告里逐条列出，**不再把明文留在库里**。导入是一次
//     带报告的动作，"少了一支密钥且被列出"优于"明文留在可自由复制的 DB 里"。

// providersBackupName 迁移前备份的文件名（同目录，0600；**已存在则不覆盖**——
// 第二次启动不该把第一次的备份冲掉）。
const providersBackupName = "providers.json.bak-v11"

// providerMismatchExtraMarker 对账不一致里**"库里有、文件里没有"**这一类的判据标记。
//
// 它是"写入侧拼串"与"读取侧分档"之间唯一的耦合点，所以提成常量：`mate doctor` 要据它
// 把"写面新建后快照滞后"（正常，只告警）与"真不一致"（判失败）分开。两侧各手抄一份
// 中文串，会在某次改文案时静默失效——那时 doctor 会把正常状态报成失败。
const providerMismatchExtraMarker = "DB 里有、文件里没有"

// ProviderImportStats 一次导入的完整计数 = 域无关计数 + provider 面自己的计数。
type ProviderImportStats struct {
	DefinitionImportStats
	// Providers 文件里**过了闸门**的档案条数。
	Providers int
	// Backup 本次写下的备份文件路径（空 = 没有写，因为已存在或 DryRun）。
	Backup string
	// SkippedKeys 因 vault 不可用而**被跳过的密钥**（`<id>（原因）`，升序）。
	//
	// 它不是失败，但必须被看见：档案的其余列照常导入，只是这把密钥没进来——
	// 用户要照着这份清单重设（明文**不再有任何落点**）。
	SkippedKeys []string
	// Mismatch 导入后对账不一致的条目（人话描述；空 = 一致）。
	Mismatch []string
}

// ImportProviderFile 从 `<AgentDir>/providers.json` 导入存量档案到持久层（sqlite 模式专用）。
//
// 顺序是刻意的（与设计「数据迁移与校验」的流程图一致）：
//
//	① 幂等判据（`meta.imported_providers` == 文件内容指纹）→ 命中即整体跳过，一个字节都不写；
//	② 备份 `providers.json.bak-v11`（已存在则不覆盖）；
//	③ 逐条：旧明文走**既有**的 vault 加密路径（`vault.Put`，引用由 id 派生）；vault 不可用则
//	   跳过该密钥并逐条列入报告；v11 → v12 的字段映射（`type` → `protocol` + `base_url`）保留；
//	④ 单事务插入（绝不覆盖） + 一条 `action=import` 审计；
//	⑤ 对账：DB 行集合 == 文件档案集合（比 id / protocol / base_url，**key_ref 只作输入**）→
//	   不一致只 WARN 并落 `meta.providers_import_mismatch`，**不阻断启动**；
//	⑥ **不删、不回写**文件（sqlite 模式下它是导入/导出载体）。
func ImportProviderFile(ctx context.Context, st *store.Store, dir string, opts DefinitionImportOptions, out io.Writer) (ProviderImportStats, error) {
	var stats ProviderImportStats
	if st == nil {
		return stats, errors.New("manager: 导入 provider 档案需要 sqlite 持久层（MATE_STORE=sqlite）")
	}
	dir = strings.TrimSpace(dir)
	stats.Dir = dir
	if dir == "" {
		return stats, nil
	}
	path := filepath.Join(dir, providersFileName)
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

	items, expected, skipped, err := scanProviderFile(raw, path, dir, opts, out)
	if err != nil {
		return stats, err
	}
	stats.Providers = len(items)
	stats.SkippedKeys = skipped
	if opts.DryRun {
		return stats, countDryRun(ctx, st, items, &stats.DefinitionImportStats)
	}

	// ② 备份（只写一次）。
	if backup, bErr := backupProviderFile(path, dir); bErr != nil {
		// 备份失败**必须阻断**：回退窗口的关闭是不可逆动作，没有备份就没有回退路径。
		return stats, fmt.Errorf("manager: 备份 %s 失败（拒绝在无备份的情况下关闭回退窗口）: %w", providersFileName, bErr)
	} else {
		stats.Backup = backup
	}

	// ③④ 单事务导入（绝不覆盖 + 逐条 import 审计 + marker）。
	marker := ""
	if opts.Auto {
		marker = stats.Marker
	}
	if err := importDefinitionRows(ctx, st, items, model.MetaKeyImportedProviders, marker,
		&stats.DefinitionImportStats, "provider", out); err != nil {
		return stats, err
	}
	if stats.Already {
		return stats, nil
	}

	// ⑤ 对账：不一致只告警（档案数量是几十条，任何不一致都该被人看见，而不是被静默放过或直接拒绝启动）。
	stats.Mismatch = reconcileProviderRows(ctx, st, expected, dir)
	if len(stats.Mismatch) > 0 {
		glog.Warnw(ctx, "providers: 导入对账不一致（档案行与 providers.json 不等价；跑 `mate doctor --json` 查看）",
			"dir", dir, "count", len(stats.Mismatch))
		if err := st.SetMeta(ctx, model.MetaKeyProvidersImportMismatch, strings.Join(stats.Mismatch, "\n")); err != nil {
			glog.Warnw(ctx, "providers: 写对账不一致标记失败", "err", err)
		}
	} else if err := st.SetMeta(ctx, model.MetaKeyProvidersImportMismatch, ""); err != nil {
		glog.Warnw(ctx, "providers: 清除对账不一致标记失败", "err", err)
	}

	// ⑥ 不删、不回写。被跳过的密钥必须逐条说出来（它们是"少了一支密钥"的事实，不是失败）。
	if len(stats.SkippedKeys) > 0 {
		glog.Warnw(ctx, "providers: 有密钥未导入（vault 不可用，明文不再有任何落点）；修好 vault 后重跑 `mate provider import`",
			"count", len(stats.SkippedKeys), "ids", strings.Join(stats.SkippedKeys, "、"))
	}
	return stats, nil
}

// providerExpected 一条"文件里应该有"的档案（对账字段）。
//
// v12 把 `type` 换成了 `protocol` + `base_url`；v13 之后 DB 行里**没有 `key_ref` 列**，
// 所以对账比的是**资源身份**（id / protocol / base_url），`key_ref` 只作输入：
// 它用来回答另一个问题——"文件里有引用，但 vault 里没有对应密文"（那把密钥没进来）。
type providerExpected struct {
	ID       string
	Protocol string
	BaseURL  string
	KeyRef   string
}

// scanProviderFile 解析 `providers.json`：逐条过闸门、把旧明文迁进 vault、拼出要写进 DB 的列载荷。
//
// 返回 (列载荷, 对账期望, 被跳过的密钥, error)。
//
// **明文不再有任何落点**（ADR-8）：vault 不可用时那条密钥被跳过（记录在第三个返回值里），
// 档案其余列照常导入——宁可少一支且说出来，也不把明文写进可自由复制的 DB。
func scanProviderFile(raw []byte, path, dir string, opts DefinitionImportOptions, out io.Writer) ([]store.DefinitionImportItem, []providerExpected, []string, error) {
	var file map[string]map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, nil, nil, fmt.Errorf("manager: %s 解析失败: %w", path, err)
	}
	// vault 句柄按需惰性打开：只有真有明文要迁移时才碰 keyfile（DryRun 也**不**写 vault）。
	var vst *vault.Store
	var verr error
	vaultTried := false
	openVault := func() *vault.Store {
		if vaultTried {
			if verr != nil {
				return nil
			}
			return vst
		}
		vaultTried = true
		vst, verr = vault.Open(filepath.Dir(dir))
		if verr != nil {
			glog.Warnw(context.Background(), "providers: vault 不可用，旧明文密钥将被跳过（不再留明文）", "err", verr)
		}
		return vst
	}

	ids := make([]string, 0, len(file))
	for id := range file {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	items := make([]store.DefinitionImportItem, 0, len(ids))
	expected := make([]providerExpected, 0, len(ids))
	var skipped []string
	for _, id := range ids {
		entry := file[id]
		if id == "" || entry == nil {
			continue
		}
		// id 会进 vault 引用 → 必须过同一道校验（防路径穿越）；坏 id 逐条跳过并可见。
		if _, err := vault.ValidateRef(providerRef(id)); err != nil {
			fmt.Fprintf(os.Stderr, "provider import: 跳过 id %q（非法：%v）\n", id, err)
			continue
		}
		// **v11 → v12 字段映射**（迁移逃生门）：文件里可能还是旧 `type`（自由串）+ `projections`。
		// 导入保留对它的识别并映射成 `protocol` + `base_url`——映射完就不要再提旧键：
		// 留着它们等于让"底座维度"从导入路径重新进库。
		protocol := bodyString(entry, "protocol")
		if protocol == "" {
			protocol = string(resource.ProtocolOpenAI)
		}
		baseURL := bodyString(entry, "base_url")
		if baseURL == "" {
			baseURL = v11FileBaseURL(&providerFile{
				Type:        bodyString(entry, "type"),
				Projections: anyToProjections(entry["projections"]),
			})
		}
		keyRef := bodyString(entry, "key_ref")
		if k := bodyString(entry, "key"); k != "" {
			// 旧明文档案 → 走既有 vault 加密路径（**不新写一套**）。
			switch {
			case opts.DryRun:
				// DryRun 只读：不写 vault，也不声称迁移成功。
				keyRef = providerRef(id)
			case openVault() == nil:
				// vault 不可用：**跳过这条密钥**并列入报告（v12 是"保留明文在 body 里"）。
				skipped = append(skipped, id+"（密钥未导入：vault 不可用）")
				keyRef = ""
			default:
				if err := vst.Put(providerRef(id), vault.KindProviderKey, []byte(k)); err != nil {
					// 加密失败同样跳过并**让它可见**（导入不因此整体失败：其余档案照常搬）。
					fmt.Fprintf(os.Stderr, "provider import: %s 的明文密钥加密失败（跳过该密钥）: %v\n", id, err)
					skipped = append(skipped, id+"（密钥未导入："+err.Error()+"）")
					keyRef = ""
				} else {
					keyRef = providerRef(id)
				}
			}
		}
		items = append(items, store.DefinitionImportItem{
			Kind: model.CapabilityKindProvider, ID: id,
			Provider: &store.ProviderColumns{
				ID: id, Name: bodyString(entry, "name"), Protocol: protocol,
				BaseURL: baseURL, Note: bodyString(entry, "note"),
			},
		})
		expected = append(expected, providerExpected{ID: id, Protocol: protocol, BaseURL: baseURL, KeyRef: keyRef})
	}
	if out != nil {
		fmt.Fprintf(out, "provider import: 扫描 %s，%d 条档案\n", path, len(items))
	}
	return items, expected, skipped, nil
}

// reconcileProviderRows 对账：DB 行集合 == 文件档案集合。
//
// 口径：**任一字段不一致即算不一致**——档案数量是几十条，这里没有抽样一说，全量逐条比。
// 比的是 `protocol` / `base_url` 两列；`key_ref` 只作输入，用来发现"文件里有引用、
// vault 里没有密文"（那把密钥没进来）。
func reconcileProviderRows(ctx context.Context, st *store.Store, want []providerExpected, dir string) []string {
	rows, err := st.ListProviderDefinitions(ctx)
	if err != nil {
		return []string{"读 providers 行失败: " + err.Error()}
	}
	vst := openVaultIfPresent(filepath.Dir(dir))
	got := make(map[string]providerExpected, len(rows))
	for i := range rows {
		got[rows[i].ID] = providerExpected{
			ID: rows[i].ID, Protocol: rows[i].Protocol, BaseURL: rows[i].BaseURL,
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
		if w.KeyRef != "" && vst != nil && !vst.Has(w.KeyRef) {
			mismatch = append(mismatch, fmt.Sprintf("%s: 文件里有密钥引用 %q，但 vault 里没有对应密文（密钥未导入）", w.ID, w.KeyRef))
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
			id, providerMismatchExtraMarker))
	}
	return mismatch
}

// ExportProviderFile 把持久层里的档案写回 `<AgentDir>/providers.json`（回退窗口的**唯一**回灌路径）。
//
// 输出**确定性**（map 键排序 + 2 空格缩进 + 末尾换行）：导出两次逐字节相同，导出物可以 diff。
// **只写 `key_ref`**，而它只在该供应商确实有密文时才写：v13 的引用是 `providerRef(id)` 的纯函数
// 副本，写一个"指向不存在密文"的引用会让下次导入的对账报一条假不一致。
func ExportProviderFile(ctx context.Context, st *store.Store, dir string, dryRun bool, out io.Writer) (DefinitionExportStats, error) {
	stats := DefinitionExportStats{Dir: strings.TrimSpace(dir), DryRun: dryRun}
	if st == nil {
		return stats, errors.New("manager: 导出 provider 档案需要 sqlite 持久层（MATE_STORE=sqlite）")
	}
	if stats.Dir == "" {
		return stats, errors.New("manager: 导出 provider 档案需要 AgentDir（--dir 或 MATE_AGENTS）")
	}
	rows, err := st.ListProviderDefinitions(ctx)
	if err != nil {
		return stats, err
	}
	// vault 不可达 → 一律不写 key_ref（**绝不**退化成写明文）；而且**只在 vault 已经有主密钥时**
	// 才打开它——`vault.Open` 缺 keyfile 会创建主密钥，导出不该有这个副作用。
	vst := openVaultIfPresent(filepath.Dir(stats.Dir))
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
		if ref := providerRef(rows[i].ID); vst != nil && vst.Has(ref) {
			entry["key_ref"] = ref
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
			fmt.Fprintf(out, "  ── %s（--dry-run，未写盘）\n%s", providersFileName, indentJSONBlock(string(body)))
		}
		return stats, nil
	}
	if err := writeFileAtomic(filepath.Join(stats.Dir, providersFileName), body, 0o600); err != nil {
		stats.Written = 0
		stats.Failed++
		return stats, err
	}
	if out != nil {
		fmt.Fprintf(out, "  ✓ %s（%d 条档案）\n", providersFileName, len(file))
	}
	return stats, nil
}

// openVaultIfPresent 只在该位置**已经有主密钥**时打开 vault（`MATE_VAULT_KEY` 或 `<home>/vault.key`）。
//
// 为什么要有这条判断：`vault.Open` 在缺 keyfile 时会**创建**主密钥（一次不可逆的落笔），
// 而"导出"与"导入对账"都只是读——为了问一句"这条引用有没有密文"就把用户的 mate home 改掉，
// 是那种「看起来无害、事后没人说得清是谁建的」的副作用。真正要写密文的路径（导入明文迁移）
// 仍然直接用 `vault.Open`（它本来就要建）。
func openVaultIfPresent(home string) *vault.Store {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	if strings.TrimSpace(os.Getenv(vault.EnvKey)) == "" {
		if _, err := os.Stat(filepath.Join(home, vault.KeyFileName)); err != nil {
			return nil
		}
	}
	st, err := vault.Open(home)
	if err != nil {
		return nil
	}
	return st
}

// backupProviderFile 把 providers.json 复制成 `providers.json.bak-v11`（**已存在则不覆盖**）。
//
// 不覆盖是刻意的：第二次启动（或用户手动重跑导入）不该把第一次那份"迁移前真相"冲掉。
func backupProviderFile(path, dir string) (string, error) {
	dst := filepath.Join(dir, providersBackupName)
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
// "改了内容但大小没变"在能力/agent 面是低概率的漏判，在这里却正好是最常见的一种编辑
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
