package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"gorm.io/gorm"
)

// 本文件是 **v13 的数据迁移**（设计 §关键逻辑 ⑥，ADR-1/ADR-7/ADR-9/ADR-11/ADR-12）。
//
// 它把 `providers` / `models` 从「定义载体」（`body` 唯一权威）变成「资源表」（平台字段成列），
// 全在同一事务里（失败整体回滚，不存在"迁移了一半"）：
//
//	① 读 v12 的两表（`body` + 身份列 + revision + updated_at）；
//	② 解析出列值：`name` / `protocol` / `base_url` / `note`（provider）、`name` / `note`（model），
//	   未知键只登记**键名**，`input_modalities` 按拍板的默认 `text` 填；
//	③ 灌入 `*_v13` 临时表；
//	④ 逐行对账（行数 / id 集合 / 唯一键 / 逐字段），任一不成立即返回 error 整体回滚；
//	⑤ DROP 旧表 → RENAME → 建索引；
//	⑥ 写 `meta.migration_notes_v13`。
//
// **明文密钥绝不搬进新表**（ADR-11）：v12 的降级路径（vault 不可用时导入）曾把明文写进
// `body.key`。新表**没有能容纳它的列**，所以迁移只登记"哪一行有明文"（**不记值**），
// 唯一副本留在 `mate.db.bak-v13-<ts>` 里，由 `mate doctor` 指路让用户重设。
// 不趁迁移加密进 vault：vault（文件 + AES-GCM）与 SQLite 无法同事务，且迁移发生在
// `store.Open` 内部（service 拿到库句柄要到 `EnableStore` 之后），sweep 需要的三件东西
// 在那个时点凑不齐——让 store 去 import vault 等于把机密与"可自由复制的事实层"重新搅回一起。

// MigrationNotesV13 v13 迁移留下的人工处置事实（落 `meta.MetaKeyMigrationNotesV13`，
// 由 `mate doctor` 读出）。
//
// **结构里刻意没有任何承载密钥值的字段**：明文只可能存在于备份库里。这不是"忘了加"，
// 而是 ADR-11 的一半——事实可读、值不可读。
type MigrationNotesV13 struct {
	// BackupPath 迁移前的库文件备份（`mate.db.bak-v13-<ts>`）。**只管 DB**：vault（密文 +
	// 主密钥）不在其内，而 v13 不触碰 vault，所以回退不必恢复它。
	BackupPath string `json:"backup_path,omitempty"`
	// DroppedBodyKeys 被丢弃的 body 未知键（`<provider id>：k1、k2`）——值仍在备份库里。
	//
	// 这是 P7 的直接后果：那些键写了、存了、什么都不发生，而没有任何读面能看见它们。
	// 迁移**不做静默改变语义**的事，所以键名必须落成可读事实（ADR-9）。
	DroppedBodyKeys []string `json:"dropped_body_keys,omitempty"`
	// LegacyPlaintextKeys **正文里曾留明文密钥**的供应商 id（值**不在这里**，也不在新表里）。
	//
	// 出路由 doctor 给出：旧值可从 `mate.db.bak-v13-<ts>` 的 `providers.body.key` 取回，
	// 然后在「设置 · 模型」里重设一次密钥（此后密文进 vault）。
	LegacyPlaintextKeys []string `json:"legacy_plaintext_keys,omitempty"`
	// CreatedAtApproximated `created_at` 取的是 `updated_at` 近似值的行（`provider/<id>` /
	// `model/<id>`）——v11/v12 都没建 `created_at`，如实登记，不假装它是创建时间（Q3）。
	CreatedAtApproximated []string `json:"created_at_approximated,omitempty"`
	// ModalityDefaulted `input_modalities` 被填成默认 `text` 的条目（`<provider>/<model>`）。
	//
	// 这一步会**覆盖底座 catalog**（dsh 的 route 声明里 `input` 是明确值）：底座目录里本来
	// 能收图的老模型因此变成纯文本，除非用户逐条勾选"图片"。必须让用户看见并能一键改（ADR-12）。
	ModalityDefaulted []string `json:"modality_defaulted,omitempty"`
	// OrphanModels v12 里 `provider_id` 就指向不存在供应商的模型行（`<id>（<provider>/<model>）`）。
	//
	// **不阻断迁移**（它是被容忍的既有事实，代码里本来就有一等 problem 文案），但必须显式
	// 说出来——否则这些条目在界面上只是"一条谁都交付不了的模型"。
	OrphanModels []string `json:"orphan_models,omitempty"`
}

