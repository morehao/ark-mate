package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-forge/internal/dto/dtoprovider"
	"github.com/morehao/ark-forge/internal/vault"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/redact"
)

// providersFileName provider 档案文件名（**目录级配置**：AgentDir 下的保留文件，不是 agent 定义）。
//
// 它留在文件侧（计划 §7 P-9）：档案是"这台机器上有哪些上游凭据"，与 agent 定义的生命周期不同。
const providersFileName = "providers.json"

// ——— Provider 注入面（P2）：档案只写不读回 + 启动 env 注入 + §5 密钥加密落盘 ———
//
// 档案存于 <AgentDir>/providers.json（providersFileName）：{id: {type, model, key_ref}}。REST 语义：
// GET /providers 只回 {id,type,model,has_key}（密钥绝不读回）；PATCH /providers/:id
// 幂等 upsert（type/model 指针更新；key 非 nil 即覆盖/清除——空串 = 清除）。
// 启动注入：agent.provider 指向档案 id 时，acquire 拨号把其密钥以 <TYPE>_API_KEY=… 注入
// 子进程 env（默认映射；未知 type 取大写 type + _API_KEY）。
//
// 落盘形态（§5）：文件里**只留引用** `key_ref: "vault:providers/<id>"`，密文单独落
// `<forge_home>/vault/providers/<id>.enc`（AES-256-GCM，AAD 绑定 {kind, ref}）。
// 明文只存在于内存（d.Key）与子进程 env，绝不写盘——除非 vault 不可用且文件里本来就是
// 旧明文（此时**保留明文**而不是丢弃：丢了比多留一会更糟，并在诊断里报错）。
// keyfile 丢失 = 密文不可恢复（见 docs/deployment.md 的备份清单）。

// providerDef 磁盘/内存中的 provider 档案。
type providerDef struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Model string `json:"model,omitempty"`
	// Key 内存明文（仅注入/判 has_key 用；落盘一律走 KeyRef）。
	Key string `json:"-"`
	// KeyRef 密文引用（落盘字段）。
	KeyRef string `json:"key_ref,omitempty"`
	// keyErr 装载时解密失败的原因（CredentialProblem 据此给出可执行出路，不静默降级）。
	keyErr error `json:"-"`
}

// providerFile providers.json 的磁盘形状。
//
// Key 字段**只读**：用于把旧版本的明文档案迁移进 vault；除「vault 不可用」这一种情况外
// 一律不再写出（persistProvidersFile 只在 KeyRef 为空且确有明文时才写 Key）。
type providerFile struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Model  string `json:"model,omitempty"`
	KeyRef string `json:"key_ref,omitempty"`
	Key    string `json:"key,omitempty"`
}

// providerRef provider id → vault 引用（namespace 只有 providers）。
func providerRef(id string) string { return vault.RefPrefix + "providers/" + id }

// envKeyOf provider type → 注入的环境变量名。
func envKeyOf(typ string) string {
	if typ == "" {
		return ""
	}
	switch typ {
	case "openai", "anthropic", "deepseek", "openrouter", "groq":
		return strings.ToUpper(typ) + "_API_KEY"
	}
	return strings.ToUpper(typ) + "_API_KEY"
}

// providerVault 返回 vault 句柄（惰性创建 + 缓存）。
//
// home 取 agentDir 的父目录（默认 ~/.forge/agents → ~/.forge）：密钥 ~/.forge/vault.key、
// 密文 ~/.forge/vault/。agentDir 为空（纯内存场景/未配置）→ (nil, nil) = 无 vault，
// 此时密钥只在内存、绝不落盘。
func (s *Service) providerVault() (*vault.Store, error) {
	s.mu.Lock()
	if s.vaultTried {
		st, err := s.vaultStore, s.vaultErr
		s.mu.Unlock()
		return st, err
	}
	s.vaultTried = true
	dir := s.agentDir
	s.mu.Unlock()

	if dir == "" {
		return nil, nil
	}
	st, err := vault.Open(filepath.Dir(dir))
	s.mu.Lock()
	s.vaultStore, s.vaultErr = st, err
	s.mu.Unlock()
	if err != nil {
		slog.Warn("vault: 初始化失败（provider 密钥暂无法加密落盘）", "err", err)
	}
	return st, err
}

