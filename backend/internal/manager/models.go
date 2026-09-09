package manager

import (
	"context"
	"fmt"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/modelcatalog"
	"github.com/morehao/ark-forge/internal/server/handler"
)

// ——— 模型目录（设计文档 §11：三段式 live → 磁盘缓存 → go:embed 快照） ———
//
// 目录只负责「拿来一份可选的模型列表」，不做运行时注入（那属 Phase B）。ListModels 支持
// 三种定位：?agent=<id>（取该 agent 的 provider 类型）、?provider=<type>（直接指定类型）、
// 两者均空（聚合全部受支持类型）。密钥取自同类型 provider 档案（有 key 才 live，否则快照）。

// ListModels 返回模型目录。
func (s *Service) ListModels(ctx context.Context, providerType, agentID string) (*handler.ModelCatalogResp, error) {
	var out []handler.ModelOptionResp
	source := modelcatalog.SourceSnapshot

	// resolve 按类型解析：取该类型的 provider 密钥（无则空 → live 跳过），并把模型并入结果。
	resolve := func(typ string) {
		models, src, err := modelcatalog.Resolve(ctx, modelcatalog.ProviderType(typ), s.keyForType(typ), "")
		if err != nil {
			// 未知/异常类型：跳过该类型，不拖垮整批（降级为已成功类型的汇总）。
			return
		}
		source = src
		for _, m := range models {
			out = append(out, toModelOptionResp(typ, m))
		}
	}

	if agentID != "" {
		agent, ok := s.AgentByID(agentID)
		if !ok {
			return nil, s.restErr(acl.NewError(acl.ClassRequest, "agent_not_found",
				fmt.Sprintf("[agent_not_found] 未知 agent %q", agentID), nil))
		}
		if typ := s.providerTypeForAgent(agent); typ != "" {
			resolve(typ)
		}
	} else if providerType != "" {
		resolve(providerType)
	} else {
		for _, typ := range modelcatalog.SupportedTypes() {
			resolve(string(typ))
		}
	}
	return &handler.ModelCatalogResp{Source: string(source), Models: out}, nil
}

// ListProviderModels 返回某 provider 档案的可选模型目录。
func (s *Service) ListProviderModels(ctx context.Context, id string) (*handler.ModelCatalogResp, error) {
	d := s.providerByID(id)
	if d == nil {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "provider_not_found",
			fmt.Sprintf("[provider_not_found] 未知 provider 档案 %q", id), nil))
	}
	models, source, err := modelcatalog.Resolve(ctx, modelcatalog.ProviderType(d.Type), d.Key, "")
	if err != nil {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "unsupported_provider_type",
			fmt.Sprintf("[unsupported_provider_type] provider %q 类型 %q 不支持", id, d.Type), err))
	}
	out := make([]handler.ModelOptionResp, 0, len(models))
	for _, m := range models {
		out = append(out, toModelOptionResp(d.Type, m))
	}
	return &handler.ModelCatalogResp{Source: string(source), Models: out}, nil
}

// providerByID 取 provider 档案（nil = 未知）。
func (s *Service) providerByID(id string) *providerDef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.providers[id]
}

// keyForType 取第一个 type 匹配且已有密钥的 provider 档案密钥（无 → ""，live 跳过）。
func (s *Service) keyForType(typ string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.providers {
		if d != nil && d.Type == typ && d.Key != "" {
			return d.Key
		}
	}
	return ""
}

// providerTypeForAgent 解析 agent 引用 provider 档案的类型（无/未知 → ""）。
func (s *Service) providerTypeForAgent(agent *AgentRec) string {
	if agent == nil || agent.Provider == "" {
		return ""
	}
	d := s.providerByID(agent.Provider)
	if d == nil {
		return ""
	}
	return d.Type
}

// toModelOptionResp 归一化目录条目 → REST DTO。
func toModelOptionResp(typ string, m modelcatalog.Model) handler.ModelOptionResp {
	return handler.ModelOptionResp{
		Provider:    typ,
		ID:          m.ID,
		Title:       m.Title,
		Description: m.Description,
		Behavior:    m.Behavior,
		Recommended: m.Recommended,
		IsFree:      m.IsFree,
	}
}