// migrationV12Provider v12 的一行 provider（只取迁移需要的字段）。
type migrationV12Provider struct {
	ID string
	// Body v12 的正文（键集合封闭，但那时还没列化）。
	Body string
	// Revision 行上的 CAS 计数——**字段名必须与列名一致**（gorm 按列名 → 蛇形字段名映射，
	// 写成 `Rev` 会静默扫不到，revision 于是被写成 0：这正是"对账对的是自己算出来的值"的盲区，
	// 所以夹具里逐行断言了 revision 的保真）。
	Revision int64
	// UpdatedAt 最后一次成功提交的时刻（存量行的 created_at 取它作近似值）。
	UpdatedAt string
}

// migrationV12Model v12 的一行 model。
type migrationV12Model struct {
	ID         string
	ProviderID string
	ModelID    string
	Body       string
	Revision   int64
	UpdatedAt  string
}

// migratedV13Provider 要写进 `providers_v13` 的一行。
type migratedV13Provider struct {
	ID        string
	Name      string
	Protocol  string
	BaseURL   string
	Note      string
	Revision  int64
	CreatedAt string
	UpdatedAt string
}

// migratedV13Model 要写进 `models_v13` 的一行。
type migratedV13Model struct {
	ID              string
	ProviderID      string
	ModelID         string
	Name            string
	Note            string
	InputModalities string
	Revision        int64
	CreatedAt       string
	UpdatedAt       string
}

