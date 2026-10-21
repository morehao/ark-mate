package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"gorm.io/gorm"
)

// 本文件是 **v12 的数据迁移**（计划 M1-T1.6 / 设计 §数据迁移与校验）。
//
// 它做四件事，全在同一事务里（失败整体回滚，不存在"迁移了一半"）：
//
//	① 重写 `providers.body`：`type` → `protocol` + `base_url`，删掉 `type`/`projections`/`model`；
//	② 归并 `models`：`(runtime, provider_id, model_id)` 三元组 → `(provider_id, model_id)` 两元组，
//	   并把 `model_id` 从 body 提升为列；
//	③ 把逐 runtime 的 `is_default` 换算成 `meta` 里的**单一** `default_model`（ADR-21）；
//	④ 同事务对账（行数 / 逐组等价 / 默认模型 / 供应商条数），任一不成立即回滚。

// MigrationNotes v12 迁移留下的人工处置事实（落 `meta.MetaKeyMigrationNotes`，由 `mate doctor` 读出）。
//
// 迁移**不做静默改变语义**的事，但有些东西 v12 确实没有出口。那些不阻断迁移（B1：缺端点是一次性
// 人工成本），但必须落成可被读到的持久事实——否则"未迁移字段"只是一句没人能验证的话。
type MigrationNotes struct {
	// BackupPath 迁移前的库文件备份（`mate.db.bak-v12-<ts>`）。**只管 DB**：
	// vault（密文 + 主密钥）不在其内，而 v12 不触碰 vault，所以回退不必恢复它。
	BackupPath string `json:"backup_path,omitempty"`
	// MissingBaseURL 推导不出 `base_url` 的供应商（迁移后界面与 doctor 都要显式标「缺端点」，
	// 且该供应商**不进任何 Set**——宁可不下发，也不下发半条）。
	MissingBaseURL []string `json:"missing_base_url,omitempty"`
	// DefaultModelConflicts v11 逐 runtime 默认不一致的记录（已按 runtime id 字典序取一，可复现）。
	DefaultModelConflicts []string `json:"default_model_conflicts,omitempty"`
	// ModelNameConflicts 归并时 `name` 冲突的记录（取 id 字典序最小那行的非空值，不阻塞）。
	ModelNameConflicts []string `json:"model_name_conflicts,omitempty"`
	// UnmigratedFields v12 无出口的 v11 字段（`extra` / `compat` / `package` / `options` 等
	// 底座私有键）。值仍留在 `mate.db.bak-v12-<ts>` 里，这里只登记"它们没了"。
	UnmigratedFields []string `json:"unmigrated_fields,omitempty"`
}

// frozenV11ProviderEndpoints v11 的 `type` → 上游端点（**冻结副本**）。
//
// 为什么在迁移里自带一份而不 import 活实现：① 依赖方向是 `实现 → store`，反向 import 成环
// （archtest 守）；② 迁移描述的是**历史**，不该依赖会继续变化的活代码——活的那张表
// （当时的 `manager/provider_probe.go`）已随 P4a 搬走。
var frozenV11ProviderEndpoints = map[string]string{
	"openai":     "https://api.openai.com/v1",
	"deepseek":   "https://api.deepseek.com/v1",
	"openrouter": "https://openrouter.ai/api/v1",
	"groq":       "https://api.groq.com/openai/v1",
}

// v11OpenAICompatibleAPIs 与 `protocol=openai` 相容的 wire 协议取值（投影里的 `api`）。
//
// 空串也算相容：v11 里 `api` 是可选字段，不写它时底座按 route 自己的默认解析。
var v11OpenAICompatibleAPIs = map[string]bool{
	"":                   true,
	"openai-completions": true,
	"openai-responses":   true,
	"openai-chat":        true,
	"openai":             true,
	"chat-completions":   true,
}

// migrationV11Provider v11 的一行 provider（只取迁移需要的字段）。
type migrationV11Provider struct {
	ID    string
	Body  string
	Rev   int64
	UpdAt string
}

// migrationV11Model v11 的一行 model（身份三元组都在列/正文里）。
type migrationV11Model struct {
	ID         string
	Runtime    string
	ProviderID string
	Body       string
	IsDefault  bool
	Revision   int64
	UpdatedAt  string
}