// loadProvidersFile 从 <agentDir>/providers.json 装载（文件缺失 → 空表）；
// 发现旧明文 `key` → 加密写入 vault + 写回 key_ref（幂等：迁移后再读走 key_ref 分支）。
func (s *Service) loadProvidersFile(dir string) {
	if dir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, providersFileName))
	if err != nil {
		return // 首次/未建 → 空表
	}
	var m map[string]*providerFile
	if err := json.Unmarshal(b, &m); err != nil {
		slog.Warn("providers: providers.json 解析失败（忽略）", "err", err)
		return
	}
	// vault 句柄必须在持锁外取（providerVault 自己要拿 s.mu）。
	st, verr := s.providerVault()
	loaded := make(map[string]*providerDef, len(m))
	migrated := 0
	for id, d := range m {
		if d == nil || id == "" {
			continue
		}
		p := &providerDef{ID: id, Type: d.Type, Model: d.Model}
		switch {
		case d.Key != "":
			// 旧明文档案 → 迁移（Put 幂等；覆盖写同一 ref）。
			if st == nil {
				p.Key, p.keyErr = d.Key, fmt.Errorf("vault 不可用，密钥仍为明文: %v", vaultUnavailable(verr))
				break
			}
			ref := providerRef(id)
			if err := st.Put(ref, vault.KindProviderKey, []byte(d.Key)); err != nil {
				p.Key, p.keyErr = d.Key, err
				slog.Warn("providers: 密钥加密迁移失败（保留明文，不丢密钥）", "provider", id, "err", err)
				break
			}
			p.Key, p.KeyRef = d.Key, ref
			migrated++
		case d.KeyRef != "":
			p.KeyRef = d.KeyRef
			if st == nil {
				p.keyErr = fmt.Errorf("vault 不可用，无法解密 %s: %v", d.KeyRef, vaultUnavailable(verr))
				slog.Warn("providers: 有密文但 vault 不可用", "provider", id, "ref", d.KeyRef, "err", p.keyErr)
				break
			}
			pt, err := st.Get(d.KeyRef, vault.KindProviderKey)
			if err != nil {
				p.keyErr = err
				slog.Warn("providers: 密钥密文解密失败（该 provider 在修复前不可用）", "provider", id, "err", err)
				break
			}
			p.Key = string(pt)
		}
		loaded[id] = p
	}
	s.mu.Lock()
	s.providers = loaded
	s.mu.Unlock()
	if migrated > 0 {
		// 写回：明文从文件里消失，只留 ref（幂等——第二次启动 migrated=0，文件不变）。
		if err := s.persistProvidersFile(); err != nil {
			slog.Warn("providers: 迁移写回失败", "migrated", migrated, "err", err)
			return
		}
		slog.Info("providers: 明文密钥已迁移进 vault", "count", migrated)
	}
}

// vaultUnavailable 把「没有 vault」与「vault 初始化失败」统一成可读原因。
func vaultUnavailable(err error) string {
	if err == nil {
		return "agentDir 未配置（无 forge home）"
	}
	return err.Error()
}

// persistProvidersFile 原子写 providers.json（只写引用；仅未迁移的明文档案继续写 key）。
func (s *Service) persistProvidersFile() error {
	s.mu.Lock()
	dir := s.agentDir
	cp := make(map[string]*providerFile, len(s.providers))
	for id, d := range s.providers {
		f := &providerFile{ID: id, Type: d.Type, Model: d.Model, KeyRef: d.KeyRef}
		if d.KeyRef == "" && d.Key != "" {
			// vault 不可用的降级路径：明文已在文件里，写回时必须保留（否则等于删密钥）。
			f.Key = d.Key
		}
		cp[id] = f
	}
	s.mu.Unlock()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".providers.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "providers.json"))
}

// ListProviders 列出 provider 档案概要（密钥只以 has_key 呈现，绝不读回）。
func (s *Service) ListProviders() ([]dtoprovider.ProviderResp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]dtoprovider.ProviderResp, 0, len(s.providers))
	for id, d := range s.providers {
		if d == nil {
			continue
		}
		out = append(out, dtoprovider.ProviderResp{
			ID: id, Type: d.Type, Model: d.Model, HasKey: d.Key != "" || d.KeyRef != "",
		})
	}
	return out, nil
}

