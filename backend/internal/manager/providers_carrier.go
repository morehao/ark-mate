package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/morehao/ark-mate/internal/vault"
)

// ——— provider 档案读写面的**载体**（v11） ———
//
// 与 agent 面（`agents_carrier.go`）同源的分派判据（`s.st != nil`）：
//
//	memory 模式：<AgentDir>/providers.json（typed marshal + 同目录 tmp + rename，逐字节同今天）
//	sqlite 模式：providers 表（事务 + 行 revision CAS + 同事务一条审计）
//
// 两条路对外承诺相同：未知档案 → `provider_not_found`；并发改同一条 → `definition_changed`(40901)；
// **失败即无副作用**（内存与载体不会各改一半）。差别在 sqlite 这条路多两样东西——
// **未知键原样保留在 body 里**（P6：文件载体的定形结构体会丢未知键）与**可追审计**（P7）。
//
// 与文件载体的第三处差别是**方向性的**：sqlite 模式下 `providers.json` 降级为导入/导出载体，
// 写面**一个字节都不回写它**（B4）——"文件与 DB 谁是权威"这个问题因此在这条路上不存在。

// providerLocator 档案的定位串（人类可读；与 agent 面的 `db:agent/<id>` 同一用途）。
func (s *Service) providerLocator(id string) string {
	if s.st != nil {
		return "db:provider/" + id
	}
	dir := s.agentDirSnapshot()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, providersFileName)
}

// loadProviders 按持久层模式装载 provider 档案。
//
// 必须在 agent 装载之前完成（`LoadAgentDefinitions*` 内部先调它）：agent 的 `provider` 引用
// 要能在装载期被解析，否则"启动时先看到一批悬空引用、下一轮才对"。
func (s *Service) loadProviders(ctx context.Context, dir string) {
	if s.st != nil {
		s.loadProvidersFromStore(ctx, dir)
		return
	}
	s.loadProvidersFile(dir)
}

// loadProvidersFromStore 从 `providers` 表装载（sqlite 模式）。
//
// 与 `loadProvidersFile` 的语义逐字对齐：密文解密失败**不静默降级**（记 keyErr，由
// `CredentialProblem` 给出可执行出路），body 里残留的旧明文（vault 不可用时导入保留下来的那条）
// 仍然被采纳为内存明文。
func (s *Service) loadProvidersFromStore(ctx context.Context, dir string) {
	rows, err := s.st.ListProviderDefinitions(ctx)
	if err != nil {
		slog.Warn("providers: 从持久层装载失败（档案面为空）", "err", err)
		return
	}
	st, verr := s.providerVault()
	loaded := make(map[string]*providerDef, len(rows))
	for i := range rows {
		row := rows[i]
		var body map[string]any
		if err := json.Unmarshal([]byte(row.Body), &body); err != nil {
			slog.Warn("providers: 档案行 body 非法（忽略该行）", "provider", row.ID, "err", err)
			continue
		}
		p := &providerDef{
			ID:       row.ID,
			Revision: row.Revision,
			Body:     body,
			Type:     bodyString(body, "type"),
			Model:    bodyString(body, "model"),
			KeyRef:   bodyString(body, "key_ref"),
		}
		switch {
		case p.KeyRef != "":
			if st == nil {
				p.keyErr = fmt.Errorf("vault 不可用，无法解密 %s: %v", p.KeyRef, vaultUnavailable(verr))
				slog.Warn("providers: 有密文但 vault 不可用", "provider", row.ID, "ref", p.KeyRef, "err", p.keyErr)
				break
			}
			pt, err := st.Get(p.KeyRef, vault.KindProviderKey)
			if err != nil {
				p.keyErr = err
				slog.Warn("providers: 密钥密文解密失败（该 provider 在修复前不可用）", "provider", row.ID, "err", err)
				break
			}
			p.Key = string(pt)
		case bodyString(body, "key") != "":
			// 导入时 vault 不可用 → 旧明文被保留在 body 里（与文件载体同一条降级纪律）：
			// 采纳为内存明文，并标明它还没被加密。
			p.Key = bodyString(body, "key")
			p.keyErr = fmt.Errorf("vault 不可用，密钥仍为明文: %v", vaultUnavailable(verr))
		}
		loaded[row.ID] = p
	}
	s.mu.Lock()
	s.providers = loaded
	s.mu.Unlock()
	_ = dir // 目录只在文件载体里参与拼路径；sqlite 模式下它仍被 agent 面用作 vault 锚点
}