// migrateV13 v13 的数据步骤（在 `providers_v13` / `models_v13` 建表之后、同一事务内执行）。
func migrateV13(ctx context.Context, tx *gorm.DB, s *Store) error {
	providers, err := readV12Providers(ctx, tx)
	if err != nil {
		return err
	}
	models, err := readV12Models(ctx, tx)
	if err != nil {
		return err
	}
	if bad := v12NonOpenAIProviders(providers); len(bad) > 0 {
		// 与 v12 处理非 OpenAI 协议同一条纪律：**拒绝迁移**（非破坏、可解释、可重试），
		// 而不是丢行或静默改协议——协议是安全相关的（它决定把密钥发到哪种形态的端点）。
		return fmt.Errorf("v13 的 providers.protocol 只有一种合法取值（`openai`，由库级 CHECK 兜住），"+
			"但有 %d 条供应商是别的取值：%s。迁移**未做任何改动**（拒绝迁移，不是静默改协议）："+
			"请在旧版本的「设置 · 模型」里改回 `openai`，或删掉这些供应商，再升级",
			len(bad), strings.Join(bad, "、"))
	}

	notes := &MigrationNotesV13{}
	if s.migrationBackupPath != "" {
		notes.BackupPath = s.migrationBackupPath
	}

	// ②③ providers：解析 → 灌列。
	provRows := make([]migratedV13Provider, 0, len(providers))
	for i := range providers {
		row, noteKeys, plaintext, err := parseV12ProviderBody(providers[i])
		if err != nil {
			return fmt.Errorf("解析供应商 %q 的正文: %w", providers[i].ID, err)
		}
		if len(noteKeys) > 0 {
			notes.DroppedBodyKeys = append(notes.DroppedBodyKeys,
				providers[i].ID+"："+strings.Join(noteKeys, "、"))
		}
		if plaintext {
			notes.LegacyPlaintextKeys = append(notes.LegacyPlaintextKeys, providers[i].ID)
		}
		notes.CreatedAtApproximated = append(notes.CreatedAtApproximated, "provider/"+providers[i].ID)
		if err := tx.WithContext(ctx).Exec(
			`INSERT INTO providers_v13 (id, name, protocol, base_url, note, revision, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			row.ID, row.Name, row.Protocol, row.BaseURL, row.Note, row.Revision, row.CreatedAt, row.UpdatedAt).Error; err != nil {
			return fmt.Errorf("插入列化后的供应商 %q: %w", row.ID, err)
		}
		provRows = append(provRows, row)
	}

	// ②③ models：解析 → 灌列（`input_modalities` 一律默认 `text` 并逐条登记）。
	modelRows := make([]migratedV13Model, 0, len(models))
	for i := range models {
		row := parseV12ModelBody(models[i])
		notes.ModalityDefaulted = append(notes.ModalityDefaulted, row.ProviderID+"/"+row.ModelID)
		notes.CreatedAtApproximated = append(notes.CreatedAtApproximated, "model/"+row.ID)
		if err := tx.WithContext(ctx).Exec(
			`INSERT INTO models_v13 (id, provider_id, model_id, name, note, input_modalities, revision, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.ID, row.ProviderID, row.ModelID, row.Name, row.Note, row.InputModalities,
			row.Revision, row.CreatedAt, row.UpdatedAt).Error; err != nil {
			return fmt.Errorf("插入列化后的模型 %q: %w", row.ID, err)
		}
		modelRows = append(modelRows, row)
	}

	// ④ 对账（同事务；任一不成立即回滚并拒绝启动）。
	orphans, err := reconcileV13(ctx, tx, provRows, modelRows)
	if err != nil {
		return err
	}
	notes.OrphanModels = orphans

	// ⑤ 换表：DROP 旧 → RENAME → 建索引（库级表达 `(provider_id, model_id)` 唯一）。
	for _, stmt := range []string{
		`DROP TABLE providers`,
		`ALTER TABLE providers_v13 RENAME TO providers`,
		`DROP TABLE models`,
		`ALTER TABLE models_v13 RENAME TO models`,
		`CREATE UNIQUE INDEX idx_models_provider_model ON models(provider_id, model_id)`,
		`CREATE INDEX idx_models_provider ON models(provider_id)`,
	} {
		if err := tx.WithContext(ctx).Exec(stmt).Error; err != nil {
			return fmt.Errorf("换表步骤 %q: %w", stmt, err)
		}
	}

	// ⑥ 迁移笔记落 meta（doctor 据此报告需人工处置的事实）。
	notes.DroppedBodyKeys = sortedUnique(notes.DroppedBodyKeys)
	notes.LegacyPlaintextKeys = sortedUnique(notes.LegacyPlaintextKeys)
	notes.CreatedAtApproximated = sortedUnique(notes.CreatedAtApproximated)
	notes.ModalityDefaulted = sortedUnique(notes.ModalityDefaulted)
	notes.OrphanModels = sortedUnique(notes.OrphanModels)
	notesJSON, err := json.Marshal(notes)
	if err != nil {
		return fmt.Errorf("序列化 v13 迁移笔记: %w", err)
	}
	return writeMetaTx(ctx, tx, model.MetaKeyMigrationNotesV13, string(notesJSON))
}

// readV12Providers 读全部 v12 供应商行（`body` 是唯一权威的那个形状）。
func readV12Providers(ctx context.Context, tx *gorm.DB) ([]migrationV12Provider, error) {
	var rows []migrationV12Provider
	if err := tx.WithContext(ctx).Raw(
		`SELECT id, body, revision, updated_at FROM providers ORDER BY id`).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读 v12 供应商: %w", err)
	}
	return rows, nil
}

// readV12Models 读全部 v12 模型行（身份两列已是列，`name`/`note` 还在 body 里）。
func readV12Models(ctx context.Context, tx *gorm.DB) ([]migrationV12Model, error) {
	var rows []migrationV12Model
	if err := tx.WithContext(ctx).Raw(
		`SELECT id, provider_id, model_id, body, revision, updated_at FROM models ORDER BY provider_id, model_id`).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读 v12 模型: %w", err)
	}
	return rows, nil
}

// v13ProviderOwnedKeys provider body 里**平台拥有**的键（其余键都是未知键，丢弃 + 登记）。
var v13ProviderOwnedKeys = map[string]bool{
	"name": true, "protocol": true, "base_url": true, "note": true,
	// `key_ref` 是派生量的第二份副本（ADR-7），`key` 是不能搬的明文（ADR-11）：
	// 两者都不是"未知键"，但都不进新表。
	"key_ref": true, "key": true,
}