// UpdateProvider upsert 档案：type/model 指针更新、key 非 nil 即覆盖/清除（空串 = 清除 ref + 密文）。
func (s *Service) UpdateProvider(_ context.Context, id string, patch dtoprovider.ProviderPatchReq) (*dtoprovider.ProviderResp, error) {
	if id == "" {
		return nil, acl.NewError(acl.ClassRequest, "invalid_provider_id", "provider id 必填", nil)
	}
	// id 会进 vault 文件名 → 必须过引用校验（防路径穿越），并给出可执行提示。
	if _, err := vault.ValidateRef(providerRef(id)); err != nil {
		return nil, acl.NewError(acl.ClassRequest, "invalid_provider_id",
			fmt.Sprintf("provider id %q 非法（仅 a-zA-Z0-9._-，1-64 字符）: %v", id, err), nil)
	}
	// vault 句柄在持锁外取（避免 s.mu 重入）。
	var st *vault.Store
	var verr error
	if patch.Key != nil && *patch.Key != "" {
		st, verr = s.providerVault()
		if verr != nil {
			// 绝不因为 vault 坏了就把明文写盘。
			return nil, acl.NewError(acl.ClassRequest, "vault_unavailable",
				"无法加密保存密钥（vault 初始化失败）: "+verr.Error(), nil)
		}
	}
	if patch.Key != nil && *patch.Key == "" {
		st, verr = s.providerVault()
		if verr != nil {
			st = nil // 清除仍应尽力而为（删不掉密文不算失败，ref 会被清掉）
		}
	}

	s.mu.Lock()
	d, ok := s.providers[id]
	if !ok {
		d = &providerDef{ID: id}
		s.providers[id] = d
	}
	if patch.Type != nil {
		d.Type = *patch.Type
	}
	if patch.Model != nil {
		d.Model = *patch.Model
	}
	oldRef := d.KeyRef
	if patch.Key != nil {
		if *patch.Key == "" {
			// 清除：删 ref + 删密文。
			d.Key, d.KeyRef, d.keyErr = "", "", nil
		} else if st != nil {
			d.Key, d.KeyRef, d.keyErr = *patch.Key, providerRef(id), nil
		} else {
			// 无 forge home（纯内存场景）：只留内存，persist 也写不出去。
			d.Key, d.KeyRef, d.keyErr = *patch.Key, "", nil
		}
	}
	s.mu.Unlock()

	if patch.Key != nil && *patch.Key != "" && st != nil {
		if err := st.Put(providerRef(id), vault.KindProviderKey, []byte(*patch.Key)); err != nil {
			s.mu.Lock()
			d.Key, d.KeyRef = "", oldRef // 回滚：密文没写成，内存也不能留假成功
			s.mu.Unlock()
			return nil, acl.NewError(acl.ClassRequest, "vault_write_failed",
				"密钥加密写入失败: "+err.Error(), nil)
		}
	}
	if patch.Key != nil && *patch.Key == "" && st != nil && oldRef != "" {
		if err := st.Delete(oldRef); err != nil {
			return nil, acl.NewError(acl.ClassRequest, "vault_delete_failed",
				"密钥密文删除失败: "+err.Error(), nil)
		}
	}
	if err := s.persistProvidersFile(); err != nil {
		return nil, err
	}
	return &dtoprovider.ProviderResp{ID: id, Type: d.Type, Model: d.Model, HasKey: d.Key != "" || d.KeyRef != ""}, nil
}

// providerEnvFor 返回给某 provider 档案注入的 env（KEY=VALUE 列表；无 key/未知档案 → nil）。
func (s *Service) providerEnvFor(providerID string) []string {
	s.mu.Lock()
	d := s.providers[providerID]
	s.mu.Unlock()
	if d == nil || d.Key == "" {
		if d != nil && d.KeyRef != "" {
			slog.Warn("providers: 密钥不可用（密文未解密），不注入 env", "provider", providerID, "err", d.keyErr)
		}
		return nil
	}
	k := envKeyOf(d.Type)
	if k == "" {
		return nil
	}
	// 登记真实密钥值（引用计数去重）：无论密钥形状如何，出现在日志/报错/事件里都必被替换。
	// 这是脱敏最可靠的一档（零误报）——provider key 是当前唯一已知的真实密钥来源。
	redact.RegisterDynamicSecret(d.Key)
	return []string{k + "=" + d.Key}
}

// CredentialProblem 校验 agent 绑定的 provider 档案当前是否可用；不可用即 credential_missing
// 并带上可执行出路。
//
// 语义：agent.provider 为空 = 不注入密钥、用底座自身登录态（合法 → nil）；非空则必须指向一个
// **存在且带 key** 的档案。悬空引用会让「本该注入的密钥静默缺失」，属配置错误——宁可显式失败，
// 也不让会话在半配置状态下启动（设计取向：不把降级藏在静默里）。密钥密文解不开（vault.key 丢失/
// 被更换）同样走这里，并直接报出解密失败原因。
func (s *Service) CredentialProblem(agent *AgentRec) error {
	if agent == nil || agent.Provider == "" {
		return nil
	}
	s.mu.Lock()
	d := s.providers[agent.Provider]
	s.mu.Unlock()
	ref := fmt.Sprintf("agent %q 绑定的 provider 档案 %q", agent.ID, agent.Provider)
	if d == nil {
		return acl.NewError(acl.ClassRequest, "credential_missing",
			ref+" 不存在：在 Agents 页新建该档案，或清空该 agent 的 provider（改用它底座自身的登录态）", nil)
	}
	if d.Key == "" {
		if d.KeyRef != "" && d.keyErr != nil {
			return acl.NewError(acl.ClassRequest, "credential_missing",
				ref+" 的密钥密文无法解密（"+d.KeyRef+"）："+d.keyErr.Error()+
					"；确认 vault.key 未丢失/未被更换（备份清单见 docs/deployment.md），或重设 key", nil)
		}
		return acl.NewError(acl.ClassRequest, "credential_missing",
			ref+" 没有密钥（本应注入 "+envKeyOf(d.Type)+"）：在 Agents 页补 key，或清空该 agent 的 provider", nil)
	}
	return nil
}
