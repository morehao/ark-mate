package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/ark-forge/pkg/acl"
)

// ——— Provider 注入面（P2）：档案只写不读回 + 启动 env 注入 ———
//
// 档案存于 <AgentDir>/providers.json：{id: {type, model, key}}。REST 语义：
// GET /providers 只回 {id,type,model,has_key}（密钥绝不读回）；PATCH /providers/:id
// 幂等 upsert（type/model 指针更新；key 非 nil 即覆盖/清除——空串 = 清除）。
// 启动注入：agent.provider 指向档案 id 时，acquire 拨号把其密钥以 <TYPE>_API_KEY=… 注入
// 子进程 env（默认映射；未知 type 取大写 type + _API_KEY）。

// providerDef 磁盘/内存中的 provider 档案。
type providerDef struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Model string `json:"model,omitempty"`
	Key   string `json:"key,omitempty"`
}

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

// loadProvidersFile 从 <agentDir>/providers.json 装载（文件缺失 → 空表）。
func (s *Service) loadProvidersFile(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.providers == nil {
		s.providers = map[string]*providerDef{}
	}
	if dir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, "providers.json"))
	if err != nil {
		return // 首次/未建 → 空表
	}
	var m map[string]*providerDef
	if err := json.Unmarshal(b, &m); err != nil {
		return
	}
	for id, d := range m {
		if d != nil && id != "" {
			d.ID = id
			s.providers[id] = d
		}
	}
}

// persistProvidersFile 原子写 providers.json。
func (s *Service) persistProvidersFile() error {
	s.mu.Lock()
	dir := s.agentDir
	cp := make(map[string]*providerDef, len(s.providers))
	for id, d := range s.providers {
		cp[id] = &providerDef{ID: id, Type: d.Type, Model: d.Model, Key: d.Key}
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
func (s *Service) ListProviders() ([]handler.ProviderResp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]handler.ProviderResp, 0, len(s.providers))
	for id, d := range s.providers {
		if d == nil {
			continue
		}
		out = append(out, handler.ProviderResp{
			ID: id, Type: d.Type, Model: d.Model, HasKey: d.Key != "",
		})
	}
	return out, nil
}

// UpdateProvider upsert 档案：type/model 指针更新、key 非 nil 即覆盖/清除。
func (s *Service) UpdateProvider(_ context.Context, id string, patch handler.ProviderPatchReq) (*handler.ProviderResp, error) {
	if id == "" {
		return nil, acl.NewError(acl.ClassRequest, "invalid_provider_id", "provider id 必填", nil)
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
	if patch.Key != nil {
		d.Key = *patch.Key // 空串 = 清除
	}
	s.mu.Unlock()
	if err := s.persistProvidersFile(); err != nil {
		return nil, err
	}
	return &handler.ProviderResp{ID: id, Type: d.Type, Model: d.Model, HasKey: d.Key != ""}, nil
}

// providerEnvFor 返回给某 provider 档案注入的 env（KEY=VALUE 列表；无 key/未知档案 → nil）。
func (s *Service) providerEnvFor(providerID string) []string {
	s.mu.Lock()
	d := s.providers[providerID]
	s.mu.Unlock()
	if d == nil || d.Key == "" {
		return nil
	}
	k := envKeyOf(d.Type)
	if k == "" {
		return nil
	}
	return []string{k + "=" + d.Key}
}

// CredentialProblem 校验 agent 绑定的 provider 档案当前是否可用；不可用即 credential_missing
// 并带上可执行出路。
//
// 语义：agent.provider 为空 = 不注入密钥、用底座自身登录态（合法 → nil）；非空则必须指向一个
// **存在且带 key** 的档案。悬空引用会让「本该注入的密钥静默缺失」，属配置错误——宁可显式失败，
// 也不让会话在半配置状态下启动（设计取向：不把降级藏在静默里）。
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
		return acl.NewError(acl.ClassRequest, "credential_missing",
			ref+" 没有密钥（本应注入 "+envKeyOf(d.Type)+"）：在 Agents 页补 key，或清空该 agent 的 provider", nil)
	}
	return nil
}