// v12NonOpenAIProviders 找出 `protocol` 不是 `openai` 的供应商（id 升序）。
//
// v12 的迁移自己只写 `openai`，但**导入路径**会把文件里的 `protocol` 原样写进 body，
// 所以一个 v12 库完全可能带着别的取值——那会让 v13 建表语句上的 CHECK 在插入时炸掉，
// 表现为一句难懂的约束错误。这里提前变成一句可执行的说明。
func v12NonOpenAIProviders(providers []migrationV12Provider) []string {
	var bad []string
	for i := range providers {
		protocol := openAIProtocol
		var body map[string]any
		if err := json.Unmarshal([]byte(providers[i].Body), &body); err == nil {
			if v := bodyString(body, "protocol"); v != "" {
				protocol = v
			}
		}
		if protocol != openAIProtocol {
			bad = append(bad, providers[i].ID+"（protocol="+protocol+"）")
		}
	}
	sort.Strings(bad)
	return bad
}

// parseV12ProviderBody 把 v12 的 provider 正文解析成列值。
//
// 返回 (列值, 被丢弃的未知键名, 正文里是否有明文密钥, error)。
//
// `protocol` 缺失 → `openai`（与 v12 导入映射同一默认）；两者都不是"猜"，因为
// v12 的形状里 protocol 是**必填**（`rewriteV11ProviderBody` 恒写它），缺失只可能是
// 手写行，而 `openai` 是唯一合法取值。
func parseV12ProviderBody(p migrationV12Provider) (migratedV13Provider, []string, bool, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(p.Body), &body); err != nil {
		return migratedV13Provider{}, nil, false, fmt.Errorf("正文不是合法 JSON: %w", err)
	}
	protocol := bodyString(body, "protocol")
	if protocol == "" {
		protocol = openAIProtocol
	}
	var unknown []string
	for k := range body {
		if !v13ProviderOwnedKeys[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	created := p.UpdatedAt
	return migratedV13Provider{
		ID: p.ID, Name: bodyString(body, "name"), Protocol: protocol,
		BaseURL: bodyString(body, "base_url"), Note: bodyString(body, "note"),
		Revision: p.Revision, CreatedAt: created, UpdatedAt: p.UpdatedAt,
	}, unknown, bodyString(body, "key") != "", nil
}

// parseV12ModelBody 把 v12 的 model 正文解析成列值。
//
// `input_modalities` **一律** `text`：v12 里没有这个信息，按 2026-10-11 用户拍板的默认值填，
// 并由调用方逐条登记（它会覆盖底座 catalog，用户必须能看见）。
func parseV12ModelBody(m migrationV12Model) migratedV13Model {
	var body map[string]any
	_ = json.Unmarshal([]byte(m.Body), &body)
	return migratedV13Model{
		ID: m.ID, ProviderID: m.ProviderID, ModelID: m.ModelID,
		Name: bodyString(body, "name"), Note: bodyString(body, "note"),
		InputModalities: modalityText,
		Revision:        m.Revision, CreatedAt: m.UpdatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// modalityText 是 `input_modalities` 的默认值（两态里的保守那一态）。
//
// store 不 import `internal/platform/resource`：这里是**落库字面量**，与"今天应用层怎么写"是两件事
// （对齐 `openAIProtocol` 的同一条理由）。
const modalityText = "text"

// reconcileV13 同事务对账（设计 §关键逻辑 ⑥ 的步骤 5）。
//
// **全量、不抽样**：清单条数量级 10²，全量对账的成本可忽略，而抽样放过的恰是最容易错的那条。
// 返回孤儿模型行（`provider_id` 在 v13 providers 里找不到）——**不阻断**，由调用方登记。
func reconcileV13(ctx context.Context, tx *gorm.DB, providers []migratedV13Provider, models []migratedV13Model) ([]string, error) {
	// ① 逐行读回新表（"写完再读"是对账的唯一可信来源：写入本身也可能被驱动改写）。
	var gotProv []migratedV13Provider
	if err := tx.WithContext(ctx).Raw(
		`SELECT id, name, protocol, base_url, note, revision, created_at, updated_at FROM providers_v13`).
		Scan(&gotProv).Error; err != nil {
		return nil, fmt.Errorf("对账：读 providers_v13: %w", err)
	}
	var gotModels []migratedV13Model
	if err := tx.WithContext(ctx).Raw(
		`SELECT id, provider_id, model_id, name, note, input_modalities, revision, created_at, updated_at FROM models_v13`).
		Scan(&gotModels).Error; err != nil {
		return nil, fmt.Errorf("对账：读 models_v13: %w", err)
	}

	// ② 行数与 id 集合相等。
	if len(gotProv) != len(providers) {
		return nil, fmt.Errorf("对账失败：providers 迁移前 %d 行、新表 %d 行", len(providers), len(gotProv))
	}
	if len(gotModels) != len(models) {
		return nil, fmt.Errorf("对账失败：models 迁移前 %d 行、新表 %d 行", len(models), len(gotModels))
	}

	// ③ 逐字段逐字相等（列化不该改变任何一个平台字段）。
	wantProv := make(map[string]migratedV13Provider, len(providers))
	for _, p := range providers {
		wantProv[p.ID] = p
	}
	for _, g := range gotProv {
		w, ok := wantProv[g.ID]
		if !ok {
			return nil, fmt.Errorf("对账失败：新表里出现了迁移前不存在的供应商 %q", g.ID)
		}
		if g.Name != w.Name || g.Protocol != w.Protocol || g.BaseURL != w.BaseURL || g.Note != w.Note {
			return nil, fmt.Errorf("对账失败：供应商 %q 的平台字段变了（name %q→%q / protocol %q→%q / base_url %q→%q / note %q→%q）",
				g.ID, w.Name, g.Name, w.Protocol, g.Protocol, w.BaseURL, g.BaseURL, w.Note, g.Note)
		}
		if g.Revision != w.Revision {
			return nil, fmt.Errorf("对账失败：供应商 %q 的 revision 从 %d 变成 %d", g.ID, w.Revision, g.Revision)
		}
		delete(wantProv, g.ID)
	}
	if len(wantProv) > 0 {
		missing := make([]string, 0, len(wantProv))
		for id := range wantProv {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("对账失败：迁移前的供应商 %s 在新表里找不到", strings.Join(missing, "、"))
	}

	wantModel := make(map[string]migratedV13Model, len(models))
	seenKey := map[string]string{}
	for _, m := range models {
		wantModel[m.ID] = m
		key := m.ProviderID + "\x00" + m.ModelID
		if prev, dup := seenKey[key]; dup {
			return nil, fmt.Errorf("对账失败：迁移前的 (provider_id=%s, model_id=%s) 同时属于 %q 与 %q",
				m.ProviderID, m.ModelID, prev, m.ID)
		}
		seenKey[key] = m.ID
	}
	for _, g := range gotModels {
		w, ok := wantModel[g.ID]
		if !ok {
			return nil, fmt.Errorf("对账失败：新表里出现了迁移前不存在的模型 %q", g.ID)
		}
		if g.ProviderID != w.ProviderID || g.ModelID != w.ModelID {
			return nil, fmt.Errorf("对账失败：模型 %q 的身份列变了（provider_id %q→%q / model_id %q→%q）",
				g.ID, w.ProviderID, g.ProviderID, w.ModelID, g.ModelID)
		}
		if g.Name != w.Name || g.Note != w.Note || g.InputModalities != w.InputModalities {
			return nil, fmt.Errorf("对账失败：模型 %q 的内容列变了（name %q→%q / note %q→%q / input_modalities %q→%q）",
				g.ID, w.Name, g.Name, w.Note, g.Note, w.InputModalities, g.InputModalities)
		}
		if g.Revision != w.Revision {
			return nil, fmt.Errorf("对账失败：模型 %q 的 revision 从 %d 变成 %d", g.ID, w.Revision, g.Revision)
		}
		delete(wantModel, g.ID)
	}
	if len(wantModel) > 0 {
		missing := make([]string, 0, len(wantModel))
		for id := range wantModel {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("对账失败：迁移前的模型 %s 在新表里找不到", strings.Join(missing, "、"))
	}

	// ④ 孤儿行：`provider_id` 在 v13 providers 里找不到（v12 里就可能是孤儿，**不阻断**）。
	known := make(map[string]bool, len(providers))
	for _, p := range providers {
		known[p.ID] = true
	}
	var orphans []string
	for _, g := range gotModels {
		if !known[g.ProviderID] {
			orphans = append(orphans, fmt.Sprintf("%s（%s/%s）", g.ID, g.ProviderID, g.ModelID))
		}
	}
	sort.Strings(orphans)
	return orphans, nil
}
