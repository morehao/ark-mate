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

// ——— 存量 provider 档案的一次性导入与导出（v11 下半） ———
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
	// Plaintext 导入后正文里**仍留明文**的档案数（vault 不可用时的降级路径）。
	// 它不是失败，但必须被看见——doctor 会据此报一条 warn。
	Plaintext int
	// Mismatch 导入后对账不一致的条目（人话描述；空 = 一致）。
	Mismatch []string
}

// ImportProviderFile 从 `<AgentDir>/providers.json` 导入存量档案到持久层（sqlite 模式专用）。
//
// 顺序是刻意的（与设计「数据迁移与校验」的流程图一致）：
//
//	① 幂等判据（`meta.imported_providers` == 文件内容指纹）→ 命中即整体跳过，一个字节都不写；
//	② 备份 `providers.json.bak-v11`（已存在则不覆盖）；
//	③ 逐条：旧明文走**既有**的 vault 加密路径（`vault.Put` + `key_ref`），body 保留除
//	   `key`/`id` 外的**所有键**（含历史 `model`）——零丢失；
//	④ 单事务插入（绝不覆盖） + 一条 `action=import` 审计；
//	⑤ 对账：DB 行集合 == 文件档案集合（比 id / type / key_ref）→ 不一致只 WARN 并落
//	   `meta.providers_import_mismatch`，**不阻断启动**；
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

	items, expected, plaintext, err := scanProviderFile(raw, path, dir, opts, out)
	if err != nil {
		return stats, err
	}
	stats.Providers = len(items)
	stats.Plaintext = plaintext
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

	// ⑥ 不删、不回写。若正文里仍有明文，明确说出来（它是降级路径的痕迹，不是失败）。
	if stats.Plaintext > 0 {
		glog.Warnw(ctx, "providers: 有档案的密钥仍是明文（vault 不可用）；修好 vault 后重跑 `mate provider import`",
			"count", stats.Plaintext)
	}
	return stats, nil
}

// providerExpected 一条"文件里应该有"的档案（对账字段）。
//
// v12 把 `type` 换成了 `protocol` + `base_url`：对账比的是**资源身份**，不是 v11 的凭据档案类型。
type providerExpected struct {
	ID       string
	Protocol string
	BaseURL  string
	KeyRef   string
}