// providerBodyJSON 把内存里的档案编成 body 字符串（sqlite 载体）。
//
// **保住未知键**是这条路的重点（P6）：`d.Body` 是装载时读到的原始键值，这里只覆盖平台自己
// 拥有的三个键（`type` / `key_ref` / 降级时的 `key`），其余原样带走——历史 `model` 因此不会
// 在"改一次密钥"之后消失。`id` 不进 body：主键才是它的权威，第二个副本迟早漂移。
//
// `json.MarshalIndent` 对 map 键排序，因此同一份档案两次导出逐字节相同（导出物可 diff）。
func providerBodyJSON(d *providerDef) (string, error) {
	body := make(map[string]any, len(d.Body)+3)
	for k, v := range d.Body {
		if k == "id" {
			continue
		}
		body[k] = v
	}
	if d.Type != "" {
		body["type"] = d.Type
	} else {
		delete(body, "type")
	}
	if d.KeyRef != "" {
		body["key_ref"] = d.KeyRef
	} else {
		delete(body, "key_ref")
	}
	// 明文只在"vault 不可用且文件里本来就是明文"这一种降级路径上继续留在载体里
	// （与 `persistProvidersFile` 的既有纪律逐字相同：宁可多留一会明文，也不丢密钥）。
	if d.KeyRef == "" && d.Key != "" {
		body["key"] = d.Key
	} else {
		delete(body, "key")
	}
	b, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// persistProviderDefinition 提交一条档案（新建或覆盖）。
//
// sqlite 模式走行 CAS：`Revision == 0` 是新建，否则仅当行仍等于该版本。
// 返回 `definition_changed`(40901) 而不是静默覆盖——与其余四个定义面逐字同形。
func (s *Service) persistProviderDefinition(ctx context.Context, d *providerDef) error {
	if s.st == nil {
		return s.persistProvidersFile()
	}
	body, err := providerBodyJSON(d)
	if err != nil {
		return err
	}
	expected := d.Revision
	ok, err := s.st.UpsertProviderDefinition(ctx, d.ID, body, expected, definitionWriteActor)
	if err != nil {
		return err
	}
	if !ok {
		return definitionChangedErr(s.providerLocator(d.ID))
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err == nil {
		d.Body = parsed
	}
	d.Revision = expected + 1
	return nil
}

// removeProviderDefinition 删除载体里的一条档案。
//
// 返回 false = 载体里本来就没有（行不存在 / 文件里没有这一条）——调用方据此保持"删除是幂等的"。
func (s *Service) removeProviderDefinition(ctx context.Context, id string) (bool, error) {
	if s.st == nil {
		s.mu.Lock()
		_, existed := s.providers[id]
		delete(s.providers, id)
		s.mu.Unlock()
		if !existed {
			return false, nil
		}
		return true, s.persistProvidersFile()
	}
	row, found, err := s.st.GetProviderDefinition(ctx, id)
	if err != nil || !found {
		return false, err
	}
	ok, err := s.st.DeleteProviderDefinition(ctx, id, row.Revision, definitionWriteActor)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, definitionChangedErr(s.providerLocator(id))
	}
	return true, nil
}

// bodyString 从 body map 里取一个字符串键（缺失/类型不符 → 空串）。
func bodyString(body map[string]any, key string) string {
	v, ok := body[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// providerFileExists `<AgentDir>/providers.json` 是否存在（doctor 与迁移提示用）。
func providerFileExists(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, providersFileName))
	return err == nil
}