// migrateV12 v12 的数据步骤（在 `models_v12` 建表之后、同一事务内执行）。
func migrateV12(ctx context.Context, tx *gorm.DB, s *Store) error {
	providers, err := readV11Providers(ctx, tx)
	if err != nil {
		return err
	}
	// 前置检查（**在任何写入之前**）：v11 显式声明了非 OpenAI 协议的供应商，v12 没有对应的
	// 表示。这里取"拒绝迁移并报「协议不支持」"，而不是丢行——丢行是**静默的数据丢失**
	// （凭据引用消失、密文变孤儿），与"不静默改协议"这条第一原则直接冲突；拒绝迁移则是
	// 非破坏的、可解释的、可重试的（用户在旧版本上修好或删掉该供应商，再升级）。
	if bad := v11UnsupportedProtocolProviders(providers); len(bad) > 0 {
		return fmt.Errorf("v12 只支持 OpenAI 兼容协议（`protocol=openai`），但有 %d 条供应商声明了别的协议："+
			"%s。迁移**未做任何改动**（拒绝迁移，不是静默改协议）：请在旧版本的「设置 · 模型」里把它们的"+
			"wire 协议改回 `openai-completions`，或删掉这些供应商，再升级",
			len(bad), strings.Join(bad, "、"))
	}

	notes := &MigrationNotes{}
	if s.migrationBackupPath != "" {
		notes.BackupPath = s.migrationBackupPath
	}

	// ① providers：body 改写。
	for i := range providers {
		newBody, note, err := rewriteV11ProviderBody(providers[i])
		if err != nil {
			return fmt.Errorf("改写供应商 %q 的正文: %w", providers[i].ID, err)
		}
		if note == "" {
			notes.MissingBaseURL = append(notes.MissingBaseURL, providers[i].ID)
		}
		if extra := v11UnmigratedProviderFields(providers[i].Body); len(extra) > 0 {
			notes.UnmigratedFields = append(notes.UnmigratedFields, providers[i].ID+"："+strings.Join(extra, "、"))
		}
		if err := tx.WithContext(ctx).Exec(
			`UPDATE providers SET body = ? WHERE id = ?`, newBody, providers[i].ID).Error; err != nil {
			return fmt.Errorf("写回供应商 %q: %w", providers[i].ID, err)
		}
	}

	// ② models：归并。
	oldModels, err := readV11Models(ctx, tx)
	if err != nil {
		return err
	}
	merged, defaultByRuntime, nameConflicts := mergeV11Models(oldModels)
	notes.ModelNameConflicts = nameConflicts

	now := model.FormatTime(time.Now().UTC())
	for i := range merged {
		row := merged[i]
		if err := tx.WithContext(ctx).Exec(
			`INSERT INTO models_v12 (id, provider_id, model_id, body, revision, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			row.ID, row.ProviderID, row.ModelID, row.Body, row.Revision, now).Error; err != nil {
			return fmt.Errorf("插入归并后的模型 %q: %w", row.ID, err)
		}
		for j := range oldModels {
			if oldModels[j].ID == row.ID && oldModels[j].UpdatedAt != "" {
				// 保留幸存行原来的 updated_at（它是"最后一次成功提交的时刻"，迁移不该改写它）。
				if err := tx.WithContext(ctx).Exec(
					`UPDATE models_v12 SET updated_at = ? WHERE id = ?`, oldModels[j].UpdatedAt, row.ID).Error; err != nil {
					return fmt.Errorf("回填模型 %q 的 updated_at: %w", row.ID, err)
				}
			}
		}
	}

	// ③ 换表：DROP 旧 → RENAME → 建唯一索引（库级表达 `(provider_id, model_id)` 唯一）。
	for _, stmt := range []string{
		`DROP TABLE models`,
		`ALTER TABLE models_v12 RENAME TO models`,
		`CREATE UNIQUE INDEX idx_models_provider_model ON models(provider_id, model_id)`,
		`CREATE INDEX idx_models_provider ON models(provider_id)`,
	} {
		if err := tx.WithContext(ctx).Exec(stmt).Error; err != nil {
			return fmt.Errorf("换表步骤 %q: %w", stmt, err)
		}
	}

	// ④ `meta.default_model`：多 runtime 默认不一致时取 **runtime id 字典序最小**者（确定性、可复现）。
	defaultModelID, conflicts := mergeV11DefaultModel(defaultByRuntime)
	notes.DefaultModelConflicts = conflicts
	if defaultModelID != "" {
		if err := writeMetaTx(ctx, tx, model.MetaKeyDefaultModel, defaultModelID); err != nil {
			return err
		}
	}

	// ⑤ 对账（同事务；任一不成立即回滚并拒绝启动）。
	if err := reconcileV12(ctx, tx, s, providers, oldModels, merged, defaultModelID, defaultByRuntime); err != nil {
		return err
	}

	// ⑥ 迁移笔记落 meta（doctor 据此报告需人工处置的事实）。
	notes.MissingBaseURL = sortedUnique(notes.MissingBaseURL)
	notes.UnmigratedFields = sortedUnique(notes.UnmigratedFields)
	notes.ModelNameConflicts = sortedUnique(notes.ModelNameConflicts)
	notes.DefaultModelConflicts = sortedUnique(notes.DefaultModelConflicts)
	notesJSON, err := json.Marshal(notes)
	if err != nil {
		return fmt.Errorf("序列化迁移笔记: %w", err)
	}
	return writeMetaTx(ctx, tx, model.MetaKeyMigrationNotes, string(notesJSON))
}

// readV11Providers 读全部 v11 供应商行。
func readV11Providers(ctx context.Context, tx *gorm.DB) ([]migrationV11Provider, error) {
	var rows []migrationV11Provider
	if err := tx.WithContext(ctx).Raw(`SELECT id, body, revision, updated_at FROM providers ORDER BY id`).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读 v11 供应商: %w", err)
	}
	return rows, nil
}

// readV11Models 读全部 v11 模型行（含 runtime / is_default 两个即将消失的列）。
func readV11Models(ctx context.Context, tx *gorm.DB) ([]migrationV11Model, error) {
	var rows []migrationV11Model
	if err := tx.WithContext(ctx).Raw(
		`SELECT id, runtime, provider_id, body, is_default, revision, updated_at FROM models ORDER BY id`).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读 v11 模型: %w", err)
	}
	return rows, nil
}

// v11UnsupportedProtocolProviders 找出显式声明了非 OpenAI 协议的供应商（id 升序）。
//
// 两个信号：`type` 是 `anthropic`（v11 里 `type` 是厂商，anthropic 意味着原生协议），
// 或任一 `projections.<runtime>.api` 不在 OpenAI 相容集里。
func v11UnsupportedProtocolProviders(providers []migrationV11Provider) []string {
	var bad []string
	for i := range providers {
		var body map[string]any
		if err := json.Unmarshal([]byte(providers[i].Body), &body); err != nil {
			continue // 坏正文交给改写那一步报错
		}
		if strings.EqualFold(bodyString(body, "type"), "anthropic") {
			bad = append(bad, providers[i].ID)
			continue
		}
		projs, _ := body["projections"].(map[string]any)
		for _, frag := range projs {
			m, _ := frag.(map[string]any)
			api := bodyString(m, "api")
			if !v11OpenAICompatibleAPIs[api] {
				bad = append(bad, providers[i].ID+"（api="+api+"）")
				break
			}
		}
	}
	sort.Strings(bad)
	return bad
}

// rewriteV11ProviderBody 把 v11 的 provider 正文改写成 v12 的形状。
//
// 返回 (新正文, 推导出的 base_url, 错误)。`base_url` 为空串 = 推不出来（调用方记进 doctor 问题清单）。
//
// 推导优先级（设计 §数据迁移与校验 第 2 条）：
//  1. `projections.<任意 runtime>.baseURL` / `settings_baseURL` / `settings.baseURL` 的第一个非空值；
//  2. 否则按 `type` 查**冻结的**已知映射表；
//  3. 都没有 → 留空（B1：不阻塞，界面与 doctor 报「缺端点」）。
func rewriteV11ProviderBody(p migrationV11Provider) (string, string, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(p.Body), &body); err != nil {
		return "", "", fmt.Errorf("正文不是合法 JSON: %w", err)
	}
	baseURL := v11ProviderBaseURL(body)
	// v12 的正文形状：{name?, protocol, base_url?, note?} + {key_ref?} + 其余未知键。
	out := map[string]any{"protocol": string(openAIProtocol)}
	if v := bodyString(body, "name"); v != "" {
		out["name"] = v
	}
	if v := bodyString(body, "note"); v != "" {
		out["note"] = v
	}
	if baseURL != "" {
		out["base_url"] = baseURL
	}
	if ref := bodyString(body, "key_ref"); ref != "" {
		out["key_ref"] = ref
	}
	// 未知键原样保留（落盘纪律）：**除了** v11 那几个被删掉的键。
	for k, v := range body {
		switch k {
		case "type", "projections", "model", "name", "note", "key_ref", "base_url", "protocol", "key":
			continue
		}
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", "", err
	}
	return string(b), baseURL, nil
}

// openAIProtocol 是 `resource.ProtocolOpenAI` 的字面量副本。
//
// store 不 import `internal/platform/resource`：迁移描述的是**历史形状**，它与"今天应用层怎么写"是两件事。
// 这条字符串是落库值（对外契约的一部分），在这里写死比引一个会演进的类型更稳。
const openAIProtocol = "openai"

// v11ProviderBaseURL 按优先级从 v11 正文里推导端点。
func v11ProviderBaseURL(body map[string]any) string {
	if projs, ok := body["projections"].(map[string]any); ok {
		// map 迭代无序 → 先按键排序，保证"第一个非空值"是**确定性**的（同库同结果）。
		keys := make([]string, 0, len(projs))
		for k := range projs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			frag, ok := projs[k].(map[string]any)
			if !ok {
				continue
			}
			if v := bodyString(frag, "baseURL"); v != "" {
				return v
			}
			if v := bodyString(frag, "settings_baseURL"); v != "" {
				return v
			}
			if st, ok := frag["settings"].(map[string]any); ok {
				if v := bodyString(st, "baseURL"); v != "" {
					return v
				}
			}
		}
	}
	if v := bodyString(body, "base_url"); v != "" {
		return v
	}
	if ep, ok := frozenV11ProviderEndpoints[strings.ToLower(bodyString(body, "type"))]; ok {
		return ep
	}
	return ""
}

// v11UnmigratedProviderFields 列出 v12 无出口的 v11 字段（如实登记，不假装迁移）。
func v11UnmigratedProviderFields(raw string) []string {
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil
	}
	var out []string
	projs, _ := body["projections"].(map[string]any)
	for _, k := range []string{"package", "compat", "options", "api", "settings"} {
		found := false
		for _, frag := range projs {
			m, _ := frag.(map[string]any)
			if _, ok := m[k]; ok {
				found = true
				break
			}
		}
		if found {
			out = append(out, "projections.*."+k)
		}
	}
	sort.Strings(out)
	return out
}

// mergedV12Model 归并后要写进 `models_v12` 的一行。
type mergedV12Model struct {
	ID         string
	ProviderID string
	ModelID    string
	Body       string
	Revision   int64
}

// mergeV11Models 把 v11 的三元组清单归并成两元组。
//
// 规则（设计 §载体差异与对策 第 1 行）：
//   - 分组键 `(provider_id, model_id)`；
//   - **幸存 id** = 组内旧 id 字典序最小者（确定性、可复现）；
//   - `name` 取「按 id 排序后第一个非空值」；组内出现多个**不同**非空 name → 记一条冲突（不阻塞）；
//   - `extra` / `hidden` / `pinned` 不进 v12 正文（ADR-7），值留在 v11 备份里。
//
// 返回值：归并行、`runtime → 归并行 id`（默认模型换算用）、冲突描述。
func mergeV11Models(rows []migrationV11Model) ([]mergedV12Model, map[string]string, []string) {
	type group struct {
		providerID string
		modelID    string
		rows       []migrationV11Model
	}
	groups := map[string]*group{}
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		var body map[string]any
		_ = json.Unmarshal([]byte(r.Body), &body)
		modelID := bodyString(body, "model_id")
		key := r.ProviderID + "\x00" + modelID
		g, ok := groups[key]
		if !ok {
			g = &group{providerID: r.ProviderID, modelID: modelID}
			groups[key] = g
			order = append(order, key)
		}
		g.rows = append(g.rows, r)
	}
	sort.Strings(order)

	out := make([]mergedV12Model, 0, len(order))
	var conflicts []string
	for _, key := range order {
		g := groups[key]
		sort.Slice(g.rows, func(i, j int) bool { return g.rows[i].ID < g.rows[j].ID })
		survivor := g.rows[0]
		name, names := "", map[string]bool{}
		for _, r := range g.rows {
			var body map[string]any
			_ = json.Unmarshal([]byte(r.Body), &body)
			n := bodyString(body, "name")
			if n == "" {
				continue
			}
			names[n] = true
			if name == "" {
				name = n
			}
		}
		if len(names) > 1 {
			distinct := make([]string, 0, len(names))
			for n := range names {
				distinct = append(distinct, n)
			}
			sort.Strings(distinct)
			conflicts = append(conflicts, fmt.Sprintf(
				"模型 %s/%s 在 v11 里有多个不同显示名（%s），已取 id 字典序最小那行的 %q",
				g.providerID, g.modelID, strings.Join(distinct, " / "), name))
		}
		// 正文只留 name（note 在 v11 不存在；extra/hidden/pinned 无 v12 出口）。
		bodyMap := map[string]any{}
		if name != "" {
			bodyMap["name"] = name
		}
		b, _ := json.Marshal(bodyMap)
		out = append(out, mergedV12Model{
			ID: survivor.ID, ProviderID: g.providerID, ModelID: g.modelID, Body: string(b), Revision: survivor.Revision,
		})
	}

	// 默认模型换算：每个 runtime 的 is_default 行 → 它所在组的幸存 id。
	byRuntime := map[string]string{}
	for _, g := range groups {
		for _, r := range g.rows {
			if !r.IsDefault {
				continue
			}
			if prev, ok := byRuntime[r.Runtime]; !ok || r.ID < prev {
				byRuntime[r.Runtime] = r.ID
			}
		}
	}
	// 上面记的是旧 id；映射到幸存 id。
	survivorOf := map[string]string{}
	for _, m := range out {
		for _, g := range groups {
			for _, r := range g.rows {
				if r.ID == m.ID {
					survivorOf[r.ID] = m.ID
				}
			}
		}
	}
	defaultByRuntime := map[string]string{}
	for rt, oldID := range byRuntime {
		if sid, ok := survivorOf[oldID]; ok {
			defaultByRuntime[rt] = sid
		}
	}
	return out, defaultByRuntime, conflicts
}

// mergeV11DefaultModel 把"逐 runtime 的默认"换算成**单一值**（ADR-21）。
//
// 多个 runtime 指向不同模型时按 **runtime id 字典序最小**者取胜（确定性、可复现），
// 并回带一条冲突描述（`mate doctor` 打印它，并告诉用户另一侧的原默认可在页面上重设）。
func mergeV11DefaultModel(byRuntime map[string]string) (string, []string) {
	if len(byRuntime) == 0 {
		return "", nil
	}
	runtimes := make([]string, 0, len(byRuntime))
	for rt := range byRuntime {
		runtimes = append(runtimes, rt)
	}
	sort.Strings(runtimes)
	winnerRT, winnerID := runtimes[0], byRuntime[runtimes[0]]
	distinct := map[string]bool{}
	for _, rt := range runtimes {
		distinct[byRuntime[rt]] = true
	}
	if len(distinct) <= 1 {
		return winnerID, nil
	}
	var others []string
	for _, rt := range runtimes[1:] {
		if byRuntime[rt] != winnerID {
			others = append(others, fmt.Sprintf("%s→%s", rt, byRuntime[rt]))
		}
	}
	note := fmt.Sprintf("v11 存在逐底座默认不一致（%s→%s；%s），已按 runtime id 字典序取 %s 的取值；"+
		"另一侧的原默认可在「设置 · 模型」里重设", winnerRT, winnerID, strings.Join(others, "、"), winnerRT)
	return winnerID, []string{note}
}

// reconcileV12 同事务对账（设计 §数据迁移与校验 的五行判据）。
//
// **全量、不抽样**：清单条数量级 10²，全量对账的成本可忽略，而抽样放过的恰是最容易错的那条
// （同名不同 runtime 的归并）。
func reconcileV12(
	ctx context.Context, tx *gorm.DB, s *Store,
	providers []migrationV11Provider, oldModels []migrationV11Model, merged []mergedV12Model,
	defaultModelID string, defaultByRuntime map[string]string,
) error {
	// ① 模型行数 == 旧表 COUNT(DISTINCT provider_id, model_id)。
	distinct := map[string]bool{}
	for _, r := range oldModels {
		var body map[string]any
		_ = json.Unmarshal([]byte(r.Body), &body)
		distinct[r.ProviderID+"\x00"+bodyString(body, "model_id")] = true
	}
	if len(distinct) != len(merged) {
		return fmt.Errorf("对账失败：旧表有 %d 个 distinct (provider_id, model_id) 组，新表写入 %d 行",
			len(distinct), len(merged))
	}
	var newCount int64
	if err := tx.WithContext(ctx).Raw(`SELECT COUNT(*) FROM models`).Scan(&newCount).Error; err != nil {
		return fmt.Errorf("对账：读新表行数: %w", err)
	}
	if int(newCount) != len(merged) {
		return fmt.Errorf("对账失败：新表有 %d 行，期望 %d 行", newCount, len(merged))
	}

	// ② 逐组等价：每个旧组在新表恰好一行，且 model_id 逐字相同。
	got := map[string]string{} // key=(provider_id,model_id) → 新表行 id
	{
		var rows []struct {
			ID         string
			ProviderID string
			ModelID    string
		}
		if err := tx.WithContext(ctx).Raw(`SELECT id, provider_id, model_id FROM models`).Scan(&rows).Error; err != nil {
			return fmt.Errorf("对账：读新表身份列: %w", err)
		}
		for _, r := range rows {
			key := r.ProviderID + "\x00" + r.ModelID
			if _, dup := got[key]; dup {
				return fmt.Errorf("对账失败：新表里 (provider_id=%s, model_id=%s) 出现多行", r.ProviderID, r.ModelID)
			}
			got[key] = r.ID
		}
	}
	for _, r := range oldModels {
		var body map[string]any
		_ = json.Unmarshal([]byte(r.Body), &body)
		key := r.ProviderID + "\x00" + bodyString(body, "model_id")
		if _, ok := got[key]; !ok {
			return fmt.Errorf("对账失败：旧表的组 (provider_id=%s, model_id=%s) 在新表里找不到",
				r.ProviderID, bodyString(body, "model_id"))
		}
	}

	// ③ 默认模型：胜出 runtime 的取值必须**就是**写进 meta 的那一个。
	if len(defaultByRuntime) > 0 {
		runtimes := make([]string, 0, len(defaultByRuntime))
		for rt := range defaultByRuntime {
			runtimes = append(runtimes, rt)
		}
		sort.Strings(runtimes)
		want := defaultByRuntime[runtimes[0]]
		if want != defaultModelID {
			return fmt.Errorf("对账失败：胜出 runtime %q 的默认模型是 %q，但写进 meta 的是 %q",
				runtimes[0], want, defaultModelID)
		}
		// 写进 meta 的那个 id 必须真的在 models 表里。
		var n int64
		if err := tx.WithContext(ctx).Raw(`SELECT COUNT(*) FROM models WHERE id = ?`, defaultModelID).Scan(&n).Error; err != nil {
			return fmt.Errorf("对账：查默认模型: %w", err)
		}
		if n != 1 {
			return fmt.Errorf("对账失败：写进 meta 的默认模型 %q 在 models 表里有 %d 行", defaultModelID, n)
		}
	}

	// ④ 供应商条数前后相同。
	var provCount int64
	if err := tx.WithContext(ctx).Raw(`SELECT COUNT(*) FROM providers`).Scan(&provCount).Error; err != nil {
		return fmt.Errorf("对账：读供应商条数: %w", err)
	}
	if int(provCount) != len(providers) {
		return fmt.Errorf("对账失败：供应商条数 %d ≠ 迁移前 %d", provCount, len(providers))
	}
	return nil
}

// writeMetaTx 在事务内写一个 meta 键（裸 SQL：迁移不经过 DAO 层，它面对的是**旧**表形状）。
func writeMetaTx(ctx context.Context, tx *gorm.DB, key, value string) error {
	if err := tx.WithContext(ctx).Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value).Error; err != nil {
		return fmt.Errorf("写 meta.%s: %w", key, err)
	}
	return nil
}

// bodyString 从已解析的 JSON 对象里取一个字符串字段（非字符串/缺失 → 空串）。
func bodyString(body map[string]any, key string) string {
	if body == nil {
		return ""
	}
	v, ok := body[key].(string)
	if !ok {
		return ""
	}
	return v
}

// sortedUnique 去重 + 升序（迁移笔记的稳定输出：同库同结果，便于 diff）。
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