// scanProviderFile 解析 `providers.json`：逐条过闸门、把旧明文迁进 vault、拼出要写进 DB 的正文。
//
// 保留未知键是这一步的重点（P6）：body = 文件里除 `key`/`id` 外的**所有**键，于是历史 `model`
// 与用户手写的备注不会在迁移中消失（`mate provider export` 能把它们原样写回去）。
func scanProviderFile(raw []byte, path, dir string, opts DefinitionImportOptions, out io.Writer) ([]store.DefinitionImportItem, []providerExpected, int, error) {
	var file map[string]map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, nil, 0, fmt.Errorf("manager: %s 解析失败: %w", path, err)
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
			glog.Warnw(context.Background(), "providers: vault 不可用，旧明文将原样保留在正文里", "err", verr)
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
	plaintext := 0
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
		body := make(map[string]any, len(entry)+1)
		for k, v := range entry {
			if k == "id" || k == "key" {
				continue // id 是主键（权威在行上）；key 单独处理
			}
			body[k] = v
		}
		// **v11 → v12 字段映射**（迁移逃生门）：文件里可能还是旧 `type`（自由串）+ `projections`。
		// 导入保留对它的识别并映射成 `protocol` + `base_url`，映射完就把旧键删掉——
		// 留着它们等于让"底座维度"从导入路径重新进库。
		protocol := bodyString(body, "protocol")
		baseURL := bodyString(body, "base_url")
		if protocol == "" {
			protocol = string(resource.ProtocolOpenAI)
		}
		if baseURL == "" {
			baseURL = v11FileBaseURL(&providerFile{
				Type:        bodyString(body, "type"),
				Projections: anyToProjections(body["projections"]),
			})
		}
		delete(body, "type")
		delete(body, "projections")
		delete(body, "model")
		body["protocol"] = protocol
		if baseURL != "" {
			body["base_url"] = baseURL
		}
		keyRef := bodyString(body, "key_ref")
		if k := bodyString(entry, "key"); k != "" {
			// 旧明文档案 → 走既有 vault 加密路径（**不新写一套**）。
			if opts.DryRun {
				// DryRun 只读：不写 vault，也不声称迁移成功。
				keyRef = providerRef(id)
			} else if vst := openVault(); vst == nil {
				// vault 不可用：保留明文（丢了比多留一会更糟，与文件载体同一条纪律）。
				body["key"] = k
				plaintext++
			} else if err := vst.Put(providerRef(id), vault.KindProviderKey, []byte(k)); err != nil {
				// 加密失败同样保留明文，并**让它可见**（导入不因此整体失败：其余档案照常搬）。
				fmt.Fprintf(os.Stderr, "provider import: %s 的明文密钥加密失败（保留明文）: %v\n", id, err)
				body["key"] = k
				plaintext++
			} else {
				keyRef = providerRef(id)
			}
		} else if keyRef != "" && opts.DryRun {
			// 只读路径不校验密文可解性（那是装载期的事，且有 keyErr 承载）。
			_ = keyRef
		}
		if keyRef != "" && bodyString(body, "key") == "" {
			body["key_ref"] = keyRef
		}
		b, err := json.MarshalIndent(body, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "provider import: %s 正文编码失败: %v\n", id, err)
			continue
		}
		items = append(items, store.DefinitionImportItem{
			Kind: model.CapabilityKindProvider, ID: id, Body: string(b),
		})
		expected = append(expected, providerExpected{ID: id, Protocol: protocol, BaseURL: baseURL, KeyRef: keyRef})
	}
	if out != nil {
		fmt.Fprintf(out, "provider import: 扫描 %s，%d 条档案\n", path, len(items))
	}
	return items, expected, plaintext, nil
}

// reconcileProviderRows 对账：DB 行集合 == 文件档案集合（比 id / type / key_ref 三个字段）。
//
// 口径：**任一字段不一致即算不一致**——档案数量是几十条，这里没有抽样一说，全量逐条比。
// note 与未知键的呈现顺序不参与比较（它们不是"等价"的判据）。
func reconcileProviderRows(ctx context.Context, st *store.Store, want []providerExpected) []string {
	rows, err := st.ListProviderDefinitions(ctx)
	if err != nil {
		return []string{"读 providers 行失败: " + err.Error()}
	}
	got := make(map[string]providerExpected, len(rows))
	for i := range rows {
		var body map[string]any
		if err := json.Unmarshal([]byte(rows[i].Body), &body); err != nil {
			got[rows[i].ID] = providerExpected{ID: rows[i].ID, Protocol: "<body 非法>"}
			continue
		}
		got[rows[i].ID] = providerExpected{
			ID: rows[i].ID, Protocol: bodyString(body, "protocol"),
			BaseURL: bodyString(body, "base_url"), KeyRef: bodyString(body, "key_ref"),
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
		if g.KeyRef != w.KeyRef {
			mismatch = append(mismatch, fmt.Sprintf("%s: key_ref 不一致（文件 %q / DB %q）", w.ID, w.KeyRef, g.KeyRef))
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
// 只写 `key_ref`（明文只在"vault 不可用且正文里本来就是明文"的降级路径上才继续出现）。
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
	file := make(map[string]map[string]any, len(rows))
	for i := range rows {
		var body map[string]any
		if err := json.Unmarshal([]byte(rows[i].Body), &body); err != nil {
			stats.Failed++
			fmt.Fprintf(os.Stderr, "provider export: %s 的正文非法，未导出: %v\n", rows[i].ID, err)
			continue
		}
		entry := make(map[string]any, len(body)+1)
		for k, v := range body {
			if k == "id" {
				continue
			}
			entry[k] = v
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
